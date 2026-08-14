package terminal

import (
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/eul-ai/eul/agent"
)

func renderFrame(model *tuiModel) string {
	var renderer tuiRenderer
	return renderModel(&renderer, model)
}

func renderModel(renderer *tuiRenderer, model *tuiModel) string {
	normalizeViewport(model, renderer)
	return renderer.render(model)
}

func TestRenderFrameShowsRuledInputAndStatus(t *testing.T) {
	model := newTUIModel(72, 12, Options{Config: Config{
		Model: "gpt-5.6-sol", ThinkingLevel: agent.ThinkingXHigh, FastMode: true, ContextWindow: 272_000,
	}})
	model.contextTokens = 84_320
	model.appendBlock(blockUser, "hello")
	model.appendStream(blockAssistant, "answer")
	model.activity = activity{kind: activityThinking}

	frame := renderFrame(model)
	for _, want := range []string{"hello", "answer", "> ", "────────────────", "gpt-5.6-sol", string(agent.ThinkingXHigh), "31%"} {
		if !strings.Contains(frame, want) {
			t.Fatalf("frame omits %q:\n%q", want, frame)
		}
	}

	left, right := renderStatus(model, model.width)
	spinner, activity := splitActivitySpinner(model, left)
	status := ansiForeground(currentTheme.accent) + spinner + ansiForeground(currentTheme.muted) + activity + strings.Repeat(" ", model.width-cellWidth(left)-cellWidth(right)) + right
	if !strings.Contains(frame, status) || strings.Count(frame, "\x1b[12;") != 1 {
		t.Fatalf("status metadata is not independently right-aligned with an accented spinner: %q", frame)
	}
	if !strings.Contains(frame, ansiColors(currentTheme.error, terminalColor{}, false)) {
		t.Fatalf("frame does not use the xhigh thinking color: %q", frame)
	}
	if strings.Contains(frame, "\x1b[48;2;23;27;36m") || strings.Contains(frame, ansiColors(currentTheme.foreground, currentTheme.editorLine, true)) || !strings.Contains(frame, "\x1b[49m") {
		t.Fatalf("frame does not preserve the terminal background: %q", frame)
	}
	if !strings.HasPrefix(frame, ansiBeginSynchronizedOutput+ansiHideCursor) || !strings.HasSuffix(frame, ansiEndSynchronizedOutput) {
		t.Fatalf("frame does not use synchronized output: %q", frame)
	}
}

func TestRenderFrameShowsPermission(t *testing.T) {
	model := newTUIModel(72, 12, Options{})
	model.running = true
	model.showPermission(PermissionRequest{
		Title:        "Network access requested",
		Subject:      "bash",
		Description:  "needs access to the network",
		Detail:       "git push origin main",
		DetailPrefix: "$ ",
		Notice:       "This command and its descendants will have network access.",
	}, 1, 2)
	if err := model.insertInput("queued steering"); err != nil {
		t.Fatal(err)
	}

	frame := buildTerminalFrame(model)
	joined := strings.Join(frame.plainRows, "\n")
	for _, want := range []string{
		"Network access requested",
		"bash needs access to the network",
		"$ git push origin main",
		"This command and its descendants will have network access.",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("frame omits %q: %q", want, frame.plainRows)
		}
	}
	input, _ := modelInputLayout(model)
	if len(input.lines) < 2 || strings.TrimSpace(input.lines[0]) != "" || strings.TrimSpace(input.lines[len(input.lines)-1]) != "" {
		t.Fatalf("permission spacing = %q", input.lines)
	}
	descriptionIndex := slices.IndexFunc(input.styledLines, func(line styledLine) bool { return len(line.spans) > 0 && line.spans[0].text == "bash" })
	if descriptionIndex < 0 {
		t.Fatalf("permission description missing: %q", input.lines)
	}
	description := input.styledLines[descriptionIndex]
	if len(description.spans) != 3 || description.spans[0].text != "bash" || description.spans[0].style.foreground != inlineForegroundAccent || description.spans[2].style.foreground != inlineForegroundDefault {
		t.Fatalf("permission description = %+v", description.spans)
	}
	detailIndex := slices.IndexFunc(input.styledLines, func(line styledLine) bool { return line.text == model.permission.detail })
	if detailIndex < 0 || input.styledLines[detailIndex].style != (lineStyle{foreground: currentTheme.markdownCode, background: currentTheme.editorLine, paintBackground: true}) {
		t.Fatalf("permission detail does not use the panel background: %+v", input.styledLines)
	}
	noticeIndex := slices.IndexFunc(input.styledLines, func(line styledLine) bool { return line.text == model.permission.notice })
	if noticeIndex < 1 || noticeIndex+2 >= len(input.lines) || strings.TrimSpace(input.lines[noticeIndex-1]) != "" || strings.TrimSpace(input.lines[noticeIndex+1]) != "" {
		t.Fatalf("permission notice spacing = %q", input.lines)
	}
	buttons := input.lines[noticeIndex+2]
	buttonIndex := strings.Index(buttons, "[")
	descriptionIndexInLine := strings.Index(input.lines[descriptionIndex], model.permission.subject)
	buttonIndent := cellWidth(buttons[:max(buttonIndex, 0)])
	descriptionIndent := cellWidth(input.lines[descriptionIndex][:max(descriptionIndexInLine, 0)])
	if strings.TrimSpace(buttons) == "" || buttonIndex < 0 || descriptionIndexInLine < 0 || buttonIndent != descriptionIndent {
		t.Fatalf("permission button alignment = %q, description = %q", buttons, input.lines[descriptionIndex])
	}
	if frame.cursorVisible || model.inputText() != "queued steering" {
		t.Fatalf("cursor=%v input=%q", frame.cursorVisible, model.inputText())
	}
}

