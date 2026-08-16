package chatcompletions

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/backend/compaction"
	"github.com/eul-ai/eul/backend/continuation"
	backendhttp "github.com/eul-ai/eul/backend/httpclient"
)

const (
	defaultHTTPTimeout      = 10 * time.Minute
	defaultMaxRequestBytes  = int64(32 * 1024 * 1024)
	defaultMaxResponseBytes = int64(16 * 1024 * 1024)
	defaultMaxErrorBytes    = int64(64 * 1024)
	defaultMaxStateBytes    = 16 * 1024 * 1024
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

	maxRequestBytes := options.MaxRequestBytes
	if maxRequestBytes <= 0 {
		maxRequestBytes = defaultMaxRequestBytes
	}
	maxResponseBytes := options.MaxResponseBytes
	if maxResponseBytes <= 0 {
		maxResponseBytes = defaultMaxResponseBytes
	}
	maxErrorBytes := options.MaxErrorBytes
	if maxErrorBytes <= 0 {
		maxErrorBytes = defaultMaxErrorBytes
	}
	maxStateBytes := options.MaxStateBytes
	if maxStateBytes <= 0 {
		maxStateBytes = defaultMaxStateBytes
	}
	return &Client{
		httpClient: backendhttp.CloneNoRedirects(options.HTTPClient, defaultHTTPTimeout),
		endpoint:   options.Endpoint,
		errorConfig: backendhttp.APIErrorConfig{
			Prefix:  strings.TrimSpace(options.ErrorPrefix),
			Maximum: maxErrorBytes,
		},
		prepareRequest:      options.PrepareRequest,
		requestOptions:      options.RequestOptions,
		maxRequestBytes:     maxRequestBytes,
		maxResponseBytes:    maxResponseBytes,
		maxStateBytes:       maxStateBytes,
		stateOutputHeadroom: options.StateOutputHeadroom,
	}, nil
}

func (client *Client) generationStateBytes() int {
	return continuation.GenerationStateBytes(
		client.maxStateBytes,
		client.stateOutputHeadroom,
		continuationStateEnvelopeBytes,
	)
}

func (client *Client) ShouldCompactState(request agent.Request) bool {
	if len(request.State) == 0 {
		return false
	}
	if _, _, _, err := buildRequest(request, client.maxStateBytes, client.generationStateBytes()); err == nil {
		return false
	}

	withoutState := request
	withoutState.State = nil
	_, _, _, err := buildRequest(withoutState, client.maxStateBytes, client.generationStateBytes())
	return err == nil
}

func (client *Client) Generate(ctx context.Context, request agent.Request, observer agent.StreamObserver) (agent.Response, error) {
	if err := ctx.Err(); err != nil {
		return agent.Response{}, err
	}

	wireRequest, history, newMessages, err := buildRequest(request, client.maxStateBytes, client.generationStateBytes())
	if err != nil {
		return agent.Response{}, client.errorf("build request: %v", err)
	}
	options, err := client.optionsFor(request)
	if err != nil {
		return agent.Response{}, client.errorf("%v", err)
	}
	wireRequest = configureGenerationRequest(configureCommonRequest(wireRequest, options), options)

	result, err := client.complete(ctx, wireRequest, observer, "request")
	if err != nil {
		return agent.Response{}, err
	}

	output := []json.RawMessage{result.assistant}
	state, err := encodeState(history, newMessages, output, client.maxStateBytes)
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
	wireRequest, _, _, err := buildRequestUnchecked(request, client.maxStateBytes)
	if err != nil {
		return agent.CompactResponse{}, client.errorf("build summary request: %v", err)
	}
	options, err := client.optionsFor(request)
	if err != nil {
		return agent.CompactResponse{}, client.errorf("%v", err)
	}
	wireRequest = configureCommonRequest(wireRequest, options)

	result, err := client.complete(ctx, wireRequest, agent.StreamObserver{}, "summary request")
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
		ReasoningContent: reasoningContent("", wireRequest.serializeReasoningContent),
	})
	messages := []json.RawMessage{questionRaw, summaryRaw}
	if continueAfterCompaction {
		continuation, _ := json.Marshal(message{Role: "user", Content: compaction.Continuation})
		messages = append(messages, continuation)
	}
	state, err := encodeState(nil, nil, messages, client.generationStateBytes())
	if err != nil {
		return agent.CompactResponse{}, client.errorf("encode summary state: %v", err)
	}
	return agent.CompactResponse{State: state, Usage: result.usage}, nil
}

func (client *Client) complete(ctx context.Context, wireRequest createRequest, observer agent.StreamObserver, operation string) (streamResult, error) {
	requestBody, oversized, err := backendhttp.MarshalBoundedJSON(wireRequest, client.maxRequestBytes)
	if err != nil {
		return streamResult{}, client.errorf("encode %s: %v", operation, err)
	}
	if oversized {
		return streamResult{}, client.errorf("%s exceeds %d bytes", operation, client.maxRequestBytes)
	}

	httpResponse, err := client.post(ctx, requestBody, operation)
	if err != nil {
		return streamResult{}, err
	}
	defer httpResponse.Body.Close()

	result, err := readCompletionSSE(
		httpResponse.Body,
		client.maxResponseBytes,
		observer,
		wireRequest.serializeReasoningContent,
	)
	if err == nil {
		return result, nil
	}
	var observerErr *observerDeliveryError
	if errors.As(err, &observerErr) {
		return streamResult{}, client.wrapf(err, "%v", err)
	}
	var partialErr *partialResponseError
	if errors.As(err, &partialErr) {
		return streamResult{}, client.wrapf(err, "%v", err)
	}
	classified := backendhttp.ClassifyTransportError(ctx, err)
	if classified.ReturnDirectly {
		return streamResult{}, classified.Cause
	}
	if classified.Retryable {
		return streamResult{}, client.retryableWrapf(classified.Cause, "%v", err)
	}
	return streamResult{}, client.wrapf(classified.Cause, "%v", err)
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
