package terminal

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

func renderStatus(model *tuiModel, width int) (string, string) {
	return renderStatusAt(model, width, time.Now())
}

func renderStatusAt(model *tuiModel, width int, now time.Time) (string, string) {
	activity := activityText(model)
	modelText := model.model + " (" + string(model.thinkingLevel) + ")"
	if model.fastMode {
		modelText += " fast"
	}
	if model.sessionID != "" {
		modelText += " · session " + truncateCells(model.sessionID, 8, false)
	}
	contextLong, contextShort := contextText(model.contextTokens, model.contextWindow)
	usageLong, usageShort := providerUsageText(model.providerUsage, now)

	candidates := []string{
		modelText + " · " + contextLong,
		modelText + " · " + contextShort,
		contextShort,
		"",
	}
	if usageLong != "" {
		candidates = []string{
			modelText + " · " + contextLong + " · " + usageLong,
			modelText + " · " + contextShort + " · " + usageShort,
			contextShort + " · " + usageShort,
			usageLong,
			usageShort,
			modelText + " · " + contextLong,
			contextShort,
			"",
		}
	}
	for _, right := range candidates {
		gap := 0
		if right != "" && activity != "" {
			gap = 1
		}
		if cellWidth(activity)+gap+cellWidth(right) <= width {
			return activity, right
		}
	}
	return truncateCells(activity, width, true), ""
}

func providerUsageText(usage ProviderUsage, now time.Time) (string, string) {
	windows := append([]UsageWindow(nil), usage.Windows...)
	sort.SliceStable(windows, func(left, right int) bool {
		return windows[left].Duration < windows[right].Duration
	})

	validWindows := 0
	for _, window := range windows {
		if window.Duration > 0 {
			validWindows++
		}
	}

	long := make([]string, 0, validWindows)
	short := make([]string, 0, validWindows)
	for _, window := range windows {
		if window.Duration <= 0 {
			continue
		}
		used := min(100, max(0, window.UsedPercent))
		longText := fmt.Sprintf("usage %d%%", used)
		shortText := longText
		if validWindows > 1 {
			label := usageWindowLabel(window.Duration)
			longText = fmt.Sprintf("%s usage %d%%", label, used)
			shortText = fmt.Sprintf("%s %d%%", label, used)
		}
		if !window.ResetsAt.IsZero() {
			reset := resetCountdown(window.ResetsAt, now)
			if reset == "now" {
				longText += " (resets now)"
				shortText += " (resets now)"
			} else {
				longText += " (resets in " + reset + ")"
				shortText += " (resets in " + reset + ")"
			}
		}
		long = append(long, longText)
		short = append(short, shortText)
	}
	return strings.Join(long, " · "), strings.Join(short, " · ")
}

func resetCountdown(reset, now time.Time) string {
	remaining := reset.Sub(now)
	if remaining <= 0 {
		return "now"
	}

	minutes := int64(remaining / time.Minute)
	if remaining%time.Minute != 0 {
		minutes++
	}
	days := minutes / (24 * 60)
	minutes -= days * 24 * 60
	hours := minutes / 60
	minutes -= hours * 60

	switch {
	case days > 0 && hours > 0:
		return fmt.Sprintf("%dd %dh", days, hours)
	case days > 0:
		return fmt.Sprintf("%dd", days)
	case hours > 0 && minutes > 0:
		return fmt.Sprintf("%dh %dm", hours, minutes)
	case hours > 0:
		return fmt.Sprintf("%dh", hours)
	default:
		return fmt.Sprintf("%dm", max(int64(1), minutes))
	}
}

func usageWindowLabel(duration time.Duration) string {
	switch {
	case duration%(24*time.Hour) == 0:
		return strconv.FormatInt(int64(duration/(24*time.Hour)), 10) + "d"
	case duration%time.Hour == 0:
		return strconv.FormatInt(int64(duration/time.Hour), 10) + "h"
	case duration%time.Minute == 0:
		return strconv.FormatInt(int64(duration/time.Minute), 10) + "m"
	default:
		return duration.String()
	}
}

func splitActivitySpinner(model *tuiModel, text string) (string, string) {
	if model.activity.kind == activityReady || model.activity.kind == activityPermission || model.activity.kind == activityError || text == "" {
		return "", text
	}

	_, size := utf8.DecodeRuneInString(text)
	return text[:size], text[size:]
}

func activityText(model *tuiModel) string {
	label := "ready"
	switch model.activity.kind {
	case activityThinking:
		label = "thinking"
	case activityResponding:
		label = "responding"
	case activityRetrying:
		label = "retrying response"
		if model.activity.detail != "" {
			label += " (" + model.activity.detail + ")"
		}
	case activityCompacting:
		label = "compacting context"
	case activityTool:
		label = model.activity.detail
	case activityPermission:
		label = "waiting for permission"
	case activityCanceling:
		label = "canceling"
	case activityError:
		label = "error: " + model.activity.detail
	}
	if model.activity.kind == activityReady || model.activity.kind == activityPermission || model.activity.kind == activityError {
		return label
	}
	frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	return frames[model.spinner%len(frames)] + " " + label
}

func contextText(tokens, window int64) (string, string) {
	if window <= 0 {
		text := "context " + formatTokens(tokens)
		return text, text
	}

	text := fmt.Sprintf("context %d%%", tokens*100/window)
	return text, text
}

func formatTokens(tokens int64) string {
	switch {
	case tokens >= 1_000_000 && tokens%1_000_000 == 0:
		return strconv.FormatInt(tokens/1_000_000, 10) + "m"
	case tokens >= 1_000_000:
		return fmt.Sprintf("%.1fm", float64(tokens)/1_000_000)
	case tokens >= 1_000 && tokens%1_000 == 0:
		return strconv.FormatInt(tokens/1_000, 10) + "k"
	case tokens >= 1_000:
		return fmt.Sprintf("%.1fk", float64(tokens)/1_000)
	default:
		return strconv.FormatInt(tokens, 10)
	}
}
