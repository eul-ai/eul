package protocol

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"time"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/backend/httpclient"
)

type Codec interface {
	Build(request agent.Request, history []json.RawMessage) (Wire, []json.RawMessage, error)
}

// Wire is a request body that also decodes its streamed response and encodes
// summary state in the same wire format.
type Wire interface {
	Read(body io.Reader, observer agent.StreamObserver) (Result, error)
	SummaryItems(summary string, continueTask bool) []json.RawMessage
}

type Result struct {
	Text      string
	ToolCalls []agent.ToolCall
	Output    []json.RawMessage
	Usage     agent.Usage
}

type Options struct {
	Codec          Codec
	HTTPClient     *http.Client
	Endpoint       string
	ErrorPrefix    string
	PrepareRequest func(context.Context, *http.Request) error
	ContextWindow  func(model string) int64
}

type Client struct {
	codec         Codec
	contextWindow func(string) int64
	config        httpclient.JSONSSEConfig
}

func New(options Options) *Client {
	return &Client{
		codec:         options.Codec,
		contextWindow: options.ContextWindow,
		config: httpclient.JSONSSEConfig{
			HTTPClient:     httpclient.CloneNoRedirects(options.HTTPClient, 0),
			Endpoint:       options.Endpoint,
			ErrorConfig:    httpclient.APIErrorConfig{Prefix: options.ErrorPrefix},
			PrepareRequest: options.PrepareRequest,
		},
	}
}

func (client *Client) Generate(ctx context.Context, request agent.Request, observer agent.StreamObserver) (agent.Response, error) {
	if err := ctx.Err(); err != nil {
		return agent.Response{}, err
	}

	wire, input, err := client.build(request)
	if err != nil {
		return agent.Response{}, err
	}

	result, err := client.Complete(ctx, wire, observer, "request")
	if err != nil {
		return agent.Response{}, err
	}

	state, err := EncodeState(input, result.Output)
	if err != nil {
		return agent.Response{}, client.errorf("%v", err)
	}
	return agent.Response{Text: result.Text, ToolCalls: result.ToolCalls, State: state, Usage: result.Usage}, nil
}

func (client *Client) Compact(ctx context.Context, request agent.Request) (agent.CompactResponse, error) {
	if err := ctx.Err(); err != nil {
		return agent.CompactResponse{}, err
	}

	request, continueTask := prepareSummary(request)
	wire, _, err := client.build(request)
	if err != nil {
		return agent.CompactResponse{}, err
	}

	result, err := client.Complete(ctx, wire, agent.StreamObserver{}, "summary request")
	if err != nil {
		return agent.CompactResponse{}, err
	}
	summary, err := validateSummary(result.Text, len(result.ToolCalls))
	if err != nil {
		return agent.CompactResponse{}, client.errorf("%v", err)
	}

	state, err := EncodeState(wire.SummaryItems(formatSummary(summary), continueTask))
	if err != nil {
		return agent.CompactResponse{}, client.errorf("encode summary state: %v", err)
	}
	return agent.CompactResponse{State: state, Usage: result.Usage}, nil
}

func (client *Client) build(request agent.Request) (Wire, []json.RawMessage, error) {
	history, err := DecodeState(request.State)
	if err != nil {
		return nil, nil, client.errorf("%v", err)
	}
	wire, newItems, err := client.codec.Build(request, history)
	if err != nil {
		return nil, nil, client.errorf("build request: %v", err)
	}
	return wire, slices.Concat(history, newItems), nil
}

func (client *Client) Complete(ctx context.Context, wire Wire, observer agent.StreamObserver, operation string) (Result, error) {
	return httpclient.CompleteJSONSSE(ctx, client.config, wire, operation, func(body io.Reader) (Result, error) {
		return wire.Read(body, observer)
	})
}

func (client *Client) ShouldCompact(request agent.Request, usage agent.Usage) bool {
	return shouldCompact(request, usage, client.contextWindow(request.Model))
}

func (*Client) ShouldCompactAfterError(_ agent.Request, err error) bool {
	return httpclient.IsContextLimit(err)
}

func (*Client) RetryGeneration(err error, failedAttempts int) (time.Duration, bool) {
	return httpclient.RetryGeneration(err, failedAttempts)
}

func (client *Client) errorf(format string, arguments ...any) error {
	return client.config.ErrorConfig.Errorf(format, arguments...)
}
