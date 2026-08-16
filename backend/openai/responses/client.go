package responses

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/backend/compaction"
	backendhttp "github.com/eul-ai/eul/backend/httpclient"
)

const (
	defaultHTTPTimeout         = 10 * time.Minute
	defaultMaxRequestBytes     = int64(32 * 1024 * 1024)
	defaultMaxResponseBytes    = int64(16 * 1024 * 1024)
	defaultMaxErrorBytes       = int64(64 * 1024)
	defaultMaxStateBytes       = 16 * 1024 * 1024
	defaultStateOutputHeadroom = 1024 * 1024
)

type RequestOptions struct {
	SessionID         string
	Reasoning         *Reasoning
	ServiceTier       string
	TextVerbosity     string
	Include           []string
	ToolChoice        string
	ParallelToolCalls bool
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
	errorPrefix         string
	prepareRequest      PrepareRequestFunc
	requestOptions      RequestOptionsFunc
	maxRequestBytes     int64
	maxResponseBytes    int64
	maxErrorBytes       int64
	maxStateBytes       int
	stateOutputHeadroom int
}

func New(options Options) (*Client, error) {
	if strings.TrimSpace(options.Endpoint) == "" {
		return nil, errors.New("responses: endpoint is required")
	}

	httpClient := backendhttp.CloneNoRedirects(options.HTTPClient, defaultHTTPTimeout)

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
	stateOutputHeadroom := options.StateOutputHeadroom
	if stateOutputHeadroom <= 0 {
		stateOutputHeadroom = defaultStateOutputHeadroom
	}

	return &Client{
		httpClient:          httpClient,
		endpoint:            options.Endpoint,
		errorPrefix:         strings.TrimSpace(options.ErrorPrefix),
		prepareRequest:      options.PrepareRequest,
		requestOptions:      options.RequestOptions,
		maxRequestBytes:     maxRequestBytes,
		maxResponseBytes:    maxResponseBytes,
		maxErrorBytes:       maxErrorBytes,
		maxStateBytes:       maxStateBytes,
		stateOutputHeadroom: stateOutputHeadroom,
	}, nil
}

func (client *Client) stateOutputBudget() int {
	budget := client.stateOutputHeadroom
	if budget <= 0 {
		budget = min(defaultStateOutputHeadroom, client.maxStateBytes/4)
	}
	return min(budget, max(0, client.maxStateBytes-continuationStateEnvelopeBytes))
}

func (client *Client) generationStateBytes() int {
	return max(0, client.maxStateBytes-client.stateOutputBudget())
}

func (client *Client) ShouldCompactState(request agent.Request) bool {
	if len(request.State) == 0 {
		return false
	}
	if _, err := buildRequest(request, client.maxStateBytes, client.generationStateBytes()); err == nil {
		return false
	}

	withoutState := request
	withoutState.State = nil
	_, err := buildRequest(withoutState, client.maxStateBytes, client.generationStateBytes())
	return err == nil
}

func (client *Client) Generate(ctx context.Context, request agent.Request, observer agent.StreamObserver) (agent.Response, error) {
	if err := ctx.Err(); err != nil {
		return agent.Response{}, err
	}

	build, err := buildRequest(request, client.maxStateBytes, client.generationStateBytes())
	if err != nil {
		return agent.Response{}, client.errorf("build request: %v", err)
	}
	options, err := client.optionsFor(request)
	if err != nil {
		return agent.Response{}, client.errorf("%v", err)
	}
	wireRequest := configureGenerationRequest(configureCommonRequest(build.wire, options), options)

	stream, err := client.complete(ctx, wireRequest, observer, "request", client.generationReadError)
	if err != nil {
		return agent.Response{}, err
	}

	text, calls, usage, err := normalizeResponse(stream.response)
	if err != nil {
		if stream.observed {
			err = &partialResponseError{cause: err}
		}
		return agent.Response{}, client.wrapf(err, "%v", err)
	}

	state, err := encodeState(build.history, build.newItems, stream.response.Output, client.maxStateBytes)
	if err != nil {
		return agent.Response{}, client.errorf("%v", err)
	}

	if text != "" && observer.Text != nil && !stream.sawTextDelta {
		if err := observer.Text(text); err != nil {
			deliveryErr := &observerDeliveryError{operation: "deliver text", cause: err}
			return agent.Response{}, client.wrapf(deliveryErr, "%v", deliveryErr)
		}
	}

	return agent.Response{Text: text, ToolCalls: calls, State: state, Usage: usage}, nil
}

func (client *Client) Compact(ctx context.Context, request agent.Request) (agent.CompactResponse, error) {
	if err := ctx.Err(); err != nil {
		return agent.CompactResponse{}, err
	}

	build, err := buildCompactRequest(request, client.maxStateBytes)
	if err != nil {
		return agent.CompactResponse{}, client.errorf("build compact request: %v", err)
	}
	options, err := client.optionsFor(request)
	if err != nil {
		return agent.CompactResponse{}, client.errorf("%v", err)
	}
	wireRequest := configureGenerationRequest(configureCommonRequest(build.wire, options), options)

	stream, err := client.complete(ctx, wireRequest, agent.StreamObserver{}, "compact request", client.compactionReadError)
	if err != nil {
		return agent.CompactResponse{}, err
	}
	if err := validateCompletedResponse(stream.response); err != nil {
		return agent.CompactResponse{}, client.errorf("%v", err)
	}
	usage, err := normalizeUsage(stream.response.Usage)
	if err != nil {
		return agent.CompactResponse{}, client.errorf("%v", err)
	}
	if err := validateCompactOutput(stream.response.Output); err != nil {
		return agent.CompactResponse{}, client.errorf("%v", err)
	}
	state, err := encodeState(nil, nil, compactedStateItems(build.input, stream.response.Output), client.generationStateBytes())
	if err != nil {
		return agent.CompactResponse{}, client.errorf("%v", err)
	}

	return agent.CompactResponse{State: state, Usage: usage}, nil
}

