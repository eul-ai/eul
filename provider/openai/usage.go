package openai

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"yaah/agent"
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

func (c *Client) Usage(ctx context.Context) (agent.ProviderUsage, error) {
	if err := ctx.Err(); err != nil {
		return agent.ProviderUsage{}, err
	}

	credential, err := c.resolveCredential(ctx)
	if err != nil {
		return agent.ProviderUsage{}, err
	}
	response, err := c.get(ctx, c.usageEndpoint, credential, "usage request")
	if err != nil {
		return agent.ProviderUsage{}, err
	}
	defer response.Body.Close()

	body, truncated, err := readBounded(response.Body, c.maxResponseBytes)
	if err != nil {
		if classified := c.contextError(ctx, err, "read usage response"); classified != nil {
			return agent.ProviderUsage{}, classified
		}
		return agent.ProviderUsage{}, c.errorf("read usage response: %v", err)
	}
	if truncated {
		return agent.ProviderUsage{}, c.errorf("usage response exceeds %d bytes", c.maxResponseBytes)
	}

	var wire usageResponse
	if err := json.Unmarshal(body, &wire); err != nil {
		return agent.ProviderUsage{}, c.errorf("decode usage response: %v", err)
	}
	return normalizeProviderUsage(wire)
}

func normalizeProviderUsage(response usageResponse) (agent.ProviderUsage, error) {
	if response.RateLimit == nil {
		return agent.ProviderUsage{}, nil
	}

	windows := make([]agent.UsageWindow, 0, 2)
	for _, window := range []*usageWindow{response.RateLimit.PrimaryWindow, response.RateLimit.SecondaryWindow} {
		if window == nil {
			continue
		}
		if window.UsedPercent < 0 || window.UsedPercent > 100 {
			return agent.ProviderUsage{}, errors.New("openai: usage response contains an invalid used percentage")
		}
		maximumSeconds := int64(time.Duration(1<<63-1) / time.Second)
		if window.LimitWindowSeconds <= 0 || window.LimitWindowSeconds > maximumSeconds {
			return agent.ProviderUsage{}, errors.New("openai: usage response contains an invalid window duration")
		}
		if window.ResetAt < 0 {
			return agent.ProviderUsage{}, errors.New("openai: usage response contains an invalid reset time")
		}

		usageWindow := agent.UsageWindow{
			Duration:    time.Duration(window.LimitWindowSeconds) * time.Second,
			UsedPercent: window.UsedPercent,
		}
		if window.ResetAt > 0 {
			usageWindow.ResetsAt = time.Unix(window.ResetAt, 0)
		}
		windows = append(windows, usageWindow)
	}

	if len(windows) == 0 {
		return agent.ProviderUsage{}, nil
	}
	return agent.ProviderUsage{Windows: windows}, nil
}
