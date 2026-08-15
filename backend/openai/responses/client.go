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
	Redact              []string
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
	redact              []string
}

func New(options Options) (*Client, error) {
	if strings.TrimSpace(options.Endpoint) == "" {
		return nil, errors.New("responses: endpoint is required")
	}

	httpClient := backendhttp.New(options.HTTPClient, defaultHTTPTimeout)

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
		redact:              append([]string(nil), options.Redact...),
	}, nil
}

func (c *Client) stateOutputBudget() int {
	budget := c.stateOutputHeadroom
	if budget <= 0 {
		budget = min(defaultStateOutputHeadroom, c.maxStateBytes/4)
	}
	return min(budget, max(0, c.maxStateBytes-continuationStateEnvelopeBytes))
}

func (c *Client) generationStateBytes() int {
	return max(0, c.maxStateBytes-c.stateOutputBudget())
}

func (c *Client) ShouldCompactState(request agent.Request) bool {
	if len(request.State) == 0 {
		return false
	}
	if _, _, err := buildCreateRequestWithLimit(request, c.maxStateBytes, c.generationStateBytes()); err == nil {
		return false
	}

	withoutState := request
	withoutState.State = nil
	_, _, err := buildCreateRequestWithLimit(withoutState, c.maxStateBytes, c.generationStateBytes())
	return err == nil
}

func (c *Client) Generate(ctx context.Context, request agent.Request, observer agent.StreamObserver) (agent.Response, error) {
	if err := ctx.Err(); err != nil {
		return agent.Response{}, err
	}

	wireRequest, newInputs, err := buildCreateRequestWithLimit(request, c.maxStateBytes, c.generationStateBytes())
	if err != nil {
		return agent.Response{}, c.errorf("build request: %v", err)
	}
	if err := c.configureRequest(request, &wireRequest); err != nil {
		return agent.Response{}, c.errorf("%v", err)
	}

	requestBody, oversized, err := marshalBoundedJSON(wireRequest, c.maxRequestBytes)
	if err != nil {
		return agent.Response{}, c.errorf("encode request: %v", err)
	}
	if oversized {
		return agent.Response{}, c.errorf("request exceeds %d bytes", c.maxRequestBytes)
	}

	httpResponse, err := c.post(ctx, requestBody, "request")
	if err != nil {
		return agent.Response{}, err
	}
	defer httpResponse.Body.Close()

	stream := streamObserver{observer: observer}
	wireResponse, err := readResponsesSSE(httpResponse.Body, c.maxResponseBytes, &stream)
	if err != nil {
		c.redactResponseFailure(err)
		var observerErr *observerDeliveryError
		if errors.As(err, &observerErr) {
			return agent.Response{}, c.wrapf(err, "%v", err)
		}
		if classified := c.contextError(ctx, err, "read response"); classified != nil {
			return agent.Response{}, classified
		}
		if isRetryableNetworkError(err) {
			return agent.Response{}, c.retryableWrapf(err, "%v", err)
		}
		return agent.Response{}, c.wrapf(err, "%v", err)
	}

	text, calls, usage, err := normalizeResponse(wireResponse)
	if err != nil {
		c.redactResponseFailure(err)
		return agent.Response{}, c.wrapf(err, "%v", err)
	}

	historyLength := len(wireRequest.Input) - len(newInputs)
	history := wireRequest.Input[:historyLength]
	outputStateBytes, err := encodedStateSize(wireResponse.Output)
	if err != nil {
		return agent.Response{}, c.errorf("%v", err)
	}
	inputStateBytes, err := encodedStateSize(history, newInputs)
	if err != nil {
		return agent.Response{}, c.errorf("%v", err)
	}
	if outputStateBytes-continuationStateEnvelopeBytes > c.maxStateBytes-inputStateBytes {
		return agent.Response{}, c.errorf("response output cannot fit continuation state")
	}
	state, err := encodeState(history, newInputs, wireResponse.Output, c.maxStateBytes)
	if err != nil {
		return agent.Response{}, c.errorf("%v", err)
	}

	if text != "" && observer.Text != nil && !stream.sawDelta {
		if err := observer.Text(text); err != nil {
			deliveryErr := &observerDeliveryError{operation: "deliver text", cause: err}
			return agent.Response{}, c.wrapf(deliveryErr, "%v", deliveryErr)
		}
	}

	return agent.Response{Text: text, ToolCalls: calls, State: state, Usage: usage}, nil
}

