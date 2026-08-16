package opencodego

import (
	"context"
	"fmt"
	"net/http"
	"slices"

	"github.com/eul-ai/eul/agent"
	anthropic "github.com/eul-ai/eul/backend/anthropic/messages"
	"github.com/eul-ai/eul/backend/openai/chatcompletions"
	"github.com/eul-ai/eul/backend/openai/responses"
)

func newProtocolClients(apiKey, baseURL string, httpClient *http.Client, models map[string]modelInfo) (map[protocol]protocolClient, error) {
	responsesClient, err := responses.New(responses.Options{
		HTTPClient:     httpClient,
		Endpoint:       baseURL + "/responses",
		ErrorPrefix:    "opencode go",
		PrepareRequest: prepareBearerRequest(apiKey),
		RequestOptions: func(request agent.Request) (responses.RequestOptions, error) {
			return responseRequestOptions(models, request)
		},
	})
	if err != nil {
		return nil, err
	}

	chatClient, err := chatcompletions.New(chatcompletions.Options{
		HTTPClient:     httpClient,
		Endpoint:       baseURL + "/chat/completions",
		ErrorPrefix:    "opencode go",
		PrepareRequest: prepareBearerRequest(apiKey),
		RequestOptions: func(request agent.Request) (chatcompletions.RequestOptions, error) {
			return chatRequestOptions(models, request)
		},
	})
	if err != nil {
		return nil, err
	}

	anthropicClient, err := anthropic.New(anthropic.Options{
		HTTPClient:     httpClient,
		Endpoint:       baseURL + "/messages",
		ErrorPrefix:    "opencode go",
		PrepareRequest: prepareAnthropicRequest(apiKey),
		RequestOptions: func(request agent.Request) (anthropic.RequestOptions, error) {
			return anthropicRequestOptions(models, request)
		},
		PromptCaching: true,
	})
	if err != nil {
		return nil, err
	}

	return map[protocol]protocolClient{
		protocolResponses:         responsesClient,
		protocolChatCompletions:   chatClient,
		protocolAnthropicMessages: anthropicClient,
	}, nil
}

func prepareBearerRequest(apiKey string) func(context.Context, *http.Request) error {
	return func(_ context.Context, request *http.Request) error {
		setBearerHeaders(request, apiKey)
		return nil
	}
}

func setBearerHeaders(request *http.Request, apiKey string) {
	request.Header.Set("Authorization", "Bearer "+apiKey)
	request.Header.Set("User-Agent", "eul")
	request.Header.Set("x-opencode-client", "eul")
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

func responseRequestOptions(models map[string]modelInfo, request agent.Request) (responses.RequestOptions, error) {
	info, err := validateRequestModel(models, request, protocolResponses)
	if err != nil {
		return responses.RequestOptions{}, err
	}

	config := info.protocol.(responsesConfig)
	options := responses.RequestOptions{
		SessionID:         request.SessionID,
		Include:           []string{"reasoning.encrypted_content"},
		ToolChoice:        "auto",
		ParallelToolCalls: true,
	}
	if info.thinking.mode == thinkingEffort {
		reasoning := &responses.Reasoning{Effort: info.thinking.efforts[request.ThinkingLevel]}
		if request.ThinkingLevel != agent.ThinkingOff {
			reasoning.Summary = "auto"
		}
		options.Reasoning = reasoning
	}
	if config.lowTextVerbosity {
		options.TextVerbosity = "low"
	}
	return options, nil
}

func chatRequestOptions(models map[string]modelInfo, request agent.Request) (chatcompletions.RequestOptions, error) {
	info, err := validateRequestModel(models, request, protocolChatCompletions)
	if err != nil {
		return chatcompletions.RequestOptions{}, err
	}

	config := info.protocol.(chatCompletionsConfig)
	options := chatcompletions.RequestOptions{
		MaxTokens:                 config.maxOutputTokens,
		ToolChoice:                "auto",
		ParallelToolCalls:         true,
		SerializeReasoningContent: config.serializeReasoningContent,
	}
	if info.thinking.mode == thinkingEffort {
		options.ReasoningEffort = info.thinking.efforts[request.ThinkingLevel]
	}
	return options, nil
}

func anthropicRequestOptions(models map[string]modelInfo, request agent.Request) (anthropic.RequestOptions, error) {
	info, err := validateRequestModel(models, request, protocolAnthropicMessages)
	if err != nil {
		return anthropic.RequestOptions{}, err
	}

	config := info.protocol.(anthropicMessagesConfig)
	options := anthropic.RequestOptions{
		MaxTokens:  config.maxOutputTokens,
		ToolChoice: &anthropic.ToolChoice{Type: "auto"},
	}
	switch info.thinking.mode {
	case thinkingBudget:
		budget := highThinkingBudgetTokens
		if request.ThinkingLevel == agent.ThinkingMax {
			budget = info.thinking.maxBudgetTokens
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

func validateRequestModel(models map[string]modelInfo, request agent.Request, expected protocol) (modelInfo, error) {
	info, ok := models[request.Model]
	if !ok || info.protocol.protocol() != expected {
		return modelInfo{}, fmt.Errorf("model %q does not use the requested protocol", request.Model)
	}
	if !slices.Contains(info.thinking.levels, request.ThinkingLevel) {
		return modelInfo{}, fmt.Errorf("thinking level %q is not supported by model %q", request.ThinkingLevel, request.Model)
	}
	return info, nil
}
