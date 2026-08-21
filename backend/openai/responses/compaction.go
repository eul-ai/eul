package responses

import (
	"context"
	"encoding/json"
	"fmt"
	"unicode/utf8"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/backend/compaction"
	"github.com/eul-ai/eul/backend/continuation"
)

type compactRequestBuild struct {
	wire  createResponseRequest
	input []json.RawMessage
}

func (client *Client) buildNativeCompactRequest(request agent.Request) (compactRequestBuild, error) {
	build, err := buildCompactRequestWithOptions(request, client.maxStateBytes, client.encodeInboxAsAgentMessage)
	if err != nil {
		return compactRequestBuild{}, fmt.Errorf("build compact request: %w", err)
	}
	options, err := client.optionsFor(request)
	if err != nil {
		return compactRequestBuild{}, err
	}
	build.wire = configureGenerationRequest(configureCommonRequest(build.wire, options), options)
	return build, nil
}

func buildCompactRequest(request agent.Request, maxStateBytes int) (compactRequestBuild, error) {
	return buildCompactRequestWithOptions(request, maxStateBytes, false)
}

func buildCompactRequestWithOptions(request agent.Request, maxStateBytes int, encodeInboxAsAgentMessage bool) (compactRequestBuild, error) {
	build, err := buildWireRequestWithOptions(request, maxStateBytes, encodeInboxAsAgentMessage)
	if err != nil {
		return compactRequestBuild{}, err
	}

	trigger, _ := json.Marshal(struct {
		Type string `json:"type"`
	}{Type: "compaction_trigger"})
	input := append([]json.RawMessage(nil), build.wire.Input...)
	build.wire.Input = append(build.wire.Input, trigger)

	return compactRequestBuild{wire: build.wire, input: input}, nil
}

const (
	retainedUserMessageTokenBudget = 64_000
	retainedImageTokens            = 1_024
	approximateBytesPerToken       = 4
)

type retainedUserMessage struct {
	Role    string             `json:"role"`
	Content []inputContentPart `json:"content"`
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
	content := make([]inputContentPart, 0, len(message.Content))
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

func (client *Client) Compact(ctx context.Context, request agent.Request) (agent.CompactResponse, error) {
	if err := ctx.Err(); err != nil {
		return agent.CompactResponse{}, err
	}

	build, err := client.buildNativeCompactRequest(request)
	if err != nil {
		return agent.CompactResponse{}, client.errorf("%v", err)
	}

	stream, err := client.complete(ctx, build.wire, agent.StreamObserver{}, "compact request", client.compactionReadError)
	if err != nil {
		return agent.CompactResponse{}, err
	}
	if err := validateCompletedResponse(stream.response); err != nil {
		return agent.CompactResponse{}, client.errorf("%v", err)
	}
	usage, err := normalizeUsage(stream.response.Usage)
	if err != nil {
		return agent.CompactResponse{}, client.errorf("%v", err)
	}
	if err := validateCompactOutput(stream.response.Output); err != nil {
		return agent.CompactResponse{}, client.errorf("%v", err)
	}
	state, err := continuation.Encode(client.generationStateBytes(), compactedStateItems(build.input, stream.response.Output))
	if err != nil {
		return agent.CompactResponse{}, client.errorf("%v", err)
	}

	return agent.CompactResponse{State: state, Usage: usage}, nil
}

func (client *Client) SemanticCompact(ctx context.Context, request agent.Request, instructions string) (agent.CompactResponse, error) {
	if err := ctx.Err(); err != nil {
		return agent.CompactResponse{}, err
	}

	request, continueAfterCompaction := compaction.Prepare(request, instructions)
	build, err := client.buildSummaryRequest(request)
	if err != nil {
		return agent.CompactResponse{}, client.errorf("%v", err)
	}

	stream, err := client.complete(ctx, build.wire, agent.StreamObserver{}, "summary request", client.compactionReadError)
	if err != nil {
		return agent.CompactResponse{}, err
	}

	summary, calls, usage, err := normalizeResponse(stream.response)
	if err != nil {
		return agent.CompactResponse{}, client.errorf("%v", err)
	}
	summary, err = compaction.ValidateSummary(summary, len(calls))
	if err != nil {
		return agent.CompactResponse{}, client.errorf("%v", err)
	}

	items, err := semanticCompactionStateItems(summary, continueAfterCompaction)
	if err != nil {
		return agent.CompactResponse{}, client.errorf("encode summary state: %v", err)
	}
	state, err := continuation.Encode(client.generationStateBytes(), items)
	if err != nil {
		return agent.CompactResponse{}, client.errorf("encode summary state: %v", err)
	}

	return agent.CompactResponse{State: state, Usage: usage}, nil
}

func semanticCompactionStateItems(summary string, continueTask bool) ([]json.RawMessage, error) {
	summaryItem, _ := json.Marshal(inputMessage{
		Role:    "assistant",
		Content: compaction.FormatSummary(summary),
	})
	items := []json.RawMessage{summaryItem}
	if !continueTask {
		return items, nil
	}

	continuation, err := encodeInputs([]agent.Input{agent.NewTextInput(compaction.Continuation)})
	if err != nil {
		return nil, err
	}
	return append(items, continuation...), nil
}

func validateCompactOutput(output []json.RawMessage) error {
	if len(output) != 1 {
		return fmt.Errorf("compact response must contain exactly one output item, got %d", len(output))
	}

	var item outputItem
	if err := json.Unmarshal(output[0], &item); err != nil {
		return fmt.Errorf("decode compact response output: %w", err)
	}
	if item.Type != "compaction" {
		return fmt.Errorf("compact response output has type %q", item.Type)
	}

	return nil
}
