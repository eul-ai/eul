package opencodego

import (
	"context"
	"fmt"
	"net/http"
	"slices"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/backend"
	"github.com/eul-ai/eul/backend/protocol/anthropic"
	"github.com/eul-ai/eul/backend/protocol/chatcompletions"
	"github.com/eul-ai/eul/backend/protocol/responses"
)

func newProtocolClients(apiKey, baseURL string, httpClient *http.Client, models *backend.Models[modelInfo]) map[wireProtocol]protocolClient {
	return map[wireProtocol]protocolClient{
		protocolResponses: responses.New(responses.Options{
			HTTPClient:     httpClient,
			Endpoint:       baseURL + "/responses",
			ErrorPrefix:    "opencode go",
			PrepareRequest: prepareBearerRequest(apiKey),
			RequestOptions: func(request agent.Request) (responses.RequestOptions, error) {
				return responseRequestOptions(models, request)
			},
		}),
		protocolChatCompletions: chatcompletions.New(chatcompletions.Options{
			HTTPClient:     httpClient,
			Endpoint:       baseURL + "/chat/completions",
			ErrorPrefix:    "opencode go",
			PrepareRequest: prepareBearerRequest(apiKey),
			RequestOptions: func(request agent.Request) (chatcompletions.RequestOptions, error) {
				return chatRequestOptions(models, request)
			},
		}),
		protocolAnthropicMessages: anthropic.New(anthropic.Options{
			HTTPClient:     httpClient,
			Endpoint:       baseURL + "/messages",
			ErrorPrefix:    "opencode go",
			PrepareRequest: prepareAnthropicRequest(apiKey),
			RequestOptions: func(request agent.Request) (anthropic.RequestOptions, error) {
				return anthropicRequestOptions(models, request)
			},
			PromptCaching: true,
		}),
	}
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

func (configured *runtime) authorize(request *http.Request) {
	setBearerHeaders(request, configured.apiKey)
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

func responseRequestOptions(models *backend.Models[modelInfo], request agent.Request) (responses.RequestOptions, error) {
	info, err := validateRequestModel(models, request)
	if err != nil {
		return responses.RequestOptions{}, err
	}

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
	if info.lowTextVerbosity {
		options.TextVerbosity = "low"
	}
	return options, nil
}

func chatRequestOptions(models *backend.Models[modelInfo], request agent.Request) (chatcompletions.RequestOptions, error) {
	info, err := validateRequestModel(models, request)
	if err != nil {
		return chatcompletions.RequestOptions{}, err
	}

	options := chatcompletions.RequestOptions{
		MaxTokens:                 info.maxOutputTokens,
		ToolChoice:                "auto",
		ParallelToolCalls:         true,
		SerializeReasoningContent: info.serializeReasoningContent,
	}
	if info.thinking.mode == thinkingEffort {
		options.ReasoningEffort = info.thinking.efforts[request.ThinkingLevel]
	}
	return options, nil
}

func anthropicRequestOptions(models *backend.Models[modelInfo], request agent.Request) (anthropic.RequestOptions, error) {
	info, err := validateRequestModel(models, request)
	if err != nil {
		return anthropic.RequestOptions{}, err
	}

	options := anthropic.RequestOptions{
		MaxTokens:  info.maxOutputTokens,
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

func validateRequestModel(models *backend.Models[modelInfo], request agent.Request) (modelInfo, error) {
	info, err := models.Lookup(request.Model)
	if err != nil {
		return modelInfo{}, err
	}
	if !slices.Contains(info.thinking.levels, request.ThinkingLevel) {
		return modelInfo{}, fmt.Errorf("thinking level %q is not supported by model %q", request.ThinkingLevel, request.Model)
	}
	return info, nil
}
