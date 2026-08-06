package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"yaah/agent"
)

const (
	defaultBaseURL          = "https://chatgpt.com/backend-api"
	defaultHTTPTimeout      = 10 * time.Minute
	defaultMaxRequestBytes  = int64(32 * 1024 * 1024)
	defaultMaxResponseBytes = int64(16 * 1024 * 1024)
	defaultMaxErrorBytes    = int64(64 * 1024)
	defaultMaxStateBytes    = 16 * 1024 * 1024
)

type Options struct {
	HTTPClient      *http.Client
	BaseURL         string
	ReasoningEffort string
}

type CodexCredential struct {
	AccessToken string
	AccountID   string
}

type CodexTokenSource interface {
	Token(context.Context) (CodexCredential, error)
}

type Client struct {
	httpClient       *http.Client
	endpoint         string
	tokenSource      CodexTokenSource
	maxRequestBytes  int64
	maxResponseBytes int64
	maxErrorBytes    int64
	maxStateBytes    int
	reasoningEffort  string
}

var _ agent.Provider = (*Client)(nil)

var validReasoningEfforts = map[string]struct{}{
	"":        {},
	"none":    {},
	"minimal": {},
	"low":     {},
	"medium":  {},
	"high":    {},
	"xhigh":   {},
	"max":     {},
}

func NewCodex(source CodexTokenSource, options Options) (*Client, error) {
	if err := validateReasoningEffort(options.ReasoningEffort); err != nil {
		return nil, err
	}

	baseURL := options.BaseURL
	if baseURL == "" {
		baseURL = defaultBaseURL
	}

	httpClient := options.HTTPClient
	switch {
	case httpClient == nil:
		httpClient = &http.Client{Timeout: defaultHTTPTimeout}
	case httpClient.Timeout <= 0:
		httpClient.Timeout = defaultHTTPTimeout
	}
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}

	return &Client{
		httpClient:       httpClient,
		endpoint:         strings.TrimRight(baseURL, "/") + "/codex/responses",
		tokenSource:      source,
		maxRequestBytes:  defaultMaxRequestBytes,
		maxResponseBytes: defaultMaxResponseBytes,
		maxErrorBytes:    defaultMaxErrorBytes,
		maxStateBytes:    defaultMaxStateBytes,
		reasoningEffort:  options.ReasoningEffort,
	}, nil
}

func (c *Client) Generate(ctx context.Context, request agent.Request, onText, onReasoning agent.TextSink) (agent.Response, error) {
	if err := ctx.Err(); err != nil {
		return agent.Response{}, err
	}

	credential, err := c.tokenSource.Token(ctx)
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return agent.Response{}, contextErr
		}
		return agent.Response{}, c.wrapf(err, "resolve authentication: %v", err)
	}

	wireRequest, newInputs, err := buildCreateRequest(request, c.maxStateBytes)
	if err != nil {
		return agent.Response{}, c.errorf("build request: %v", err)
	}

	if c.reasoningEffort != "" {
		wireRequest.Reasoning = &responseReasoning{Effort: c.reasoningEffort, Summary: "auto"}
	}
	wireRequest.Stream = true
	wireRequest.Text = &responseText{Verbosity: "low"}
	wireRequest.ToolChoice = "auto"
	wireRequest.ParallelToolCalls = true

	requestBody, err := json.Marshal(wireRequest)
	if err != nil {
		return agent.Response{}, c.errorf("encode request: %v", err)
	}
	if int64(len(requestBody)) > c.maxRequestBytes {
		return agent.Response{}, c.errorf("request exceeds %d bytes", c.maxRequestBytes)
	}

	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(requestBody))
	if err != nil {
		return agent.Response{}, c.errorf("create request: %v", err)
	}
	httpRequest.Header.Set("Authorization", "Bearer "+credential.AccessToken)
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Accept", "text/event-stream")
	httpRequest.Header.Set("chatgpt-account-id", credential.AccountID)
	httpRequest.Header.Set("originator", "yaah")
	httpRequest.Header.Set("User-Agent", "yaah")
	httpRequest.Header.Set("OpenAI-Beta", "responses=experimental")

	httpResponse, err := c.httpClient.Do(httpRequest)
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return agent.Response{}, contextErr
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return agent.Response{}, c.wrapf(context.DeadlineExceeded, "request failed: %v", err)
		}
		if errors.Is(err, context.Canceled) {
			return agent.Response{}, c.wrapf(context.Canceled, "request failed: %v", err)
		}
		return agent.Response{}, c.errorf("request failed: %v", err)
	}

	defer httpResponse.Body.Close()

	if httpResponse.StatusCode < 200 || httpResponse.StatusCode >= 300 {
		return agent.Response{}, c.decodeHTTPError(httpResponse)
	}

	observer := streamObserver{onText: onText, onReasoning: onReasoning}
	wireResponse, err := readResponsesSSE(httpResponse.Body, c.maxResponseBytes, &observer)
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return agent.Response{}, contextErr
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return agent.Response{}, c.wrapf(context.DeadlineExceeded, "read response: %v", err)
		}
		if errors.Is(err, context.Canceled) {
			return agent.Response{}, c.wrapf(context.Canceled, "read response: %v", err)
		}
		return agent.Response{}, c.wrapf(err, "%v", err)
	}

	text, calls, usage, err := normalizeResponse(wireResponse)
	if err != nil {
		return agent.Response{}, c.errorf("%v", err)
	}

	historyLength := len(wireRequest.Input) - len(newInputs)
	history := wireRequest.Input[:historyLength]
	state, err := encodeState(history, newInputs, wireResponse.Output, c.maxStateBytes)
	if err != nil {
		return agent.Response{}, c.errorf("%v", err)
	}

	if text != "" && onText != nil && !observer.sawDelta {
		if err := onText(text); err != nil {
			return agent.Response{}, c.wrapf(err, "deliver text: %v", err)
		}
	}

	return agent.Response{
		Text:      text,
		ToolCalls: calls,
		State:     state,
		Usage:     usage,
	}, nil
}

