package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"unicode/utf8"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/backend/protocol"
)

func (p provider) Compact(ctx context.Context, request agent.Request) (agent.CompactResponse, error) {
	if err := ctx.Err(); err != nil {
		return agent.CompactResponse{}, err
	}

	history, err := protocol.DecodeState(request.State)
	if err != nil {
		return agent.CompactResponse{}, fmt.Errorf("openai: %w", err)
	}
	wire, newItems, err := p.codec.BuildCompaction(request, history)
	if err != nil {
		return agent.CompactResponse{}, fmt.Errorf("openai: build compact request: %w", err)
	}

	result, err := p.Complete(ctx, wire, agent.StreamObserver{}, "compact request")
	if err != nil {
		return agent.CompactResponse{}, err
	}
	if err := validateCompactOutput(result.Output); err != nil {
		return agent.CompactResponse{}, fmt.Errorf("openai: %w", err)
	}

	state, err := protocol.EncodeState(compactedStateItems(slices.Concat(history, newItems), result.Output))
	if err != nil {
		return agent.CompactResponse{}, fmt.Errorf("openai: %w", err)
	}
	return agent.CompactResponse{State: state, Usage: result.Usage}, nil
}

func validateCompactOutput(output []json.RawMessage) error {
	if len(output) != 1 {
		return fmt.Errorf("compact response must contain exactly one output item, got %d", len(output))
	}

	var item struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(output[0], &item); err != nil {
		return fmt.Errorf("decode compact response output: %w", err)
	}
	if item.Type != "compaction" {
		return fmt.Errorf("compact response output has type %q", item.Type)
	}
	return nil
}

const (
	retainedUserMessageTokenBudget = 64_000
	retainedImageTokens            = 1_024
	approximateBytesPerToken       = 4
)

type retainedContentPart struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
}

type retainedUserMessage struct {
	Role    string                `json:"role"`
	Content []retainedContentPart `json:"content"`
}

func compactedStateItems(input, output []json.RawMessage) []json.RawMessage {
	return compactedStateItemsWithBudget(input, output, retainedUserMessageTokenBudget)
}

func compactedStateItemsWithBudget(input, output []json.RawMessage, tokenBudget int) []json.RawMessage {
	candidates := make([]json.RawMessage, 0, len(input))
	for _, raw := range input {
		if _, ok := decodeRetainedUserMessage(raw); ok {
			candidates = append(candidates, raw)
		}
	}

	remaining := max(0, tokenBudget)
	reversed := make([]json.RawMessage, 0, len(candidates))
	for index := len(candidates) - 1; index >= 0 && remaining > 0; index-- {
		message, _ := decodeRetainedUserMessage(candidates[index])
		tokens := retainedUserMessageTokens(message)
		if tokens <= remaining {
			reversed = append(reversed, candidates[index])
			remaining -= tokens
			continue
		}

		if truncated, ok := truncateRetainedUserMessage(message, remaining); ok {
			reversed = append(reversed, truncated)
		}
		remaining = 0
	}

	items := make([]json.RawMessage, len(reversed), len(reversed)+len(output))
	for index := range reversed {
		items[len(reversed)-1-index] = reversed[index]
	}
	return append(items, output...)
}

func decodeRetainedUserMessage(raw json.RawMessage) (retainedUserMessage, bool) {
	var message retainedUserMessage
	if json.Unmarshal(raw, &message) != nil || message.Role != "user" || len(message.Content) == 0 {
		return retainedUserMessage{}, false
	}
	for _, part := range message.Content {
		switch part.Type {
		case "input_text":
		case "input_image":
			if part.ImageURL == "" {
				return retainedUserMessage{}, false
			}
		default:
			return retainedUserMessage{}, false
		}
	}
	return message, true
}

func retainedUserMessageTokens(message retainedUserMessage) int {
	total := 0
	for _, part := range message.Content {
		switch part.Type {
		case "input_text":
			total += approximateTokenCount(part.Text)
		case "input_image":
			total += retainedImageTokens
		}
	}
	return max(1, total)
}

func truncateRetainedUserMessage(message retainedUserMessage, tokenBudget int) (json.RawMessage, bool) {
	remaining := tokenBudget
	content := make([]retainedContentPart, 0, len(message.Content))
	for _, part := range message.Content {
		switch part.Type {
		case "input_text":
			tokens := approximateTokenCount(part.Text)
			if tokens <= remaining {
				content = append(content, part)
				remaining -= tokens
				continue
			}

			part.Text = truncateUTF8Bytes(part.Text, remaining*approximateBytesPerToken)
			if part.Text != "" {
				content = append(content, part)
			}
			remaining = 0
		case "input_image":
			if remaining < retainedImageTokens {
				remaining = 0
				continue
			}
			content = append(content, part)
			remaining -= retainedImageTokens
		}
		if remaining == 0 {
			break
		}
	}
	if len(content) == 0 {
		return nil, false
	}

	message.Content = content
	encoded, _ := json.Marshal(message)
	return encoded, true
}

func approximateTokenCount(text string) int {
	bytes := len(text)
	return (bytes + approximateBytesPerToken - 1) / approximateBytesPerToken
}

func truncateUTF8Bytes(text string, maximum int) string {
	if maximum <= 0 {
		return ""
	}
	if len(text) <= maximum {
		return text
	}
	for maximum > 0 && !utf8.ValidString(text[:maximum]) {
		maximum--
	}
	return text[:maximum]
}
