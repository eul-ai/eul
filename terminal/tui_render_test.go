package terminal

import (
	"strconv"
	"strings"
	"testing"

	"yaah/agent"
)

func TestRenderFrameShowsRuledInputAndStatus(t *testing.T) {
	model := newTUIModel(72, 12, Options{
		Model: "gpt-5.6-sol", Effort: "xhigh", ContextWindow: 272_000,
	})
	model.contextTokens = 84_320
	model.appendBlock(blockUser, "hello")
	model.appendStream(blockAssistant, "answer")
	model.activity = activity{kind: activityThinking}

	frame := renderFrame(model)
	for _, want := range []string{
		"hello", "answer", "> ", "────────────────", "gpt-5.6-sol (xhigh)",
		"context 84.3k/272k (31%)", "thinking",
	} {
		if !strings.Contains(frame, want) {
			t.Fatalf("frame omits %q:\n%q", want, frame)
		}
	}
	if strings.Contains(frame, "You") || strings.Contains(frame, "Assistant") {
		t.Fatalf("frame includes role labels: %q", frame)
	}
	_, right := renderStatus(model, model.width)
	rightPosition := "\x1b[12;" + strconv.Itoa(model.width-cellWidth(right)+1) + "H" + right
	if !strings.Contains(frame, rightPosition) {
		t.Fatalf("status metadata is not independently right-aligned: %q", frame)
	}
	if !strings.Contains(frame, ansiColors(currentTheme.error, terminalColor{}, false)) {
		t.Fatalf("frame does not use the xhigh effort color: %q", frame)
	}
	if strings.Contains(frame, "\x1b[48;2;23;27;36m") || !strings.Contains(frame, "\x1b[49m") {
		t.Fatalf("frame does not preserve the terminal background: %q", frame)
	}
}

func TestConversationBlocksUseCurrentTheme(t *testing.T) {
	lines := conversationLines([]conversationBlock{
		{kind: blockUser, text: "user"},
		{kind: blockAssistant, text: "assistant"},
		{kind: blockReasoning, text: "summary"},
		{kind: blockToolPending, text: "pending tool"},
		{kind: blockTool, text: "successful tool"},
		{kind: blockToolError, text: "failed tool"},
		{kind: blockContext, text: "context"},
		{kind: blockError, text: "error"},
	}, 40)
	want := map[string]lineStyle{
		"user":            {foreground: currentTheme.foreground},
		"assistant":       {foreground: currentTheme.foreground},
		"summary":         {foreground: currentTheme.muted, italic: true},
		"pending tool":    {foreground: currentTheme.accent, background: currentTheme.toolPendingBackground, paintBackground: true},
		"successful tool": {foreground: currentTheme.accent, background: currentTheme.toolSuccessBackground, paintBackground: true},
		"failed tool":     {foreground: currentTheme.error, background: currentTheme.toolErrorBackground, paintBackground: true},
		"context":         {foreground: currentTheme.muted},
		"error":           {foreground: currentTheme.error},
	}
	for _, line := range lines {
		style, ok := want[line.text]
		if !ok {
			continue
		}
		if line.style != style || line.padding != conversationPadding {
			t.Fatalf("line %q = %+v, want style %+v and padding %d", line.text, line, style, conversationPadding)
		}
		delete(want, line.text)
	}
	if len(want) != 0 {
		t.Fatalf("missing themed lines: %+v", want)
	}
}

func TestToolBlockHasHorizontalAndVerticalPadding(t *testing.T) {
	lines := conversationLines([]conversationBlock{{kind: blockTool, text: "tool output"}}, 40)
	if len(lines) != 3 {
		t.Fatalf("lines = %+v", lines)
	}
	for index, line := range lines {
		if line.padding != conversationPadding || line.style.background != currentTheme.toolSuccessBackground || !line.style.paintBackground {
			t.Fatalf("line %d = %+v", index, line)
		}
	}
	if lines[0].text != "" || lines[1].text != "tool output" || lines[2].text != "" {
		t.Fatalf("lines = %+v", lines)
	}
}

func TestPaddedBlockBackgroundFillsWidth(t *testing.T) {
	style := blockPresentation(blockTool)
	var frame strings.Builder
	renderLine(&frame, 1, 6, styledLine{text: "x", style: style, padding: conversationPadding})

	want := ansiColors(style.foreground, style.background, true) + " x    " + ansiReset
	if !strings.Contains(frame.String(), want) {
		t.Fatalf("line = %q, want full-width background sequence %q", frame.String(), want)
	}
}

func TestReasoningSummaryHasBalancedVerticalSpace(t *testing.T) {
	lines := conversationLines([]conversationBlock{
		{kind: blockUser, text: "user"},
		{kind: blockReasoning, text: "\nsummary\n\n"},
		{kind: blockAssistant, text: "answer"},
	}, 40)
	if len(lines) != 5 || lines[1].text != "" || lines[2].text != "summary" || lines[3].text != "" {
		t.Fatalf("lines = %+v", lines)
	}
	if !lines[2].style.italic || lines[2].style.paintBackground {
		t.Fatalf("reasoning style = %+v", lines[2].style)
	}
}

func TestRenderFrameSanitizesConversationText(t *testing.T) {
	model := newTUIModel(40, 8, Options{})
	model.appendStream(blockAssistant, "safe\x1b[31m\rrewrite")
	frame := renderFrame(model)
	if strings.Contains(frame, "safe\x1b[31m") || !strings.Contains(frame, "safe�[31m�rewrite") {
		t.Fatalf("unsafe frame = %q", frame)
	}
}

