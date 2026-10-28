package messages

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/backend/compaction"
	"github.com/eul-ai/eul/backend/continuation"
	backendhttp "github.com/eul-ai/eul/backend/httpclient"
)

const minimumThinkingBudgetTokens = 1024

type RequestOptions struct {
	MaxTokens  int
	Thinking   *Thinking
	ToolChoice *ToolChoice
}

type RequestOptionsFunc func(agent.Request) (RequestOptions, error)

type PrepareRequestFunc func(context.Context, *http.Request) error

type Options struct {
	HTTPClient     *http.Client
	Endpoint       string
	ErrorPrefix    string
	PrepareRequest PrepareRequestFunc
	RequestOptions RequestOptionsFunc
	PromptCaching  bool
}

type Client struct {
	httpClient     *http.Client
	endpoint       string
	errorConfig    backendhttp.APIErrorConfig
	prepareRequest PrepareRequestFunc
	requestOptions RequestOptionsFunc
	promptCaching  bool
}

func New(options Options) *Client {
	return &Client{
		httpClient:     backendhttp.CloneNoRedirects(options.HTTPClient, 0),
		endpoint:       options.Endpoint,
		errorConfig:    backendhttp.APIErrorConfig{Prefix: options.ErrorPrefix},
		prepareRequest: options.PrepareRequest,
		requestOptions: options.RequestOptions,
		promptCaching:  options.PromptCaching,
	}
}

func (client *Client) ShouldCompactState(request agent.Request) bool {
	if len(request.State) == 0 {
		return false
	}
	if _, err := buildGenerationRequest(request); err == nil {
		return false
	}

	withoutState := request
	withoutState.State = nil
	_, err := buildGenerationRequest(withoutState)
	return err == nil
}

func (client *Client) Generate(ctx context.Context, request agent.Request, observer agent.StreamObserver) (agent.Response, error) {
	if err := ctx.Err(); err != nil {
		return agent.Response{}, err
	}

	build, err := buildGenerationRequest(request)
	if err != nil {
		return agent.Response{}, client.errorf("build request: %v", err)
	}
	if err := client.configureRequest(request, &build.wire); err != nil {
		return agent.Response{}, client.errorf("%v", err)
	}

	result, err := client.complete(ctx, build.wire, observer, "request")
	if err != nil {
		return agent.Response{}, err
	}

	output := []json.RawMessage{result.assistant}
	state, err := continuation.Encode(continuation.MaximumBytes, build.history, build.newMessages, output)
	if err != nil {
		return agent.Response{}, client.errorf("%v", err)
	}

	return agent.Response{
		Text:      result.text,
		ToolCalls: result.calls,
		State:     state,
		Usage:     result.usage,
	}, nil
}

func (client *Client) SemanticCompact(ctx context.Context, request agent.Request) (agent.CompactResponse, error) {
	if err := ctx.Err(); err != nil {
		return agent.CompactResponse{}, err
	}

	request, continueAfterCompaction := compaction.Prepare(request)
	build, err := buildWireRequest(request)
	if err != nil {
		return agent.CompactResponse{}, client.errorf("build summary request: %v", err)
	}
	if err := client.configureRequest(request, &build.wire); err != nil {
		return agent.CompactResponse{}, client.errorf("%v", err)
	}
	build.wire.Tools = nil
	build.wire.ToolChoice = nil

	result, err := client.complete(ctx, build.wire, agent.StreamObserver{}, "summary request")
	if err != nil {
		return agent.CompactResponse{}, err
	}
	summary, err := compaction.ValidateSummary(result.text, len(result.calls))
	if err != nil {
		return agent.CompactResponse{}, client.errorf("%v", err)
	}

	question, err := marshalWireMessage("user", []contentBlock{{Type: "text", Text: compaction.SummaryQuestion}})
	if err != nil {
		return agent.CompactResponse{}, client.errorf("%v", err)
	}
	summaryMessage, err := marshalWireMessage("assistant", []contentBlock{{Type: "text", Text: compaction.FormatSummary(summary)}})
	if err != nil {
		return agent.CompactResponse{}, client.errorf("%v", err)
	}
	messages := []json.RawMessage{question, summaryMessage}
	if continueAfterCompaction {
		continuation, err := marshalWireMessage("user", []contentBlock{{Type: "text", Text: compaction.Continuation}})
		if err != nil {
			return agent.CompactResponse{}, client.errorf("%v", err)
		}
		messages = append(messages, continuation)
	}
	state, err := continuation.Encode(continuation.GenerationBytes, messages)
	if err != nil {
		return agent.CompactResponse{}, client.errorf("encode summary state: %v", err)
	}
	return agent.CompactResponse{State: state, Usage: result.usage}, nil
}

func (client *Client) complete(ctx context.Context, wireRequest createRequest, observer agent.StreamObserver, operation string) (streamResult, error) {
	if client.promptCaching {
		var err error
		wireRequest, err = withPromptCacheControl(wireRequest)
		if err != nil {
			return streamResult{}, client.errorf("configure %s prompt caching: %v", operation, err)
		}
	}

	config := backendhttp.JSONSSEConfig{
		HTTPClient:  client.httpClient,
		Endpoint:    client.endpoint,
		ErrorConfig: client.errorConfig,
	}
	if client.prepareRequest != nil {
		config.PrepareRequest = func(ctx context.Context, request *http.Request) error {
			if err := client.prepareRequest(ctx, request); err != nil {
				return client.wrapf(err, "prepare %s: %v", operation, err)
			}
			return nil
		}
	}
	return backendhttp.CompleteJSONSSE(
		ctx,
		config,
		wireRequest,
		operation,
		func(reader io.Reader, maximum int64) (streamResult, error) {
			return readMessagesSSE(reader, maximum, observer)
		},
	)
}

func (client *Client) configureRequest(request agent.Request, wireRequest *createRequest) error {
	options := RequestOptions{}
	if client.requestOptions != nil {
		var err error
		options, err = client.requestOptions(request)
		if err != nil {
			return err
		}
	}
	if options.MaxTokens <= 0 {
		return errors.New("max tokens must be positive")
	}
	if options.Thinking != nil && options.Thinking.Type == "enabled" {
		if options.Thinking.BudgetTokens < minimumThinkingBudgetTokens {
			return errors.New("thinking budget must be at least 1024 tokens")
		}
		if options.Thinking.BudgetTokens >= options.MaxTokens {
			return errors.New("thinking budget must be less than max tokens")
		}
	}

	wireRequest.MaxTokens = options.MaxTokens
	wireRequest.Stream = true
	if options.Thinking != nil {
		thinking := *options.Thinking
		wireRequest.Thinking = &thinking
	}
	if len(wireRequest.Tools) != 0 && options.ToolChoice != nil {
		choice := *options.ToolChoice
		wireRequest.ToolChoice = &choice
	}
	return nil
}
