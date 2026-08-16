package responses

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/backend/compaction"
)

type compactRequestBuild struct {
	wire  createResponseRequest
	input []json.RawMessage
}

func (client *Client) buildNativeCompactRequest(request agent.Request) (compactRequestBuild, error) {
	build, err := buildCompactRequest(request, client.maxStateBytes)
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
	build, err := buildRequestUnchecked(request, maxStateBytes)
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

func compactedStateItems(input, output []json.RawMessage) []json.RawMessage {
	items := make([]json.RawMessage, 0, len(input)+len(output))
	for _, raw := range input {
		var item struct {
			Type string `json:"type"`
			Role string `json:"role"`
		}
		if json.Unmarshal(raw, &item) != nil {
			continue
		}
		if item.Type == "agent_message" || item.Role == "user" || item.Role == "developer" || item.Role == "system" {
			items = append(items, raw)
		}
	}

	return append(items, output...)
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
	state, err := encodeState(nil, nil, compactedStateItems(build.input, stream.response.Output), client.generationStateBytes())
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
	state, err := encodeState(nil, nil, items, client.generationStateBytes())
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
