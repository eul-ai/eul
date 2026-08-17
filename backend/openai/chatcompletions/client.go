package chatcompletions

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/backend/compaction"
	"github.com/eul-ai/eul/backend/continuation"
	backendhttp "github.com/eul-ai/eul/backend/httpclient"
)

type RequestOptions struct {
	MaxTokens                 int
	ReasoningEffort           string
	ToolChoice                string
	ParallelToolCalls         bool
	SerializeReasoningContent bool
}

type RequestOptionsFunc func(agent.Request) (RequestOptions, error)

type PrepareRequestFunc func(context.Context, *http.Request) error

type Options struct {
	HTTPClient          *http.Client
	Endpoint            string
	ErrorPrefix         string
	PrepareRequest      PrepareRequestFunc
	RequestOptions      RequestOptionsFunc
	MaxRequestBytes     int64
	MaxResponseBytes    int64
	MaxErrorBytes       int64
	MaxStateBytes       int
	StateOutputHeadroom int
}

type Client struct {
	httpClient          *http.Client
	endpoint            string
	errorConfig         backendhttp.APIErrorConfig
	prepareRequest      PrepareRequestFunc
	requestOptions      RequestOptionsFunc
	maxRequestBytes     int64
	maxResponseBytes    int64
	maxStateBytes       int
	stateOutputHeadroom int
}

func New(options Options) (*Client, error) {
	if strings.TrimSpace(options.Endpoint) == "" {
		return nil, errors.New("chat completions: endpoint is required")
	}

	limits := (backendhttp.GenerationLimits{
		RequestBytes:  options.MaxRequestBytes,
		ResponseBytes: options.MaxResponseBytes,
		ErrorBytes:    options.MaxErrorBytes,
	}).WithDefaults()
	maxStateBytes := options.MaxStateBytes
	if maxStateBytes <= 0 {
		maxStateBytes = continuation.DefaultMaximumBytes
	}
	return &Client{
		httpClient: backendhttp.CloneNoRedirects(options.HTTPClient, backendhttp.DefaultGenerationHTTPTimeout),
		endpoint:   options.Endpoint,
		errorConfig: backendhttp.APIErrorConfig{
			Prefix:  strings.TrimSpace(options.ErrorPrefix),
			Maximum: limits.ErrorBytes,
		},
		prepareRequest:      options.PrepareRequest,
		requestOptions:      options.RequestOptions,
		maxRequestBytes:     limits.RequestBytes,
		maxResponseBytes:    limits.ResponseBytes,
		maxStateBytes:       maxStateBytes,
		stateOutputHeadroom: options.StateOutputHeadroom,
	}, nil
}

func (client *Client) generationStateBytes() int {
	return continuation.GenerationStateBytes(client.maxStateBytes, client.stateOutputHeadroom)
}

func (client *Client) ShouldCompactState(request agent.Request) bool {
	if len(request.State) == 0 {
		return false
	}
	if _, err := buildGenerationRequest(request, client.maxStateBytes, client.generationStateBytes()); err == nil {
		return false
	}

	withoutState := request
	withoutState.State = nil
	_, err := buildGenerationRequest(withoutState, client.maxStateBytes, client.generationStateBytes())
	return err == nil
}

func (client *Client) Generate(ctx context.Context, request agent.Request, observer agent.StreamObserver) (agent.Response, error) {
	if err := ctx.Err(); err != nil {
		return agent.Response{}, err
	}

	build, err := buildGenerationRequest(request, client.maxStateBytes, client.generationStateBytes())
	if err != nil {
		return agent.Response{}, client.errorf("build request: %v", err)
	}
	options, err := client.optionsFor(request)
	if err != nil {
		return agent.Response{}, client.errorf("%v", err)
	}
	build.wire = configureGenerationRequest(configureCommonRequest(build.wire, options), options)

	result, err := client.complete(ctx, build.wire, observer, "request")
	if err != nil {
		return agent.Response{}, err
	}

	output := []json.RawMessage{result.assistant}
	state, err := continuation.Encode(client.maxStateBytes, build.history, build.newMessages, output)
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

func (client *Client) SemanticCompact(ctx context.Context, request agent.Request, instructions string) (agent.CompactResponse, error) {
	if err := ctx.Err(); err != nil {
		return agent.CompactResponse{}, err
	}

	request, continueAfterCompaction := compaction.Prepare(request, instructions)
	build, err := buildWireRequest(request, client.maxStateBytes)
	if err != nil {
		return agent.CompactResponse{}, client.errorf("build summary request: %v", err)
	}
	options, err := client.optionsFor(request)
	if err != nil {
		return agent.CompactResponse{}, client.errorf("%v", err)
	}
	build.wire = configureCommonRequest(build.wire, options)

	result, err := client.complete(ctx, build.wire, agent.StreamObserver{}, "summary request")
	if err != nil {
		return agent.CompactResponse{}, err
	}
	summary, err := compaction.ValidateSummary(result.text, len(result.calls))
	if err != nil {
		return agent.CompactResponse{}, client.errorf("%v", err)
	}

	questionRaw, _ := json.Marshal(message{Role: "user", Content: compaction.SummaryQuestion})
	summaryRaw, _ := json.Marshal(assistantMessage{
		Role:             "assistant",
		Content:          compaction.FormatSummary(summary),
		ReasoningContent: reasoningContent("", build.wire.serializeReasoningContent),
	})
	messages := []json.RawMessage{questionRaw, summaryRaw}
	if continueAfterCompaction {
		continuationMessage, _ := json.Marshal(message{Role: "user", Content: compaction.Continuation})
		messages = append(messages, continuationMessage)
	}
	state, err := continuation.Encode(client.generationStateBytes(), messages)
	if err != nil {
		return agent.CompactResponse{}, client.errorf("encode summary state: %v", err)
	}
	return agent.CompactResponse{State: state, Usage: result.usage}, nil
}

func (client *Client) complete(ctx context.Context, wireRequest createRequest, observer agent.StreamObserver, operation string) (streamResult, error) {
	return backendhttp.CompleteJSONSSE(
		ctx,
		backendhttp.JSONSSEConfig{
			HTTPClient:       client.httpClient,
			Endpoint:         client.endpoint,
			ErrorConfig:      client.errorConfig,
			PrepareRequest:   client.prepareRequest,
			MaxRequestBytes:  client.maxRequestBytes,
			MaxResponseBytes: client.maxResponseBytes,
		},
		wireRequest,
		operation,
		func(reader io.Reader, maximum int64) (streamResult, error) {
			return readCompletionSSE(reader, maximum, observer, wireRequest.serializeReasoningContent)
		},
		backendhttp.IsObserverError,
	)
}

func (client *Client) optionsFor(request agent.Request) (RequestOptions, error) {
	if client.requestOptions == nil {
		return RequestOptions{}, nil
	}
	return client.requestOptions(request)
}

func configureCommonRequest(wireRequest createRequest, options RequestOptions) createRequest {
	wireRequest.Stream = true
	wireRequest.StreamOptions = &streamOptions{IncludeUsage: true}
	wireRequest.MaxTokens = options.MaxTokens
	wireRequest.ReasoningEffort = options.ReasoningEffort
	wireRequest.serializeReasoningContent = options.SerializeReasoningContent
	return wireRequest
}

func configureGenerationRequest(wireRequest createRequest, options RequestOptions) createRequest {
	if len(wireRequest.Tools) == 0 {
		return wireRequest
	}
	wireRequest.ToolChoice = options.ToolChoice
	if options.ParallelToolCalls {
		enabled := true
		wireRequest.ParallelToolCalls = &enabled
	}
	return wireRequest
}
