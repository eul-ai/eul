package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/eul-ai/eul/backend"
	backendhttp "github.com/eul-ai/eul/backend/httpclient"
)

type usageResponse struct {
	RateLimit *usageRateLimit `json:"rate_limit"`
}

type usageRateLimit struct {
	PrimaryWindow   *providerUsageWindow `json:"primary_window"`
	SecondaryWindow *providerUsageWindow `json:"secondary_window"`
}

type providerUsageWindow struct {
	UsedPercent        int   `json:"used_percent"`
	LimitWindowSeconds int64 `json:"limit_window_seconds"`
	ResetAt            int64 `json:"reset_at"`
}

func (c *Client) Usage(ctx context.Context) (backend.AccountUsage, error) {
	if err := ctx.Err(); err != nil {
		return backend.AccountUsage{}, err
	}

	credential, err := c.resolveCredential(ctx)
	if err != nil {
		return backend.AccountUsage{}, err
	}
	response, err := c.getUsage(ctx, credential)
	if err != nil {
		return backend.AccountUsage{}, err
	}
	defer response.Body.Close()

	body, truncated, err := backendhttp.ReadBounded(response.Body, c.maxUsageResponseBytes)
	if err != nil {
		if classified := c.contextError(ctx, err, "read usage response"); classified != nil {
			return backend.AccountUsage{}, classified
		}
		return backend.AccountUsage{}, c.errorf("read usage response: %v", err)
	}
	if truncated {
		return backend.AccountUsage{}, c.errorf("usage response exceeds %d bytes", c.maxUsageResponseBytes)
	}

	var wire usageResponse
	if err := json.Unmarshal(body, &wire); err != nil {
		return backend.AccountUsage{}, c.errorf("decode usage response: %v", err)
	}
	return normalizeProviderUsage(wire)
}

func (c *Client) getUsage(ctx context.Context, credential Credential) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.usageEndpoint, nil)
	if err != nil {
		return nil, c.errorf("create usage request: %v", err)
	}
	setCredentialHeaders(request, credential)
	request.Header.Set("Accept", "application/json")

	response, err := c.httpClient.Do(request)
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return nil, contextErr
		}
		return nil, c.wrapf(err, "usage request failed: %v", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		defer response.Body.Close()
		body, _, readErr := backendhttp.ReadBounded(response.Body, c.maxErrorBytes)
		if readErr != nil {
			return nil, c.wrapf(readErr, "HTTP %s; read error response: %v", response.Status, readErr)
		}
		return nil, c.errorf("HTTP %s: %s", response.Status, strings.TrimSpace(string(body)))
	}
	return response, nil
}

func (c *Client) contextError(ctx context.Context, err error, operation string) error {
	if contextErr := ctx.Err(); contextErr != nil {
		return contextErr
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return c.wrapf(context.DeadlineExceeded, "%s: %v", operation, err)
	}
	if errors.Is(err, context.Canceled) {
		return c.wrapf(context.Canceled, "%s: %v", operation, err)
	}
	return nil
}

func (c *Client) errorf(format string, arguments ...any) error {
	return errors.New(c.errorMessage(format, arguments...))
}

func normalizeProviderUsage(response usageResponse) (backend.AccountUsage, error) {
	if response.RateLimit == nil {
		return backend.AccountUsage{}, nil
	}

	windows := make([]backend.UsageWindow, 0, 2)
	for _, window := range []*providerUsageWindow{response.RateLimit.PrimaryWindow, response.RateLimit.SecondaryWindow} {
		if window == nil {
			continue
		}
		if window.UsedPercent < 0 || window.UsedPercent > 100 {
			return backend.AccountUsage{}, errors.New("openai: usage response contains an invalid used percentage")
		}
		maximumSeconds := int64(time.Duration(1<<63-1) / time.Second)
		if window.LimitWindowSeconds <= 0 || window.LimitWindowSeconds > maximumSeconds {
			return backend.AccountUsage{}, errors.New("openai: usage response contains an invalid window duration")
		}
		if window.ResetAt < 0 {
			return backend.AccountUsage{}, errors.New("openai: usage response contains an invalid reset time")
		}

		usageWindow := backend.UsageWindow{
			Duration:    time.Duration(window.LimitWindowSeconds) * time.Second,
			UsedPercent: window.UsedPercent,
		}
		if window.ResetAt > 0 {
			usageWindow.ResetsAt = time.Unix(window.ResetAt, 0)
		}
		windows = append(windows, usageWindow)
	}

	return backend.AccountUsage{Windows: windows}, nil
}

var _ backend.UsageProvider = (*Client)(nil)
