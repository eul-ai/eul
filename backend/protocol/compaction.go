package protocol

import (
	"errors"
	"strings"

	"github.com/eul-ai/eul/agent"
)

const summaryInstructions = `Create a concise, standalone handoff summary of the conversation so another coding agent can continue the task.

Preserve only continuation-critical facts: the user's current goal, requirements, and constraints; important decisions and rationale; relevant files, symbols, and code details; changes already made; commands and tests run with their outcomes; errors and unresolved issues; and the exact next steps. Include pending user requests and relevant tool findings. Do not continue the task or address the user. Output only the summary.`

const (
	Continuation        = "Continue the task from the compacted summary."
	SummaryQuestion     = "What happened earlier in this conversation?"
	summaryIntroduction = "The earlier conversation was compacted into the following summary. Continue the task from this context:"
	summaryRequest      = "Produce the requested handoff summary now."
	compactionThreshold = 90
	imageInputTokens    = 1_024
	bytesPerToken       = 4
	maxStateBytes       = 16 * 1024 * 1024
)

func prepareSummary(request agent.Request) (agent.Request, bool) {
	continueTask := len(request.Inputs) != 0
	request.Instructions = summaryInstructions
	request.Tools = nil
	request.Inputs = append(append([]agent.Input(nil), request.Inputs...), agent.NewTextInput(summaryRequest))
	return request, continueTask
}

func validateSummary(text string, toolCallCount int) (string, error) {
	if toolCallCount != 0 {
		return "", errors.New("summary response contains tool calls")
	}

	summary := strings.TrimSpace(text)
	if summary == "" {
		return "", errors.New("summary response is empty")
	}
	return summary, nil
}

func formatSummary(summary string) string {
	return summaryIntroduction + "\n\n" + summary
}

func shouldCompact(request agent.Request, usage agent.Usage, contextWindow int64) bool {
	if len(request.State) == 0 {
		return false
	}
	if len(request.State) > maxStateBytes {
		return true
	}
	if usage.TotalTokens <= 0 || contextWindow <= 0 {
		return false
	}

	limit := contextWindow * compactionThreshold / 100
	if usage.TotalTokens >= limit {
		return true
	}
	return estimateInputTokens(request.Inputs) >= limit-usage.TotalTokens
}

func estimateInputTokens(inputs []agent.Input) int64 {
	var total int64
	for _, input := range inputs {
		textBytes := len(input.Text)
		if input.Kind == agent.InputUser {
			textBytes = len(input.PlainText())
			for _, part := range input.Content {
				if part.Kind == agent.ContentPartImage {
					total += imageInputTokens
				}
			}
		}
		bytes := int64(textBytes)
		total += bytes / bytesPerToken
		if bytes%bytesPerToken != 0 {
			total++
		}
	}
	return total
}
