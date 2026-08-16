package opencodego

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/eul-ai/eul/agent"
	anthropic "github.com/eul-ai/eul/backend/anthropic/messages"
	"github.com/eul-ai/eul/backend/compaction"
	"github.com/eul-ai/eul/backend/openai/chatcompletions"
	"github.com/eul-ai/eul/backend/openai/responses"
)

const maxThinkingOutputHeadroom = 8_000

type protocolClient interface {
	Generate(context.Context, agent.Request, agent.StreamObserver) (agent.Response, error)
	RetryGeneration(error, int) (time.Duration, bool)
	ShouldCompactState(agent.Request) bool
	SemanticCompact(context.Context, agent.Request, string) (agent.CompactResponse, error)
	IsContextLimitError(error) bool
}

type provider struct {
	models  map[string]modelInfo
	clients map[protocol]protocolClient
}

type routedError struct {
	protocol protocol
	cause    error
}

func (err *routedError) Error() string { return err.cause.Error() }
func (err *routedError) Unwrap() error { return err.cause }

func newProvider(apiKey, baseURL string, httpClient *http.Client, models map[string]modelInfo) (*provider, error) {
	client := &provider{models: models}

	responsesClient, err := responses.New(responses.Options{
		HTTPClient:     httpClient,
		Endpoint:       baseURL + "/responses",
		ErrorPrefix:    "opencode go",
		PrepareRequest: prepareBearerRequest(apiKey),
		RequestOptions: client.responseRequestOptions,
		Redact:         []string{apiKey},
	})
	if err != nil {
		return nil, err
	}

	chatClient, err := chatcompletions.New(chatcompletions.Options{
		HTTPClient:     httpClient,
		Endpoint:       baseURL + "/chat/completions",
		ErrorPrefix:    "opencode go",
		PrepareRequest: prepareBearerRequest(apiKey),
		RequestOptions: client.chatRequestOptions,
		Redact:         []string{apiKey},
	})
	if err != nil {
		return nil, err
	}

	anthropicClient, err := anthropic.New(anthropic.Options{
		HTTPClient:     httpClient,
		Endpoint:       baseURL + "/messages",
		ErrorPrefix:    "opencode go",
		PrepareRequest: prepareAnthropicRequest(apiKey),
		RequestOptions: client.anthropicRequestOptions,
		PromptCaching:  true,
		Redact:         []string{apiKey},
	})
	if err != nil {
		return nil, err
	}

	client.clients = map[protocol]protocolClient{
		protocolResponses:         responsesClient,
		protocolChatCompletions:   chatClient,
		protocolAnthropicMessages: anthropicClient,
	}
	return client, nil
}

func (client *provider) Generate(ctx context.Context, request agent.Request, observer agent.StreamObserver) (agent.Response, error) {
	info, ok := client.models[request.Model]
	if !ok {
		return agent.Response{}, fmt.Errorf("opencode go: model %q is not supported", request.Model)
	}

	selectedClient, ok := client.clients[info.protocol]
	if !ok {
		return agent.Response{}, fmt.Errorf("opencode go: model %q has no protocol", request.Model)
	}
	response, err := selectedClient.Generate(ctx, request, observer)
	if err != nil {
		return agent.Response{}, &routedError{protocol: info.protocol, cause: err}
	}
	return response, nil
}

func (client *provider) RetryGeneration(err error, failedAttempts int) (time.Duration, bool) {
	var routed *routedError
	if !errors.As(err, &routed) {
		return 0, false
	}
	selectedClient, ok := client.clients[routed.protocol]
	if !ok {
		return 0, false
	}
	return selectedClient.RetryGeneration(routed.cause, failedAttempts)
}

func (client *provider) ShouldCompact(request agent.Request, usage agent.Usage) bool {
	info, ok := client.models[request.Model]
	if !ok {
		return false
	}

	selectedClient, ok := client.clients[info.protocol]
	if !ok {
		return false
	}
	if selectedClient.ShouldCompactState(request) {
		return true
	}
	if usage.TotalTokens <= 0 || info.contextWindow <= 0 {
		return false
	}

	limit := info.contextWindow * 9 / 10
	if usage.TotalTokens >= limit {
		return true
	}
	return estimateInputTokens(request.Inputs) >= limit-usage.TotalTokens
}

func (client *provider) Compact(ctx context.Context, request agent.Request) (agent.CompactResponse, error) {
	info, ok := client.models[request.Model]
	if !ok {
		return agent.CompactResponse{}, fmt.Errorf("opencode go: model %q is not supported", request.Model)
	}

	selectedClient, ok := client.clients[info.protocol]
	if !ok {
		return agent.CompactResponse{}, fmt.Errorf("opencode go: model %q has no protocol", request.Model)
	}
	response, err := selectedClient.SemanticCompact(ctx, request, compaction.Instructions)
	if err != nil {
		return agent.CompactResponse{}, &routedError{protocol: info.protocol, cause: err}
	}
	return response, nil
}