func (c *Client) Compact(ctx context.Context, request agent.Request) (agent.CompactResponse, error) {
	if err := ctx.Err(); err != nil {
		return agent.CompactResponse{}, err
	}

	wireRequest, err := buildCompactRequest(request, c.maxStateBytes)
	if err != nil {
		return agent.CompactResponse{}, c.errorf("build compact request: %v", err)
	}
	if err := c.configureRequest(request, &wireRequest); err != nil {
		return agent.CompactResponse{}, c.errorf("%v", err)
	}

	requestBody, oversized, err := marshalBoundedJSON(wireRequest, c.maxRequestBytes)
	if err != nil {
		return agent.CompactResponse{}, c.errorf("encode compact request: %v", err)
	}
	if oversized {
		return agent.CompactResponse{}, c.errorf("compact request exceeds %d bytes", c.maxRequestBytes)
	}

	httpResponse, err := c.post(ctx, requestBody, "compact request")
	if err != nil {
		return agent.CompactResponse{}, err
	}
	defer httpResponse.Body.Close()

	wireResponse, err := readResponsesSSE(httpResponse.Body, c.maxResponseBytes, nil)
	if err != nil {
		if classified := c.contextError(ctx, err, "read compact response"); classified != nil {
			return agent.CompactResponse{}, classified
		}
		return agent.CompactResponse{}, c.errorf("read compact response: %v", err)
	}
	if err := validateCompletedResponse(wireResponse); err != nil {
		c.redactResponseFailure(err)
		return agent.CompactResponse{}, c.errorf("%v", err)
	}
	usage, err := normalizeUsage(wireResponse.Usage)
	if err != nil {
		return agent.CompactResponse{}, c.errorf("%v", err)
	}
	if err := validateCompactOutput(wireResponse.Output); err != nil {
		return agent.CompactResponse{}, c.errorf("%v", err)
	}
	input := wireRequest.Input[:len(wireRequest.Input)-1]
	state, err := encodeState(nil, nil, compactedStateItems(input, wireResponse.Output), c.generationStateBytes())
	if err != nil {
		return agent.CompactResponse{}, c.errorf("%v", err)
	}

	return agent.CompactResponse{State: state, Usage: usage}, nil
}

func (c *Client) SemanticCompact(ctx context.Context, request agent.Request, instructions string) (agent.CompactResponse, error) {
	if err := ctx.Err(); err != nil {
		return agent.CompactResponse{}, err
	}

	request, continueAfterCompaction := compaction.Prepare(request, instructions)
	wireRequest, _, err := buildCreateRequestUnchecked(request, c.maxStateBytes)
	if err != nil {
		return agent.CompactResponse{}, c.errorf("build summary request: %v", err)
	}
	if err := c.configureRequest(request, &wireRequest); err != nil {
		return agent.CompactResponse{}, c.errorf("%v", err)
	}
	wireRequest.ToolChoice = ""
	wireRequest.ParallelToolCalls = false

	requestBody, oversized, err := marshalBoundedJSON(wireRequest, c.maxRequestBytes)
	if err != nil {
		return agent.CompactResponse{}, c.errorf("encode summary request: %v", err)
	}
	if oversized {
		return agent.CompactResponse{}, c.errorf("summary request exceeds %d bytes", c.maxRequestBytes)
	}

	httpResponse, err := c.post(ctx, requestBody, "summary request")
	if err != nil {
		return agent.CompactResponse{}, err
	}
	defer httpResponse.Body.Close()

	wireResponse, err := readResponsesSSE(httpResponse.Body, c.maxResponseBytes, nil)
	if err != nil {
		if classified := c.contextError(ctx, err, "read summary response"); classified != nil {
			return agent.CompactResponse{}, classified
		}
		return agent.CompactResponse{}, c.errorf("read summary response: %v", err)
	}

	summary, calls, usage, err := normalizeResponse(wireResponse)
	if err != nil {
		c.redactResponseFailure(err)
		return agent.CompactResponse{}, c.errorf("%v", err)
	}
	summary, err = compaction.ValidateSummary(summary, len(calls))
	if err != nil {
		return agent.CompactResponse{}, c.errorf("%v", err)
	}

	items, err := semanticCompactionStateItems(summary, continueAfterCompaction)
	if err != nil {
		return agent.CompactResponse{}, c.errorf("encode summary state: %v", err)
	}
	state, err := encodeState(nil, nil, items, c.generationStateBytes())
	if err != nil {
		return agent.CompactResponse{}, c.errorf("encode summary state: %v", err)
	}

	return agent.CompactResponse{State: state, Usage: usage}, nil
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

func (c *Client) configureRequest(request agent.Request, wireRequest *createResponseRequest) error {
	options := RequestOptions{}
	if c.requestOptions != nil {
		var err error
		options, err = c.requestOptions(request)
		if err != nil {
			return err
		}
	}

	wireRequest.ServiceTier = options.ServiceTier
	wireRequest.Stream = true
	wireRequest.Reasoning = options.Reasoning
	wireRequest.Include = append([]string(nil), options.Include...)
	if options.TextVerbosity != "" {
		wireRequest.Text = &responseText{Verbosity: options.TextVerbosity}
	}
	wireRequest.ToolChoice = options.ToolChoice
	wireRequest.ParallelToolCalls = options.ParallelToolCalls
	return nil
}

func marshalBoundedJSON(value any, maximum int64) ([]byte, bool, error) {
	return backendhttp.MarshalBoundedJSON(value, maximum)
}
