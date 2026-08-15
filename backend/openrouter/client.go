package openrouter

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/backend/openai/responses"
)

type client struct {
	responses *responses.Client
	metadata  func(string) modelMetadata
}

var (
	_ agent.Provider              = (*client)(nil)
	_ agent.GenerationRetryPolicy = (*client)(nil)
	_ agent.Compactor             = (*client)(nil)
	_ agent.CompactionErrorPolicy = (*client)(nil)
)

const compactionInstructions = `Create a concise, standalone handoff summary of the conversation so another coding agent can continue the task.

Preserve only continuation-critical facts: the user's current goal, requirements, and constraints; important decisions and rationale; relevant files, symbols, and code details; changes already made; commands and tests run with their outcomes; errors and unresolved issues; and the exact next steps. Include pending user requests and relevant tool findings. Do not continue the task or address the user. Output only the summary.`

func newClient(apiKey, endpoint string, httpClient *http.Client, metadata func(string) modelMetadata) (*client, error) {
	shared, err := responses.New(responses.Options{
		HTTPClient:     httpClient,
		Endpoint:       endpoint,
		ErrorPrefix:    "openrouter",
		PrepareRequest: prepareRequest(apiKey),
		RequestOptions: requestOptions(metadata),
		Redact:         []string{apiKey},
	})
	if err != nil {
		return nil, err
	}
	return &client{responses: shared, metadata: metadata}, nil
}

func (c *client) Generate(ctx context.Context, request agent.Request, observer agent.StreamObserver) (agent.Response, error) {
	return c.responses.Generate(ctx, request, observer)
}

func (c *client) ShouldCompact(request agent.Request, usage agent.Usage) bool {
	if len(request.State) == 0 {
		return false
	}
	if c.responses.ShouldCompactState(request) {
		return true
	}
	if usage.TotalTokens <= 0 {
		return false
	}

	contextWindow := c.metadata(request.Model).contextWindow
	if contextWindow <= 0 {
		return false
	}
	limit := contextWindow * 9 / 10
	if usage.TotalTokens >= limit {
		return true
	}

	return estimateInputTokens(request.Inputs) >= limit-usage.TotalTokens
}

func (c *client) Compact(ctx context.Context, request agent.Request) (agent.CompactResponse, error) {
	return c.responses.SemanticCompact(ctx, request, compactionInstructions)
}

func (c *client) ShouldCompactAfterError(_ agent.Request, err error) bool {
	return c.responses.IsContextLimitError(err)
}

func estimateInputTokens(inputs []agent.Input) int64 {
	var total int64
	for _, input := range inputs {
		textBytes := len(input.Text)
		if input.Kind == agent.InputUser {
			textBytes = len(input.PlainText())
		}
		bytes := int64(textBytes)
		total += bytes / 4
		if bytes%4 != 0 {
			total++
		}
	}
	return total
}

func (c *client) RetryGeneration(err error, failedAttempts int) (time.Duration, bool) {
	return c.responses.RetryGeneration(err, failedAttempts)
}

func prepareRequest(apiKey string) responses.PrepareRequestFunc {
	return func(_ context.Context, request *http.Request) error {
		request.Header.Set("Authorization", "Bearer "+apiKey)
		request.Header.Set("HTTP-Referer", "https://github.com/eul-ai/eul")
		request.Header.Set("X-Title", "Eul")
		request.Header.Set("User-Agent", "eul")
		return nil
	}
}

func requestOptions(metadataFor func(string) modelMetadata) responses.RequestOptionsFunc {
	return func(request agent.Request) (responses.RequestOptions, error) {
		metadata := metadataFor(request.Model)
		level := request.ThinkingLevel
		if level == "" {
			level = metadata.defaultThinkingLevel
			if level == "" && !metadata.reasoning {
				level = agent.ThinkingOff
			}
		}

		supported := !metadata.reasoning && level == agent.ThinkingOff
		for _, candidate := range metadata.thinkingLevels {
			if candidate == level {
				supported = true
				break
			}
		}
		if !supported {
			return responses.RequestOptions{}, &unsupportedThinkingLevelError{level: level, model: request.Model}
		}

		options := responses.RequestOptions{SessionID: request.SessionID, ToolChoice: "auto", ParallelToolCalls: true}
		if metadata.reasoning {
			effort := string(level)
			if level == agent.ThinkingOff {
				effort = "none"
			}
			options.Reasoning = &responses.Reasoning{Effort: effort}
			options.Include = []string{"reasoning.encrypted_content"}
		}
		return options, nil
	}
}

type unsupportedThinkingLevelError struct {
	level agent.ThinkingLevel
	model string
}

func (err *unsupportedThinkingLevelError) Error() string {
	return fmt.Sprintf("thinking level %q is not supported by model %q", err.level, err.model)
}