func (c *Client) decodeHTTPError(response *http.Response) error {
	body, truncated, err := readBounded(response.Body, c.maxErrorBytes)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return c.wrapf(context.DeadlineExceeded, "HTTP %s; read error response: %v", response.Status, err)
		}
		if errors.Is(err, context.Canceled) {
			return c.wrapf(context.Canceled, "HTTP %s; read error response: %v", response.Status, err)
		}
		return c.errorf("HTTP %s; read error response: %v", response.Status, err)
	}

	detail := strings.TrimSpace(string(body))
	if !truncated {
		var envelope struct {
			Error responseError `json:"error"`
		}
		if json.Unmarshal(body, &envelope) == nil {
			if formatted := formatResponseError(envelope.Error); formatted != "" {
				detail = formatted
			}
		}
	}
	if detail == "" {
		detail = "empty error response"
	}
	if truncated {
		detail += " [truncated]"
	}

	return c.errorf("HTTP %s: %s", response.Status, detail)
}

func (c *Client) errorf(format string, arguments ...any) error {
	return errors.New(c.errorMessage(format, arguments...))
}

func (c *Client) wrapf(cause error, format string, arguments ...any) error {
	return &wrappedError{message: c.errorMessage(format, arguments...), cause: cause}
}

func (c *Client) errorMessage(format string, arguments ...any) string {
	message := strings.ToValidUTF8(fmt.Sprintf(format, arguments...), "�")
	return truncateUTF8("openai: "+message, int(c.maxErrorBytes))
}

func validateReasoningEffort(effort string) error {
	if _, ok := validReasoningEfforts[effort]; !ok {
		return errors.New("openai: reasoning effort must be one of none, minimal, low, medium, high, xhigh, or max")
	}
	return nil
}

type wrappedError struct {
	message string
	cause   error
}

func (e *wrappedError) Error() string { return e.message }
func (e *wrappedError) Unwrap() error { return e.cause }

func readBounded(reader io.Reader, maximum int64) ([]byte, bool, error) {
	data, err := io.ReadAll(io.LimitReader(reader, maximum+1))
	if err != nil {
		return nil, false, err
	}
	if int64(len(data)) <= maximum {
		return data, false, nil
	}

	return data[:maximum], true, nil
}

func truncateUTF8(text string, maximum int) string {
	if maximum < 0 {
		maximum = 0
	}
	if len(text) <= maximum {
		return text
	}

	end := maximum
	for end > 0 && end < len(text) && !utf8.RuneStart(text[end]) {
		end--
	}
	return text[:end]
}
