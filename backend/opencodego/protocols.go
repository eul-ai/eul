package opencodego

import (
	"context"
	"fmt"
	"net/http"
	"slices"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/backend/protocol"
	"github.com/eul-ai/eul/backend/protocol/anthropic"
	"github.com/eul-ai/eul/backend/protocol/chatcompletions"
	"github.com/eul-ai/eul/backend/protocol/responses"
)

func newProtocolClients(apiKey, baseURL string, httpClient *http.Client, models map[string]modelInfo) map[wireProtocol]*protocol.Client {
	newClient := func(path string, codec protocol.Codec, prepare func(*http.Request)) *protocol.Client {
		return protocol.New(protocol.Options{
			Codec:       codec,
			HTTPClient:  httpClient,
			Endpoint:    baseURL + path,
			ErrorPrefix: "opencode go",
			PrepareRequest: func(_ context.Context, request *http.Request) error {
				prepare(request)
				return nil
			},
			ContextWindow: func(model string) int64 { return models[model].contextWindow },
		})
	}
	bearer := func(request *http.Request) { setBearerHeaders(request, apiKey) }
	return map[wireProtocol]*protocol.Client{
		protocolResponses: newClient("/responses", responses.Codec{Options: func(request agent.Request) (responses.RequestOptions, error) {
			return responseRequestOptions(models, request)
		}}, bearer),
		protocolChatCompletions: newClient("/chat/completions", chatcompletions.Codec{Options: func(request agent.Request) (chatcompletions.RequestOptions, error) {
			return chatRequestOptions(models, request)
		}}, bearer),
		protocolAnthropicMessages: newClient("/messages", anthropic.Codec{Options: func(request agent.Request) (anthropic.RequestOptions, error) {
			return anthropicRequestOptions(models, request)
		}}, func(request *http.Request) { setAnthropicHeaders(request, apiKey) }),
	}
}

func setBearerHeaders(request *http.Request, apiKey string) {
	request.Header.Set("Authorization", "Bearer "+apiKey)
	request.Header.Set("User-Agent", "eul")
	request.Header.Set("x-opencode-client", "eul")
}

func (account account) authorize(request *http.Request) {
	setBearerHeaders(request, account.apiKey)
}

func setAnthropicHeaders(request *http.Request, apiKey string) {
	request.Header.Set("x-api-key", apiKey)
	request.Header.Set("anthropic-version", "2023-06-01")
	request.Header.Set("User-Agent", "eul")
	request.Header.Set("x-opencode-client", "eul")
}

func responseRequestOptions(models map[string]modelInfo, request agent.Request) (responses.RequestOptions, error) {
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

func chatRequestOptions(models map[string]modelInfo, request agent.Request) (chatcompletions.RequestOptions, error) {
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

func anthropicRequestOptions(models map[string]modelInfo, request agent.Request) (anthropic.RequestOptions, error) {
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

func validateRequestModel(models map[string]modelInfo, request agent.Request) (modelInfo, error) {
	info, ok := models[request.Model]
	if !ok {
		return modelInfo{}, fmt.Errorf("model %q is not supported", request.Model)
	}
	if !slices.Contains(info.thinking.levels, request.ThinkingLevel) {
		return modelInfo{}, fmt.Errorf("thinking level %q is not supported by model %q", request.ThinkingLevel, request.Model)
	}
	return info, nil
}
