package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"yaah/agent"
)

const (
	defaultBaseURL          = "https://api.openai.com"
	defaultHTTPTimeout      = 10 * time.Minute
	defaultMaxRequestBytes  = int64(32 * 1024 * 1024)
	defaultMaxResponseBytes = int64(16 * 1024 * 1024)
	defaultMaxErrorBytes    = int64(64 * 1024)
	defaultMaxStateBytes    = 16 * 1024 * 1024
)

// Options configures a Client. BaseURL is an API origin or test server URL;
// /v1/responses is appended to it. Zero size limits use bounded defaults.
// An injected client with no positive timeout receives the default timeout.
// Redirects are rejected so the bearer credential is never forwarded.
type Options struct {
	HTTPClient       *http.Client
	BaseURL          string
	MaxRequestBytes  int64
	MaxResponseBytes int64
	MaxErrorBytes    int64
	MaxStateBytes    int
}

// Client is a non-streaming OpenAI Responses API adapter and implements
// agent.Provider.
type Client struct {
	httpClient       *http.Client
	endpoint         string
	apiKey           string
	maxRequestBytes  int64
	maxResponseBytes int64
	maxErrorBytes    int64
	maxStateBytes    int
}

var _ agent.Provider = (*Client)(nil)

// New constructs an OpenAI Responses API client.
func New(apiKey string, options Options) (*Client, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, errors.New("openai: API key is required")
	}
	if len(apiKey) > 8*1024 {
		return nil, errors.New("openai: API key is too long")
	}
	for _, character := range []byte(apiKey) {
		if character <= 0x20 || character >= 0x7f {
			return nil, errors.New("openai: API key contains invalid characters")
		}
	}

	baseURL := options.BaseURL
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return nil, errors.New("openai: invalid base URL")
	}
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("openai: base URL must be an HTTP(S) origin without credentials, query, or fragment")
	}
	if parsed.Scheme == "http" && !isLoopbackHost(parsed.Hostname()) {
		return nil, errors.New("openai: plaintext HTTP base URLs are allowed only for loopback test servers")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/v1/responses"
	parsed.RawPath = ""

	maxRequestBytes := options.MaxRequestBytes
	if maxRequestBytes == 0 {
		maxRequestBytes = defaultMaxRequestBytes
	}
	maxResponseBytes := options.MaxResponseBytes
	if maxResponseBytes == 0 {
		maxResponseBytes = defaultMaxResponseBytes
	}
	maxErrorBytes := options.MaxErrorBytes
	if maxErrorBytes == 0 {
		maxErrorBytes = defaultMaxErrorBytes
	}
	maxStateBytes := options.MaxStateBytes
	if maxStateBytes == 0 {
		maxStateBytes = defaultMaxStateBytes
	}
	maximumInt := int64(^uint(0) >> 1)
	if maxRequestBytes < 1 || maxRequestBytes >= maximumInt {
		return nil, errors.New("openai: maximum request bytes must be positive and bounded")
	}
	if maxResponseBytes < 1 || maxResponseBytes >= maximumInt {
		return nil, errors.New("openai: maximum response bytes must be positive and bounded")
	}
	if maxErrorBytes < 1 || maxErrorBytes >= maximumInt {
		return nil, errors.New("openai: maximum error bytes must be positive and bounded")
	}
	if maxStateBytes < 1 {
		return nil, errors.New("openai: maximum state bytes must be positive")
	}

	httpClient := options.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultHTTPTimeout}
	}
	httpClientCopy := *httpClient
	if httpClientCopy.Timeout <= 0 {
		httpClientCopy.Timeout = defaultHTTPTimeout
	}
	httpClientCopy.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &Client{
		httpClient:       &httpClientCopy,
		endpoint:         parsed.String(),
		apiKey:           apiKey,
		maxRequestBytes:  maxRequestBytes,
		maxResponseBytes: maxResponseBytes,
		maxErrorBytes:    maxErrorBytes,
		maxStateBytes:    maxStateBytes,
	}, nil
}

func isLoopbackHost(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if host == "localhost" {
		return true
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}

// Generate makes one non-streaming Responses API request.
func (c *Client) Generate(ctx context.Context, request agent.Request, onText agent.TextSink) (agent.Response, error) {
	if c == nil {
		return agent.Response{}, errors.New("openai: client is nil")
	}
	if ctx == nil {
		return agent.Response{}, errors.New("openai: context is required")
	}
	if err := ctx.Err(); err != nil {
		return agent.Response{}, err
	}
	if requestPayloadExceeds(request, c.maxRequestBytes) {
		return agent.Response{}, c.errorf("request exceeds %d bytes", c.maxRequestBytes)
	}

	wireRequest, newInputs, err := buildCreateRequest(request, c.maxStateBytes)
	if err != nil {
		return agent.Response{}, c.errorf("build request: %v", err)
	}
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
	httpRequest.Header.Set("Authorization", "Bearer "+c.apiKey)
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Accept", "application/json")

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
	body, truncated, err := readBounded(httpResponse.Body, c.maxResponseBytes)
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
		return agent.Response{}, c.errorf("read response: %v", err)
	}
	if truncated {
		return agent.Response{}, c.errorf("response exceeds %d bytes", c.maxResponseBytes)
	}
	wireResponse, err := decodeCreateResponse(body)
	if err != nil {
		return agent.Response{}, c.errorf("%v", err)
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
	if text != "" && onText != nil {
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
		detail = redactTruncatedSecretSuffix(detail, c.apiKey)
		detail += " [truncated]"
	}
	return c.errorf("HTTP %s: %s", response.Status, detail)
}

func redactTruncatedSecretSuffix(text, secret string) string {
	text = strings.ReplaceAll(text, secret, "[REDACTED]")
	maximum := min(len(text), len(secret)-1)
	for length := maximum; length > 0; length-- {
		if strings.HasSuffix(text, secret[:length]) {
			return text[:len(text)-length] + "[REDACTED]"
		}
	}
	return text
}

func (c *Client) errorf(format string, arguments ...any) error {
	return errors.New(c.errorMessage(format, arguments...))
}

func (c *Client) wrapf(cause error, format string, arguments ...any) error {
	return &wrappedError{message: c.errorMessage(format, arguments...), cause: cause}
}

func (c *Client) errorMessage(format string, arguments ...any) string {
	message := fmt.Sprintf(format, arguments...)
	message = strings.ReplaceAll(message, c.apiKey, "[REDACTED]")
	message = strings.ToValidUTF8(message, "�")
	return truncateUTF8("openai: "+message, int(c.maxErrorBytes))
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
