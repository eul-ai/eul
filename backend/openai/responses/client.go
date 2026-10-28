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
	HTTPClient                *http.Client
	Endpoint                  string
	ErrorPrefix               string
	PrepareRequest            PrepareRequestFunc
	RequestOptions            RequestOptionsFunc
	EncodeInboxAsAgentMessage bool
}

type Client struct {
	httpClient                *http.Client
	endpoint                  string
	errorConfig               backendhttp.APIErrorConfig
	prepareRequest            PrepareRequestFunc
	requestOptions            RequestOptionsFunc
	encodeInboxAsAgentMessage bool
}

func New(options Options) (*Client, error) {
	if strings.TrimSpace(options.Endpoint) == "" {
		return nil, errors.New("responses: endpoint is required")
	}

	return &Client{
		httpClient: backendhttp.CloneNoRedirects(options.HTTPClient, backendhttp.DefaultGenerationHTTPTimeout),
		endpoint:   options.Endpoint,
		errorConfig: backendhttp.APIErrorConfig{
			Prefix:       strings.TrimSpace(options.ErrorPrefix),
			FormatDetail: formatHTTPErrorDetail,
		},
		prepareRequest:            options.PrepareRequest,
		requestOptions:            options.RequestOptions,
		encodeInboxAsAgentMessage: options.EncodeInboxAsAgentMessage,
	}, nil
}

func (client *Client) ShouldCompactState(request agent.Request) bool {
	if len(request.State) == 0 {
		return false
	}
	if _, err := client.buildGenerationWireRequest(request); err == nil {
		return false
	}

	withoutState := request
	withoutState.State = nil
	_, err := client.buildGenerationWireRequest(withoutState)
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
		return agent.Response{}, client.wrapf(err, "%v", err)
	}

	state, err := continuation.Encode(continuation.MaximumBytes, build.history, build.newItems, stream.response.Output)
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
	requestBody, oversized, err := backendhttp.MarshalBoundedJSON(wireRequest, backendhttp.MaxRequestBytes)
	if err != nil {
		return responseStreamResult{}, client.errorf("encode %s: %v", operation, err)
	}
	if oversized {
		return responseStreamResult{}, client.errorf("%s exceeds %d bytes", operation, backendhttp.MaxRequestBytes)
	}

	httpResponse, err := client.post(ctx, requestBody, operation)
	if err != nil {
		return responseStreamResult{}, err
	}
	defer httpResponse.Body.Close()

	result, err := readResponsesSSE(httpResponse.Body, backendhttp.MaxResponseBytes, observer)
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