func assertCachedConversationMatchesUncached(t *testing.T, renderer *tuiRenderer, model *tuiModel) {
	t.Helper()

	prepared := renderer.prepare(model)
	wantLines := modelConversationLines(model, model.width)
	if !reflect.DeepEqual(prepared.conversationLines, wantLines) {
		t.Fatalf("cached conversation lines differ from uncached projection:\ngot:  %+v\nwant: %+v", prepared.conversationLines, wantLines)
	}
	wantPlain := make([]string, len(wantLines))
	for index, line := range wantLines {
		wantPlain[index] = renderedLineText(line, model.width)
	}
	if !slices.Equal(prepared.conversationPlain, wantPlain) {
		t.Fatalf("cached plain lines differ from uncached projection:\ngot:  %q\nwant: %q", prepared.conversationPlain, wantPlain)
	}
	frame := projectTerminalFrame(model, prepared)
	if !slices.Equal(frame.conversationLines, wantPlain) {
		t.Fatalf("frame plain conversation lines = %q, want %q", frame.conversationLines, wantPlain)
	}
}

func TestRenderStatusSanitizesMetadata(t *testing.T) {
	model := newTUIModel(80, 8, Options{Config: Config{Model: "safe\x1b[31m", ThinkingLevel: agent.ThinkingLevel("high\a")}})
	left, right := renderStatus(model, 80)
	status := left + right
	if strings.ContainsAny(status, "\x1b\a") || !strings.Contains(right, "safe [31m") || !strings.Contains(right, "high") {
		t.Fatalf("status = %q / %q", left, right)
	}
}

func TestRenderStatusPrioritizesActivityAndContext(t *testing.T) {
	model := newTUIModel(80, 20, Options{Config: Config{Model: "very-long-model", ThinkingLevel: agent.ThinkingMax, ContextWindow: 100}})
	model.contextTokens = 50
	model.activity = activity{kind: activityCompacting}

	wideLeft, wideRight := renderStatus(model, 80)
	if model.activity.kind != activityCompacting || wideLeft == "" || !strings.Contains(wideRight, "very-long-model") || !strings.Contains(wideRight, string(agent.ThinkingMax)) || !strings.Contains(wideRight, "50%") {
		t.Fatalf("wide status = %q / %q", wideLeft, wideRight)
	}
	narrowLeft, narrowRight := renderStatus(model, 33)
	if strings.Contains(narrowRight, "very-long-model") || narrowLeft == "" || !strings.Contains(narrowRight, "50%") {
		t.Fatalf("narrow status = %q / %q", narrowLeft, narrowRight)
	}
}

func TestTUIModelShowsGenerationRetries(t *testing.T) {
	model := newTUIModel(80, 24, Options{})
	model.applyAgentEvent(agent.Event{Kind: agent.EventGenerationRetry, Attempt: 2})

	left, _ := renderStatus(model, 80)
	if model.activity.kind != activityRetrying || !strings.Contains(left, strconv.Itoa(2)) {
		t.Fatalf("activity = %+v, status = %q", model.activity, left)
	}
}

func TestTUIModelTracksActivityAndContext(t *testing.T) {
	model := newTUIModel(80, 24, Options{})
	model.contextTokens = 100
	model.applyAgentEvent(agent.Event{Kind: agent.EventCompactionStart})
	if model.activity.kind != activityCompacting || len(model.blocks) != 0 {
		t.Fatalf("activity = %+v, blocks = %+v", model.activity, model.blocks)
	}
	model.applyAgentEvent(agent.Event{Kind: agent.EventCompactionEnd})
	if model.activity.kind != activityThinking || model.contextTokens != 0 || len(model.blocks) != 1 || model.blocks[0].kind != blockContext || model.blocks[0].text != "Context compacted" {
		t.Fatalf("activity = %+v, context = %d, blocks = %+v", model.activity, model.contextTokens, model.blocks)
	}
	model.applyAgentEvent(agent.Event{Kind: agent.EventContextUsage, Usage: agent.Usage{TotalTokens: 123}})
	if model.contextTokens != 123 {
		t.Fatalf("context tokens = %d", model.contextTokens)
	}
	model.applyAgentEvent(agent.Event{Kind: agent.EventToolStart, Call: agent.ToolCall{ID: "call-1", Name: "read"}, Presentation: agent.ToolPresentation{Title: "read", Arguments: "file.go"}})
	if model.activity.kind != activityTool || model.activity.detail != "read" || model.blocks[len(model.blocks)-1].kind != blockToolPending {
		t.Fatalf("activity = %+v, blocks = %+v", model.activity, model.blocks)
	}
	toolBlocks := len(model.blocks)
	model.applyAgentEvent(agent.Event{Kind: agent.EventToolEnd, Call: agent.ToolCall{ID: "call-1", Name: "read"}, Presentation: agent.ToolPresentation{Title: "read file.go"}, Result: agent.ToolResult{Tool: "read", IsError: true, Output: "failed"}})
	last := model.blocks[len(model.blocks)-1]
	if model.activity.kind != activityThinking || len(model.blocks) != toolBlocks || last.kind != blockToolError {
		t.Fatalf("activity = %+v, blocks = %+v", model.activity, model.blocks)
	}
}
