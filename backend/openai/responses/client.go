package responses

import (
	"context"
	"io"
	"net/http"

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

func New(options Options) *Client {
	return &Client{
		httpClient:                backendhttp.CloneNoRedirects(options.HTTPClient, 0),
		endpoint:                  options.Endpoint,
		errorConfig:               backendhttp.APIErrorConfig{Prefix: options.ErrorPrefix},
		prepareRequest:            options.PrepareRequest,
		requestOptions:            options.RequestOptions,
		encodeInboxAsAgentMessage: options.EncodeInboxAsAgentMessage,
	}
}

func (client *Client) Generate(ctx context.Context, request agent.Request, observer agent.StreamObserver) (agent.Response, error) {
	if err := ctx.Err(); err != nil {
		return agent.Response{}, err
	}

	build, err := client.buildGenerationRequest(request)
	if err != nil {
		return agent.Response{}, client.errorf("%v", err)
	}

	stream, err := client.complete(ctx, build.wire, observer, "request")
	if err != nil {
		return agent.Response{}, err
	}

	text, calls, usage, err := normalizeResponse(stream.response)
	if err != nil {
		return agent.Response{}, client.wrapf(err, "%v", err)
	}

	state, err := continuation.Encode(build.history, build.newItems, stream.response.Output)
	if err != nil {
		return agent.Response{}, client.errorf("%v", err)
	}

	if !stream.sawTextDelta {
		if err := backendhttp.NewObserver(observer).Text(text); err != nil {
			return agent.Response{}, client.wrapf(err, "%v", err)
		}
	}

	return agent.Response{Text: text, ToolCalls: calls, State: state, Usage: usage}, nil
}

func (client *Client) complete(ctx context.Context, wireRequest createResponseRequest, observer agent.StreamObserver, operation string) (responseStreamResult, error) {
	return backendhttp.CompleteJSONSSE(
		ctx,
		backendhttp.JSONSSEConfig{
			HTTPClient:     client.httpClient,
			Endpoint:       client.endpoint,
			ErrorConfig:    client.errorConfig,
			PrepareRequest: client.prepareRequest,
		},
		wireRequest,
		operation,
		func(reader io.Reader) (responseStreamResult, error) {
			return readResponsesSSE(reader, observer)
		},
	)
}

func (client *Client) optionsFor(request agent.Request) (RequestOptions, error) {
	if client.requestOptions == nil {
		return RequestOptions{}, nil
	}
	return client.requestOptions(request)
}