func (client *Client) SemanticCompact(ctx context.Context, request agent.Request, instructions string) (agent.CompactResponse, error) {
	if err := ctx.Err(); err != nil {
		return agent.CompactResponse{}, err
	}

	request, continueAfterCompaction := compaction.Prepare(request, instructions)
	build, err := buildRequestUnchecked(request, client.maxStateBytes)
	if err != nil {
		return agent.CompactResponse{}, client.errorf("build summary request: %v", err)
	}
	options, err := client.optionsFor(request)
	if err != nil {
		return agent.CompactResponse{}, client.errorf("%v", err)
	}
	wireRequest := configureCommonRequest(build.wire, options)

	stream, err := client.complete(ctx, wireRequest, agent.StreamObserver{}, "summary request", client.compactionReadError)
	if err != nil {
		return agent.CompactResponse{}, err
	}

	summary, calls, usage, err := normalizeResponse(stream.response)
	if err != nil {
		return agent.CompactResponse{}, client.errorf("%v", err)
	}
	summary, err = compaction.ValidateSummary(summary, len(calls))
	if err != nil {
		return agent.CompactResponse{}, client.errorf("%v", err)
	}

	items, err := semanticCompactionStateItems(summary, continueAfterCompaction)
	if err != nil {
		return agent.CompactResponse{}, client.errorf("encode summary state: %v", err)
	}
	state, err := encodeState(nil, nil, items, client.generationStateBytes())
	if err != nil {
		return agent.CompactResponse{}, client.errorf("encode summary state: %v", err)
	}

	return agent.CompactResponse{State: state, Usage: usage}, nil
}

type responseReadErrorFunc func(context.Context, error, string) error

func (client *Client) complete(
	ctx context.Context,
	wireRequest createResponseRequest,
	observer agent.StreamObserver,
	operation string,
	readError responseReadErrorFunc,
) (responseStreamResult, error) {
	requestBody, oversized, err := backendhttp.MarshalBoundedJSON(wireRequest, client.maxRequestBytes)
	if err != nil {
		return responseStreamResult{}, client.errorf("encode %s: %v", operation, err)
	}
	if oversized {
		return responseStreamResult{}, client.errorf("%s exceeds %d bytes", operation, client.maxRequestBytes)
	}

	httpResponse, err := client.post(ctx, requestBody, operation)
	if err != nil {
		return responseStreamResult{}, err
	}
	defer httpResponse.Body.Close()

	result, err := readResponsesSSE(httpResponse.Body, client.maxResponseBytes, observer)
	if err != nil {
		return responseStreamResult{}, readError(ctx, err, operation)
	}
	return result, nil
}

func (client *Client) generationReadError(ctx context.Context, err error, _ string) error {
	var observerErr *observerDeliveryError
	if errors.As(err, &observerErr) {
		return client.wrapf(err, "%v", err)
	}
	var partialErr *partialResponseError
	if errors.As(err, &partialErr) {
		return client.wrapf(err, "%v", err)
	}

	classified := backendhttp.ClassifyTransportError(ctx, err)
	if classified.ReturnDirectly {
		return classified.Cause
	}
	if classified.Retryable {
		return client.retryableWrapf(classified.Cause, "%v", err)
	}
	return client.wrapf(classified.Cause, "%v", err)
}

func (client *Client) compactionReadError(ctx context.Context, err error, operation string) error {
	classified := backendhttp.ClassifyTransportError(ctx, err)
	if classified.ReturnDirectly {
		return classified.Cause
	}
	switch {
	case errors.Is(classified.Cause, context.DeadlineExceeded):
		return client.retryableWrapf(context.DeadlineExceeded, "read %s: %v", operation, err)
	case errors.Is(classified.Cause, context.Canceled):
		return client.wrapf(context.Canceled, "read %s: %v", operation, err)
	default:
		return client.errorf("read %s: %v", operation, err)
	}
}

func semanticCompactionStateItems(summary string, continueTask bool) ([]json.RawMessage, error) {
	summaryItem, _ := json.Marshal(inputMessage{
		Role:    "assistant",
		Content: compaction.FormatSummary(summary),
	})
	items := []json.RawMessage{summaryItem}
	if !continueTask {
		return items, nil
	}

	continuation, err := encodeInputs([]agent.Input{agent.NewTextInput(compaction.Continuation)})
	if err != nil {
		return nil, err
	}
	return append(items, continuation...), nil
}

func (client *Client) optionsFor(request agent.Request) (RequestOptions, error) {
	if client.requestOptions == nil {
		return RequestOptions{}, nil
	}
	return client.requestOptions(request)
}

func configureCommonRequest(wireRequest createResponseRequest, options RequestOptions) createResponseRequest {
	wireRequest.SessionID = options.SessionID
	wireRequest.ServiceTier = options.ServiceTier
	wireRequest.Stream = true
	wireRequest.Reasoning = options.Reasoning
	wireRequest.Include = append([]string(nil), options.Include...)
	if options.TextVerbosity != "" {
		wireRequest.Text = &responseText{Verbosity: options.TextVerbosity}
	}
	return wireRequest
}

func configureGenerationRequest(wireRequest createResponseRequest, options RequestOptions) createResponseRequest {
	wireRequest.ToolChoice = options.ToolChoice
	wireRequest.ParallelToolCalls = options.ParallelToolCalls
	return wireRequest
}
