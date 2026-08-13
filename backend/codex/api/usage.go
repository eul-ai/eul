package api

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/eul-ai/eul/backend"
)

type usageResponse struct {
	RateLimit *usageRateLimit `json:"rate_limit"`
}

type usageRateLimit struct {
	PrimaryWindow   *usageWindow `json:"primary_window"`
	SecondaryWindow *usageWindow `json:"secondary_window"`
}

type usageWindow struct {
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
	response, err := c.get(ctx, c.usageEndpoint, credential, "usage request")
	if err != nil {
		return backend.AccountUsage{}, err
	}
	defer response.Body.Close()

	body, truncated, err := readBounded(response.Body, c.maxResponseBytes)
	if err != nil {
		if classified := c.contextError(ctx, err, "read usage response"); classified != nil {
			return backend.AccountUsage{}, classified
		}
		return backend.AccountUsage{}, c.errorf("read usage response: %v", err)
	}
	if truncated {
		return backend.AccountUsage{}, c.errorf("usage response exceeds %d bytes", c.maxResponseBytes)
	}

	var wire usageResponse
	if err := json.Unmarshal(body, &wire); err != nil {
		return backend.AccountUsage{}, c.errorf("decode usage response: %v", err)
	}
	return normalizeProviderUsage(wire)
}

func normalizeProviderUsage(response usageResponse) (backend.AccountUsage, error) {
	if response.RateLimit == nil {
		return backend.AccountUsage{}, nil
	}

	windows := make([]backend.UsageWindow, 0, 2)
	for _, window := range []*usageWindow{response.RateLimit.PrimaryWindow, response.RateLimit.SecondaryWindow} {
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

	if len(windows) == 0 {
		return backend.AccountUsage{}, nil
	}
	return backend.AccountUsage{Windows: windows}, nil
}
