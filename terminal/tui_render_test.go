package terminal

import (
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
		"context 84.3k/272k (31%)", "thinking", "\x1b[1;3H",
	} {
		if !strings.Contains(frame, want) {
			t.Fatalf("frame omits %q:\n%q", want, frame)
		}
	}
	if strings.Contains(frame, "You") || strings.Contains(frame, "Assistant") {
		t.Fatalf("frame includes role labels: %q", frame)
	}
}

func TestConversationBlocksUseCurrentTheme(t *testing.T) {
	lines := conversationLines([]conversationBlock{
		{kind: blockUser, text: "user"},
		{kind: blockAssistant, text: "assistant"},
		{kind: blockReasoning, text: "summary"},
		{kind: blockTool, text: "tool"},
		{kind: blockToolError, text: "tool error"},
	}, 40)
	want := []lineStyle{
		{foreground: currentTheme.foreground, background: currentTheme.editorLine},
		{foreground: currentTheme.foreground, background: currentTheme.background},
		{foreground: currentTheme.muted, background: currentTheme.panelBackground},
		{foreground: currentTheme.accent, background: currentTheme.toolBackground},
		{foreground: currentTheme.error, background: currentTheme.toolBackground},
	}
	for index, style := range want {
		line := lines[index*2]
		if line.style != style || line.margin != conversationEdge {
			t.Fatalf("line %d = %+v, want style %+v and margin %d", index, line, style, conversationEdge)
		}
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
	status := renderStatus(model, 80)
	if strings.ContainsAny(status, "\x1b\a") || !strings.Contains(status, "safe [31m (high)") {
		t.Fatalf("status = %q", status)
	}
}

func TestRenderStatusPrioritizesActivityAndContext(t *testing.T) {
	model := newTUIModel(80, 20, Options{Model: "very-long-model", Effort: "maximum", ContextWindow: 100})
	model.contextTokens = 50
	model.activity = activity{kind: activityCompacting}

	wide := renderStatus(model, 80)
	if !strings.HasPrefix(wide, "⠋ compacting context") || !strings.HasSuffix(wide, "very-long-model (maximum) · context 50/100 (50%)") {
		t.Fatalf("wide status = %q", wide)
	}
	narrow := renderStatus(model, 33)
	if strings.Contains(narrow, "very-long-model") || !strings.HasPrefix(narrow, "⠋ compacting") || !strings.HasSuffix(narrow, "context 50%") {
		t.Fatalf("narrow status = %q", narrow)
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
	if model.activity.kind != activityTool || !strings.Contains(model.activity.detail, "file.go") {
		t.Fatalf("activity = %+v", model.activity)
	}
	model.applyAgentEvent(agent.Event{Kind: agent.EventToolEnd, Result: agent.ToolResult{Tool: "read", IsError: true, Output: "failed"}})
	if model.activity.kind != activityThinking || model.blocks[len(model.blocks)-1].kind != blockToolError {
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