func (client *provider) ShouldCompactAfterError(request agent.Request, err error) bool {
	info, ok := client.models[request.Model]
	if !ok {
		return false
	}
	var routed *routedError
	if !errors.As(err, &routed) || routed.protocol != info.protocol {
		return false
	}

	selectedClient, ok := client.clients[info.protocol]
	return ok && selectedClient.IsContextLimitError(routed.cause)
}

func prepareBearerRequest(apiKey string) func(context.Context, *http.Request) error {
	return func(_ context.Context, request *http.Request) error {
		request.Header.Set("Authorization", "Bearer "+apiKey)
		request.Header.Set("User-Agent", "eul")
		request.Header.Set("x-opencode-client", "eul")
		return nil
	}
}

func prepareAnthropicRequest(apiKey string) func(context.Context, *http.Request) error {
	return func(_ context.Context, request *http.Request) error {
		request.Header.Set("x-api-key", apiKey)
		request.Header.Set("anthropic-version", "2023-06-01")
		request.Header.Set("User-Agent", "eul")
		request.Header.Set("x-opencode-client", "eul")
		return nil
	}
}

func (client *provider) responseRequestOptions(request agent.Request) (responses.RequestOptions, error) {
	info, err := client.validateRequestModel(request, protocolResponses)
	if err != nil {
		return responses.RequestOptions{}, err
	}

	options := responses.RequestOptions{ToolChoice: "auto", ParallelToolCalls: true}
	if info.thinkingMode == thinkingEffort {
		reasoning := &responses.Reasoning{Effort: info.thinkingEfforts[request.ThinkingLevel]}
		if request.ThinkingLevel != agent.ThinkingOff {
			reasoning.Summary = "auto"
		}
		options.Reasoning = reasoning
	}
	if info.includeEncryptedState {
		options.Include = []string{"reasoning.encrypted_content"}
	}
	if info.lowTextVerbosity {
		options.TextVerbosity = "low"
	}
	return options, nil
}

func (client *provider) chatRequestOptions(request agent.Request) (chatcompletions.RequestOptions, error) {
	info, err := client.validateRequestModel(request, protocolChatCompletions)
	if err != nil {
		return chatcompletions.RequestOptions{}, err
	}

	options := chatcompletions.RequestOptions{
		MaxTokens:                 info.maxOutputTokens,
		ToolChoice:                "auto",
		ParallelToolCalls:         true,
		SerializeReasoningContent: info.serializeReasoningContent,
	}
	if info.thinkingMode == thinkingEffort {
		options.ReasoningEffort = info.thinkingEfforts[request.ThinkingLevel]
	}
	return options, nil
}

func (client *provider) anthropicRequestOptions(request agent.Request) (anthropic.RequestOptions, error) {
	info, err := client.validateRequestModel(request, protocolAnthropicMessages)
	if err != nil {
		return anthropic.RequestOptions{}, err
	}

	options := anthropic.RequestOptions{
		MaxTokens:  info.maxOutputTokens,
		ToolChoice: &anthropic.ToolChoice{Type: "auto"},
	}
	switch info.thinkingMode {
	case thinkingBudget:
		budget := 16_000
		if request.ThinkingLevel == agent.ThinkingMax {
			budget = info.maxOutputTokens - maxThinkingOutputHeadroom
		}
		options.Thinking = &anthropic.Thinking{Type: "enabled", BudgetTokens: budget}
	case thinkingAdaptive:
		if request.ThinkingLevel == agent.ThinkingOff {
			options.Thinking = &anthropic.Thinking{Type: "disabled"}
		} else {
			options.Thinking = &anthropic.Thinking{Type: "adaptive"}
		}
	}
	return options, nil
}

func (client *provider) validateRequestModel(request agent.Request, expected protocol) (modelInfo, error) {
	info, ok := client.models[request.Model]
	if !ok || info.protocol != expected {
		return modelInfo{}, fmt.Errorf("model %q does not use the requested protocol", request.Model)
	}
	if !slices.Contains(info.thinkingLevels, request.ThinkingLevel) {
		return modelInfo{}, fmt.Errorf("thinking level %q is not supported by model %q", request.ThinkingLevel, request.Model)
	}
	return info, nil
}

func estimateInputTokens(inputs []agent.Input) int64 {
	var total int64
	for _, input := range inputs {
		textBytes := len(input.Text)
		if input.Kind == agent.InputUser {
			textBytes = len(input.PlainText())
			for _, part := range input.Content {
				if part.Kind == agent.ContentPartImage {
					total += 1_024
				}
			}
		}
		bytes := int64(textBytes)
		total += bytes / 4
		if bytes%4 != 0 {
			total++
		}
	}
	return total
}

var (
	_ agent.Provider              = (*provider)(nil)
	_ agent.GenerationRetryPolicy = (*provider)(nil)
	_ agent.Compactor             = (*provider)(nil)
	_ agent.CompactionErrorPolicy = (*provider)(nil)
)
