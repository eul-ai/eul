package responses

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/backend/continuation"
	backendhttp "github.com/eul-ai/eul/backend/httpclient"
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
		return nil, errors.New("responses: endpoint is required")
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
			Prefix:       strings.TrimSpace(options.ErrorPrefix),
			Maximum:      limits.ErrorBytes,
			FormatDetail: formatHTTPErrorDetail,
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
	if _, err := buildGenerationWireRequest(request, client.maxStateBytes, client.generationStateBytes()); err == nil {
		return false
	}

	withoutState := request
	withoutState.State = nil
	_, err := buildGenerationWireRequest(withoutState, client.maxStateBytes, client.generationStateBytes())
	return err == nil
}

func (client *Client) Generate(ctx context.Context, request agent.Request, observer agent.StreamObserver) (agent.Response, error) {
	if err := ctx.Err(); err != nil {
		return agent.Response{}, err
	}

	build, err := client.buildGenerationRequest(request)
	if err != nil {
		return agent.Response{}, client.errorf("%v", err)
	}

	stream, err := client.complete(ctx, build.wire, observer, "request", client.generationReadError)
	if err != nil {
		return agent.Response{}, err
	}

	text, calls, usage, err := normalizeResponse(stream.response)
	if err != nil {
		if stream.observed {
			err = backendhttp.NewPartialResponseError(err)
		}
		return agent.Response{}, client.wrapf(err, "%v", err)
	}

	state, err := continuation.Encode(client.maxStateBytes, build.history, build.newItems, stream.response.Output)
	if err != nil {
		return agent.Response{}, client.errorf("%v", err)
	}

	if text != "" && observer.Text != nil && !stream.sawTextDelta {
		if err := observer.Text(text); err != nil {
			deliveryErr := backendhttp.NewObserverError("deliver text", err)
			return agent.Response{}, client.wrapf(deliveryErr, "%v", deliveryErr)
		}
	}

	return agent.Response{Text: text, ToolCalls: calls, State: state, Usage: usage}, nil
}

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

func (client *Client) optionsFor(request agent.Request) (RequestOptions, error) {
	if client.requestOptions == nil {
		return RequestOptions{}, nil
	}
	return client.requestOptions(request)
}
