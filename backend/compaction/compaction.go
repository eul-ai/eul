package compaction

import (
	"errors"
	"strings"

	"github.com/eul-ai/eul/agent"
)

const Instructions = `Create a concise, standalone handoff summary of the conversation so another coding agent can continue the task.

Preserve only continuation-critical facts: the user's current goal, requirements, and constraints; important decisions and rationale; relevant files, symbols, and code details; changes already made; commands and tests run with their outcomes; errors and unresolved issues; and the exact next steps. Include pending user requests and relevant tool findings. Do not continue the task or address the user. Output only the summary.`

const (
	Continuation        = "Continue the task from the compacted summary."
	SummaryQuestion     = "What happened earlier in this conversation?"
	summaryIntroduction = "The earlier conversation was compacted into the following summary. Continue the task from this context:"
	summaryRequest      = "Produce the requested handoff summary now."
)

func Prepare(request agent.Request, instructions string) (agent.Request, bool) {
	continueTask := len(request.Inputs) != 0
	request.Instructions = instructions
	request.Tools = nil
	request.Inputs = append(append([]agent.Input(nil), request.Inputs...), agent.NewTextInput(summaryRequest))
	return request, continueTask
}

func ValidateSummary(text string, toolCallCount int) (string, error) {
	if toolCallCount != 0 {
		return "", errors.New("summary response contains tool calls")
	}

	summary := strings.TrimSpace(text)
	if summary == "" {
		return "", errors.New("summary response is empty")
	}
	return summary, nil
}

func FormatSummary(summary string) string {
	return summaryIntroduction + "\n\n" + summary
}

func ShouldCompact(request agent.Request, usage agent.Usage, contextWindow int64, stateTooLarge bool) bool {
	if len(request.State) == 0 {
		return false
	}
	if stateTooLarge {
		return true
	}
	if usage.TotalTokens <= 0 || contextWindow <= 0 {
		return false
	}

	limit := contextWindow * 9 / 10
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
					total += 1_024
				}
			}
		}
		bytes := int64(textBytes)
		total += bytes / 4
		if bytes%4 != 0 {
			total++
		}
	}
	return total
}
