package messages

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
	semanticCompactionQuestion = "What happened earlier in this conversation?"
)

type RequestOptions struct {
	MaxTokens    int
	Thinking     *Thinking
	OutputConfig *OutputConfig
	ToolChoice   *ToolChoice
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
		return nil, errors.New("anthropic messages: endpoint is required")
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
	stateOutputHeadroom := options.StateOutputHeadroom
	if stateOutputHeadroom <= 0 {
		stateOutputHeadroom = defaultStateOutputHeadroom
	}

	return &Client{
		httpClient:          backendhttp.New(options.HTTPClient, defaultHTTPTimeout),
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
	if err := client.configureRequest(request, &wireRequest); err != nil {
		return agent.Response{}, client.errorf("%v", err)
	}

	result, err := client.complete(ctx, wireRequest, observer, "request")
	if err != nil {
		return agent.Response{}, err
	}

	output := []json.RawMessage{result.assistant}
	outputStateBytes, err := encodedStateSize(output)
	if err != nil {
		return agent.Response{}, client.errorf("%v", err)
	}
	inputStateBytes, err := encodedStateSize(history, newMessages)
	if err != nil {
		return agent.Response{}, client.errorf("%v", err)
	}
	if outputStateBytes-continuationStateEnvelopeBytes > client.maxStateBytes-inputStateBytes {
		return agent.Response{}, client.errorf("response output cannot fit continuation state")
	}
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
	if err := client.configureRequest(request, &wireRequest); err != nil {
		return agent.CompactResponse{}, client.errorf("%v", err)
	}
	wireRequest.Tools = nil
	wireRequest.ToolChoice = nil

	result, err := client.complete(ctx, wireRequest, agent.StreamObserver{}, "summary request")
	if err != nil {
		return agent.CompactResponse{}, err
	}
	summary, err := compaction.ValidateSummary(result.text, len(result.calls))
	if err != nil {
		return agent.CompactResponse{}, client.errorf("%v", err)
	}

	questionContent, _ := json.Marshal([]contentBlock{{Type: "text", Text: semanticCompactionQuestion}})
	question, _ := json.Marshal(wireMessage{Role: "user", Content: questionContent})
	summaryContent, _ := json.Marshal([]contentBlock{{
		Type: "text",
		Text: compaction.FormatSummary(summary),
	}})
	summaryMessage, _ := json.Marshal(wireMessage{Role: "assistant", Content: summaryContent})
	messages := []json.RawMessage{question, summaryMessage}
	if continueAfterCompaction {
		continuationContent, _ := json.Marshal([]contentBlock{{Type: "text", Text: compaction.Continuation}})
		continuation, _ := json.Marshal(wireMessage{Role: "user", Content: continuationContent})
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

	result, err := readMessagesSSE(httpResponse.Body, client.maxResponseBytes, observer)
	if err == nil {
		return result, nil
	}
	client.redactResponseFailure(err)
	var observerErr *observerDeliveryError
	if errors.As(err, &observerErr) {
		return streamResult{}, client.wrapf(err, "%v", err)
	}
	if classified := client.contextError(ctx, err, "read "+operation); classified != nil {
		return streamResult{}, classified
	}
	if backendhttp.RetryableNetworkError(err) {
		return streamResult{}, client.retryableWrapf(err, "%v", err)
	}
	return streamResult{}, client.wrapf(err, "%v", err)
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
		if options.Thinking.BudgetTokens <= 0 {
			return errors.New("thinking budget must be positive")
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
	if options.OutputConfig != nil {
		output := *options.OutputConfig
		wireRequest.OutputConfig = &output
	}
	if len(wireRequest.Tools) != 0 && options.ToolChoice != nil {
		choice := *options.ToolChoice
		wireRequest.ToolChoice = &choice
	}
	return nil
}