func TestRenderStatusSanitizesMetadata(t *testing.T) {
	model := newTUIModel(80, 8, Options{Model: "safe\x1b[31m", Effort: "high\a"})
	left, right := renderStatus(model, 80)
	status := left + right
	if strings.ContainsAny(status, "\x1b\a") || !strings.Contains(right, "safe [31m (high)") {
		t.Fatalf("status = %q / %q", left, right)
	}
}

func TestRenderStatusPrioritizesActivityAndContext(t *testing.T) {
	model := newTUIModel(80, 20, Options{Model: "very-long-model", Effort: "maximum", ContextWindow: 100})
	model.contextTokens = 50
	model.activity = activity{kind: activityCompacting}

	wideLeft, wideRight := renderStatus(model, 80)
	if wideLeft != "⠋ compacting context" || wideRight != "very-long-model (maximum) · context 50/100 (50%)" {
		t.Fatalf("wide status = %q / %q", wideLeft, wideRight)
	}
	narrowLeft, narrowRight := renderStatus(model, 33)
	if strings.Contains(narrowRight, "very-long-model") || narrowLeft != "⠋ compacting context" || narrowRight != "context 50%" {
		t.Fatalf("narrow status = %q / %q", narrowLeft, narrowRight)
	}
}

func TestTUIModelTracksActivityAndContext(t *testing.T) {
	model := newTUIModel(80, 24, Options{})
	model.applyAgentEvent(agent.Event{Kind: agent.EventCompactionStart})
	if model.activity.kind != activityCompacting {
		t.Fatalf("activity = %+v", model.activity)
	}
	model.applyAgentEvent(agent.Event{Kind: agent.EventCompactionEnd})
	if model.activity.kind != activityThinking {
		t.Fatalf("activity = %+v", model.activity)
	}
	model.applyAgentEvent(agent.Event{Kind: agent.EventContextUsage, Usage: agent.Usage{TotalTokens: 123}})
	if model.contextTokens != 123 {
		t.Fatalf("context tokens = %d", model.contextTokens)
	}
	model.applyAgentEvent(agent.Event{Kind: agent.EventToolStart, Call: agent.ToolCall{Name: "read", Arguments: []byte(`{"path":"file.go"}`)}})
	if model.activity.kind != activityTool || !strings.Contains(model.activity.detail, "file.go") || model.blocks[len(model.blocks)-1].kind != blockToolPending {
		t.Fatalf("activity = %+v, blocks = %+v", model.activity, model.blocks)
	}
	toolBlocks := len(model.blocks)
	model.applyAgentEvent(agent.Event{Kind: agent.EventToolEnd, Result: agent.ToolResult{Tool: "read", IsError: true, Output: "failed"}})
	last := model.blocks[len(model.blocks)-1]
	if model.activity.kind != activityThinking || len(model.blocks) != toolBlocks || last.kind != blockToolError || !strings.Contains(last.text, " — error: failed") {
		t.Fatalf("activity = %+v, blocks = %+v", model.activity, model.blocks)
	}
}

func TestCanceledTurnKeepsCancelingActivity(t *testing.T) {
	model := newTUIModel(80, 24, Options{})
	model.activity = activity{kind: activityCanceling}
	model.applyAgentEvent(agent.Event{Kind: agent.EventAssistantText, Text: "late text"})
	if model.activity.kind != activityCanceling {
		t.Fatalf("activity = %+v", model.activity)
	}
}

func TestConversationScrollingStopsAndResumesFollowing(t *testing.T) {
	model := newTUIModel(20, 8, Options{})
	for index := 0; index < 8; index++ {
		model.appendBlock(blockInfo, strings.Repeat(string(rune('a'+index)), 20))
	}
	conversationViewport(model, model.width, calculateLayout(model.height).conversationHeight)
	bottom := model.scrollTop
	if bottom == 0 {
		t.Fatal("conversation did not overflow")
	}

	scrollConversation(model, -1)
	if model.following || model.scrollTop >= bottom {
		t.Fatalf("after page up: following=%v top=%d bottom=%d", model.following, model.scrollTop, bottom)
	}
	oldTop := model.scrollTop
	model.appendBlock(blockInfo, "new output")
	conversationViewport(model, model.width, calculateLayout(model.height).conversationHeight)
	if model.scrollTop != oldTop {
		t.Fatalf("scrolled viewport moved from %d to %d", oldTop, model.scrollTop)
	}

	scrollConversation(model, 1)
	for !model.following {
		scrollConversation(model, 1)
	}
	if !model.following {
		t.Fatal("page down did not resume following")
	}
}

func TestRenderFrameHandlesTinyDimensions(t *testing.T) {
	for _, size := range [][2]int{{1, 1}, {2, 2}, {3, 3}, {4, 4}} {
		model := newTUIModel(size[0], size[1], Options{Model: "model", Effort: "high"})
		model.appendBlock(blockAssistant, "界")
		if frame := renderFrame(model); frame == "" {
			t.Fatalf("renderFrame(%dx%d) returned empty frame", size[0], size[1])
		}
	}
}

func TestCellWidthHandlesCombiningAndWideRunes(t *testing.T) {
	if got := cellWidth("e\u0301界"); got != 3 {
		t.Fatalf("cellWidth() = %d", got)
	}
	if got := truncateCells("ab界cd", 4, false); got != "ab界" {
		t.Fatalf("truncateCells() = %q", got)
	}
}
