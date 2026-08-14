package terminal

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/eul-ai/eul/agent"
)

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
		"user":            {foreground: currentTheme.yellow},
		"assistant":       {foreground: currentTheme.foreground},
		"summary":         {foreground: currentTheme.muted, italic: true},
		"pending tool":    {foreground: currentTheme.foreground, background: currentTheme.toolPendingBackground, paintBackground: true},
		"successful tool": {foreground: currentTheme.foreground, background: currentTheme.toolSuccessBackground, paintBackground: true},
		"failed tool":     {foreground: currentTheme.foreground, background: currentTheme.toolErrorBackground, paintBackground: true},
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

func TestUserMessagesRenderInlineMarkdown(t *testing.T) {
	lines := conversationLines([]conversationBlock{{
		kind: blockUser,
		text: "Use **bold**, *italic*, and `code`",
	}}, 80)
	if len(lines) != 1 || lines[0].text != "Use bold, italic, and code" {
		t.Fatalf("lines = %+v", lines)
	}

	var rendered strings.Builder
	renderLine(&rendered, 1, 80, lines[0])
	output := rendered.String()
	for _, want := range []string{
		ansiBold + "bold" + ansiNormalIntensity,
		ansiItalic + "italic" + ansiNotItalic,
		ansiForeground(currentTheme.markdownCode) + "code" + ansiForeground(currentTheme.yellow),
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("rendered line %q omits %q", output, want)
		}
	}
}

func TestAssistantFencedCodeUsesCodePresentation(t *testing.T) {
	lines := conversationLines([]conversationBlock{{
		kind: blockAssistant,
		text: "before\n```go\n**literal**\n```\nafter",
	}}, 80)
	if len(lines) != 3 || lines[0].text != "before" || lines[1].text != "**literal**" || lines[2].text != "after" {
		t.Fatalf("lines = %+v", lines)
	}

	code := lines[1]
	if code.prefixText != "" || code.style.foreground != currentTheme.markdownCode || len(code.spans) != 0 {
		t.Fatalf("code style = %+v", code)
	}

	var rendered strings.Builder
	renderLine(&rendered, 1, 80, code)
	want := ansiColors(currentTheme.markdownCode, terminalColor{}, false) + " **literal**"
	if !strings.Contains(rendered.String(), want) {
		t.Fatalf("rendered code = %q, want %q", rendered.String(), want)
	}
}

func TestAssistantLinksAreClickable(t *testing.T) {
	lines := conversationLines([]conversationBlock{{
		kind: blockAssistant,
		text: "Read [the docs](https://example.com/docs)",
	}}, 80)
	if len(lines) != 1 || lines[0].text != "Read the docs" {
		t.Fatalf("lines = %+v", lines)
	}

	var rendered strings.Builder
	renderLine(&rendered, 1, 80, lines[0])
	open := "\x1b]8;;https://example.com/docs\x1b\\"
	if !strings.Contains(rendered.String(), open+"the docs"+ansiLinkClose) {
		t.Fatalf("rendered line = %q", rendered.String())
	}
}

func TestAssistantReasoningAndToolDetailsRenderInlineMarkdown(t *testing.T) {
	lines := conversationLines([]conversationBlock{
		{kind: blockReasoning, text: "**Planning**"},
		{kind: blockAssistant, text: "Use *care* and **`code`**"},
		{kind: blockTool, tool: agent.ToolPresentation{
			Title: "subagent", Arguments: "(3)", Markdown: true,
			Lines: []string{"1. complete — Read `./terminal/api.go`"},
		}, toolOutcome: "ok"},
	}, 80)
	if lines[0].text != "Planning" || lines[2].text != "Use care and code" {
		t.Fatalf("lines = %+v", lines)
	}

	var reasoning strings.Builder
	renderLine(&reasoning, 1, 80, lines[0])
	if !strings.Contains(reasoning.String(), ansiBold+"Planning") || !strings.Contains(reasoning.String(), ansiItalic) {
		t.Fatalf("reasoning line = %q", reasoning.String())
	}
	var assistant strings.Builder
	renderLine(&assistant, 1, 80, lines[2])
	if !strings.Contains(assistant.String(), ansiItalic+"care"+ansiNotItalic) {
		t.Fatalf("assistant italic = %q", assistant.String())
	}
	code := ansiForeground(currentTheme.markdownCode) + ansiBold + "code" + ansiForeground(currentTheme.foreground) + ansiNormalIntensity
	if !strings.Contains(assistant.String(), code) {
		t.Fatalf("assistant code = %q", assistant.String())
	}

	var heading, detail *styledLine
	for index := range lines {
		switch lines[index].text {
		case "subagent (3) — ok":
			heading = &lines[index]
		case "1. complete — Read ./terminal/api.go":
			detail = &lines[index]
		}
	}
	if heading == nil || detail == nil {
		t.Fatalf("tool lines = %+v", lines)
	}
	var renderedHeading strings.Builder
	renderLine(&renderedHeading, 1, 80, *heading)
	headingText := renderedHeading.String()
	if !strings.Contains(headingText, ansiForeground(currentTheme.accent)+ansiBold+"subagent") || !strings.Contains(headingText, ansiForeground(currentTheme.foreground)+ansiNormalIntensity+" (3)") {
		t.Fatalf("tool heading = %q", headingText)
	}
	var renderedDetail strings.Builder
	renderLine(&renderedDetail, 1, 80, *detail)
	if strings.Contains(detail.text, "`") || !strings.Contains(renderedDetail.String(), ansiForeground(currentTheme.markdownCode)+"./terminal/api.go"+ansiForeground(currentTheme.foreground)) {
		t.Fatalf("tool detail = %q", renderedDetail.String())
	}
}

func TestToolTruncationMarkerIsMuted(t *testing.T) {
	lines := conversationLines([]conversationBlock{{
		kind: blockTool,
		tool: agent.ToolPresentation{
			Title:          "write",
			Arguments:      "tool/subagent.go",
			Lines:          []string{"package tool", "opaque-summary"},
			LinesTruncated: true,
		},
	}}, 80)

	if len(lines) < 2 || lines[len(lines)-2].style.foreground != currentTheme.muted {
		t.Fatalf("tool lines = %+v", lines)
	}
}

func TestBashToolShowsOutputTailAndDuration(t *testing.T) {
	lines := conversationLines([]conversationBlock{{
		kind: blockTool,
		tool: agent.ToolPresentation{
			Title:     "bash",
			Arguments: "go test ./...",
			Lines:     []string{"one", "two", "three", "ok github.com/eul-ai/eul/cmd", "ok github.com/eul-ai/eul/provider", "ok github.com/eul-ai/eul/terminal", "ok github.com/eul-ai/eul/tool", "ok github.com/eul-ai/eul/terminal race"},
			TailLines: 5,
			Elapsed:   2*time.Second + 900*time.Millisecond,
			Timeout:   120 * time.Second,
		},
		toolOutcome: "exit status: 0",
	}}, 80)

	var texts []string
	for _, line := range lines {
		texts = append(texts, line.text)
	}
	for _, want := range []string{"ok github.com/eul-ai/eul/cmd", "ok github.com/eul-ai/eul/terminal race"} {
		if !slices.Contains(texts, want) {
			t.Fatalf("lines = %+v, missing %q", lines, want)
		}
	}
	if slices.IndexFunc(texts, func(text string) bool {
		return strings.Contains(text, (2*time.Second+900*time.Millisecond).String()) && strings.Contains(text, strconv.Itoa(int((120*time.Second)/time.Second))+"s")
	}) < 0 {
		t.Fatalf("lines omit dynamic durations: %+v", lines)
	}
	for _, hidden := range []string{"one", "two", "three"} {
		if slices.Contains(texts, hidden) {
			t.Fatalf("lines = %+v, retained hidden line %q", lines, hidden)
		}
	}
}

func TestPendingBashToolShowsElapsedTime(t *testing.T) {
	lines := conversationLines([]conversationBlock{{
		kind: blockToolPending,
		tool: agent.ToolPresentation{
			Title:   "bash",
			Lines:   []string{"running"},
			Elapsed: time.Second + 200*time.Millisecond,
		},
	}}, 80)

	var texts []string
	for _, line := range lines {
		texts = append(texts, line.text)
	}
	if !slices.Contains(texts, "running") || slices.IndexFunc(texts, func(text string) bool { return strings.Contains(text, (time.Second + 200*time.Millisecond).String()) }) < 0 {
		t.Fatalf("lines = %+v", lines)
	}
}

func TestToolTailLimitCountsWrappedVisualLines(t *testing.T) {
	lines := conversationLines([]conversationBlock{{
		kind: blockTool,
		tool: agent.ToolPresentation{
			Title:     "bash",
			Lines:     []string{strings.Repeat("x", 100)},
			TailLines: 5,
		},
	}}, 12)

	bodyLines := 0
	for _, line := range lines {
		if line.text == strings.Repeat("x", 10) {
			bodyLines++
		}
	}
	if bodyLines != 5 || slices.IndexFunc(lines, func(line styledLine) bool { return line.text != "" && line.style.foreground == currentTheme.muted }) < 0 {
		t.Fatalf("lines = %+v", lines)
	}
}

func TestToolPlainTextDetailsPreserveMarkdownMarkers(t *testing.T) {
	lines := conversationLines([]conversationBlock{{
		kind: blockTool,
		tool: agent.ToolPresentation{Title: "write", Arguments: "README.md", Lines: []string{"Use `literal` and **markers**"}},
	}}, 80)
	for _, line := range lines {
		if line.text == "Use `literal` and **markers**" {
			if len(line.spans) != 0 {
				t.Fatalf("plain tool line has spans: %+v", line)
			}
			return
		}
	}
	t.Fatalf("plain tool line missing: %+v", lines)
}

func TestToolDiffLinesUseAddedRemovedAndContextColors(t *testing.T) {
	lines := conversationLines([]conversationBlock{{
		kind: blockTool,
		tool: agent.ToolPresentation{
			Title: "edit", Arguments: "sample.txt",
			Diff: []agent.ToolDiffLine{
				{Kind: agent.ToolDiffLineContext, OldLine: 1, NewLine: 1, Text: "before"},
				{Kind: agent.ToolDiffLineRemoved, OldLine: 2, Text: "old"},
				{Kind: agent.ToolDiffLineAdded, NewLine: 2, Text: "new"},
				{Kind: agent.ToolDiffLineContext, OldLine: 3, NewLine: 3, Text: "after"},
				{Kind: agent.ToolDiffLineOmitted, Text: "… (diff truncated)"},
			},
		},
	}}, 80)

	want := map[string]terminalColor{
		" 1 before":             currentTheme.diffContext,
		"-2 old":                currentTheme.diffRemoved,
		"+2 new":                currentTheme.diffAdded,
		" 3 after":              currentTheme.diffContext,
		"   … (diff truncated)": currentTheme.diffContext,
	}
	for _, line := range lines {
		color, exists := want[line.text]
		if !exists {
			continue
		}
		if line.style.foreground != color || line.style.background != currentTheme.toolSuccessBackground || !line.style.paintBackground {
			t.Fatalf("diff line %q style = %+v", line.text, line.style)
		}

		var rendered strings.Builder
		renderLine(&rendered, 1, 80, line)
		if !strings.Contains(rendered.String(), ansiForeground(color)) {
			t.Fatalf("diff line %q does not render color %+v: %q", line.text, color, rendered.String())
		}
		delete(want, line.text)
	}
	if len(want) != 0 {
		t.Fatalf("missing diff lines: %+v in %+v", want, lines)
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

func TestConversationWindowHasVerticalPadding(t *testing.T) {
	model := newTUIModel(40, 8, Options{})
	model.appendBlock(blockUser, "message")
	lines := modelConversationLines(model, 40)

	if len(lines) != 3 || lines[0].text != "" || lines[1].text != "message" || lines[2].text != "" {
		t.Fatalf("lines = %+v", lines)
	}
}

func TestRendererConversationBlockCacheMatchesUncachedProjection(t *testing.T) {
	model := newTUIModel(48, 12, Options{Config: Config{Model: "model"}})
	model.appendBlock(blockUser, "Use **bold** text")
	model.startTool(agent.ToolCall{ID: "read-1", Name: "read"}, agent.ToolPresentation{
		Title: "read", Arguments: "README.md", Lines: []string{"before"},
		Diff: []agent.ToolDiffLine{{Kind: agent.ToolDiffLineAdded, NewLine: 1, Text: "old diff"}},
	})
	model.appendStream(blockAssistant, "Active `streaming` response")

	renderer := &tuiRenderer{}
	assertCachedConversationMatchesUncached(t, renderer, model)
	plainBeforeImage := slices.Clone(renderer.conversationBlocks[0].plain)
	model.blocks[0].content = []agent.ContentPart{
		{Kind: agent.ContentPartText, Text: "before "},
		{Kind: agent.ContentPartImage, Image: &agent.Image{MediaType: "image/png", Data: []byte("one")}},
	}
	model.conversationChanged()
	assertCachedConversationMatchesUncached(t, renderer, model)
	if slices.Equal(renderer.conversationBlocks[0].plain, plainBeforeImage) || !strings.Contains(renderer.conversationBlocks[0].plain[0], "before ") {
		t.Fatalf("content change did not invalidate cached block: %q", renderer.conversationBlocks[0].plain)
	}
	firstBlockLine := &renderer.conversationBlocks[0].lines[0]
	previousFrame := projectTerminalFrame(model, renderer.prepare(model))
	previousPlain := append([]string(nil), previousFrame.conversationLines...)

	model.appendStream(blockAssistant, " with another delta")
	assertCachedConversationMatchesUncached(t, renderer, model)
	if &renderer.conversationBlocks[0].lines[0] != firstBlockLine {
		t.Fatal("unchanged historical block was rerendered after active stream update")
	}
	if !slices.Equal(previousFrame.conversationLines, previousPlain) {
		t.Fatal("conversation update mutated the previous frame's plain lines")
	}

	model.blocks[1].kind = blockTool
	model.blocks[1].tool.Lines[0] = "after"
	model.blocks[1].tool.Diff[0].Text = "new diff"
	model.blocks[1].toolOutcome = "ok"
	model.conversationChanged()
	if renderer.conversationBlocks[1].block.tool.Lines[0] != "before" || renderer.conversationBlocks[1].block.tool.Diff[0].Text != "old diff" {
		t.Fatal("cached block shares tool presentation slices with the model")
	}
	assertCachedConversationMatchesUncached(t, renderer, model)

	beforeWidthChange := &renderer.conversationBlocks[0].lines[0]
	model.width = 31
	assertCachedConversationMatchesUncached(t, renderer, model)
	if &renderer.conversationBlocks[0].lines[0] == beforeWidthChange {
		t.Fatal("width change did not invalidate cached blocks")
	}

	model.steering.accepted = []string{"inspect another file"}
	model.conversationChanged()
	assertCachedConversationMatchesUncached(t, renderer, model)
	model.steering.accepted = nil
	model.conversationChanged()
	assertCachedConversationMatchesUncached(t, renderer, model)

	model.selection = textSelection{
		anchor: selectionPoint{row: 1, column: 1, conversation: true},
		focus:  selectionPoint{row: 1, column: 8, conversation: true},
		set:    true,
	}
	assertCachedConversationMatchesUncached(t, renderer, model)

	checkpoint := checkpointModel(model, nil)
	model.clearConversation()
	assertCachedConversationMatchesUncached(t, renderer, model)
	if len(renderer.conversationBlocks) != 0 {
		t.Fatalf("cached blocks after clear = %d", len(renderer.conversationBlocks))
	}
	restoreModelCheckpoint(model, checkpoint)
	assertCachedConversationMatchesUncached(t, renderer, model)
}

func TestPendingSteeringRendersAndDeliversInTranscriptOrder(t *testing.T) {
	model := newTUIModel(40, 8, Options{})
	model.beginTurn("initial")
	model.appendStream(blockAssistant, "answer")
	model.steering.accepted = []string{"redirect"}
	controller := tuiController{model: model}
	model.appendStream(blockAssistant, " continues")

	lines := modelConversationLines(model, 40)
	var rendered []string
	for _, line := range lines {
		rendered = append(rendered, line.text)
	}
	if len(model.blocks) != 2 || model.blocks[1].text != "answer continues" || slices.IndexFunc(rendered, func(text string) bool { return strings.Contains(text, "redirect") }) < 0 {
		t.Fatalf("blocks=%+v lines=%q", model.blocks, rendered)
	}
	if frame := buildTerminalFrame(model); !frame.cursorVisible {
		t.Fatal("cursor hidden while agent is running")
	}

	if _, err := controller.handleAgentEvent(engineMessage{event: &agent.Event{Kind: agent.EventSteering, Text: "redirect"}}); err != nil {
		t.Fatal(err)
	}
	if len(controller.model.steering.pending()) != 0 || len(model.blocks) != 3 || model.blocks[2].kind != blockUser || model.blocks[2].text != "redirect" {
		t.Fatalf("steering=%q blocks=%+v", controller.model.steering.pending(), model.blocks)
	}
}

func TestGoalContinuationHasDistinctTranscriptBlock(t *testing.T) {
	model := newTUIModel(40, 8, Options{})
	model.beginTurn("initial")
	model.appendStream(blockAssistant, "first response")
	model.steering.accepted = []string{"same text"}

	model.applyAgentEvent(agent.Event{Kind: agent.EventGoalContinuation, Text: "same text"})
	if len(model.steering.pending()) != 1 || len(model.blocks) != 3 || model.blocks[2].kind != blockInfo {
		t.Fatalf("steering=%q blocks=%+v", model.steering.pending(), model.blocks)
	}
	model.appendStream(blockAssistant, "second response")
	if len(model.blocks) != 4 || model.blocks[3].kind != blockAssistant {
		t.Fatalf("goal continuation did not separate assistant streams: %+v", model.blocks)
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

func TestTUIModelCorrelatesAndSanitizesStreamingToolBlocks(t *testing.T) {
	model := newTUIModel(80, 24, Options{})
	model.applyAgentEvent(agent.Event{
		Kind: agent.EventToolStart, Call: agent.ToolCall{ID: "one", Name: "write"},
		Presentation: agent.ToolPresentation{Title: "write one", Lines: []string{"partial"}},
	})
	model.applyAgentEvent(agent.Event{
		Kind: agent.EventToolStart, Call: agent.ToolCall{ID: "two", Name: "subagent"},
		Presentation: agent.ToolPresentation{Title: "subagent", Lines: []string{"running"}},
	})
	model.applyAgentEvent(agent.Event{
		Kind: agent.EventToolUpdate, Call: agent.ToolCall{ID: "one", Name: "write"},
		Presentation: agent.ToolPresentation{
			Title: "write one", Lines: []string{"safe\x1b[31m"},
			Diff: []agent.ToolDiffLine{{Kind: agent.ToolDiffLineAdded, NewLine: 1, Text: "new\x1b[32m"}},
		},
	})
	model.applyAgentEvent(agent.Event{
		Kind: agent.EventToolEnd, Call: agent.ToolCall{ID: "two", Name: "subagent"},
		Presentation: agent.ToolPresentation{Title: "subagent", Lines: []string{"complete"}},
		Result:       agent.ToolResult{Tool: "subagent"},
	})

	first := model.blocks[model.toolBlockIndex("one")]
	second := model.blocks[model.toolBlockIndex("two")]
	if first.kind != blockToolPending || first.tool.Lines[0] != "safe�[31m" || first.tool.Diff[0].Text != "new�[32m" {
		t.Fatalf("first block = %+v", first)
	}
	if second.kind != blockTool || second.tool.Lines[0] != "complete" {
		t.Fatalf("second block = %+v", second)
	}
	if model.activity.kind != activityTool || model.activity.detail != "write" {
		t.Fatalf("activity = %+v", model.activity)
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

func TestTUIToolActivityOmitsArguments(t *testing.T) {
	model := newTUIModel(80, 24, Options{})
	model.applyAgentEvent(agent.Event{
		Kind: agent.EventToolStart, Call: agent.ToolCall{ID: "read-1", Name: "read"},
		Presentation: agent.ToolPresentation{Title: "read", Arguments: "first\n" + strings.Repeat("x", 1_000)},
	})
	block := model.blocks[model.toolBlockIndex("read-1")]
	if strings.Contains(block.tool.Arguments, "\n") || len(block.tool.Arguments) > maxToolPresentationSummaryBytes || model.activity.detail != "read" {
		t.Fatalf("presentation=%+v activity=%q", block.tool, model.activity.detail)
	}
}

func TestInterruptedTurnClosesPendingToolBlocks(t *testing.T) {
	model := newTUIModel(80, 24, Options{})
	model.running = true
	model.interrupted = true
	model.applyAgentEvent(agent.Event{
		Kind: agent.EventToolStart, Call: agent.ToolCall{ID: "write-1", Name: "write"},
		Presentation: agent.ToolPresentation{Title: "write file.go", Lines: []string{"partial"}},
	})
	model.finishTurn(context.Canceled)
	block := model.blocks[model.toolBlockIndex("write-1")]
	if block.kind != blockToolError {
		t.Fatalf("block = %+v", block)
	}
}

func TestConversationDividerIndicatesTextBelow(t *testing.T) {
	model := newTUIModel(20, 8, Options{})
	for index := 0; index < 8; index++ {
		model.appendBlock(blockInfo, strings.Repeat(string(rune('a'+index)), 20))
	}

	var renderer tuiRenderer
	_ = renderModel(&renderer, model)
	ruleRow := renderer.frame.layout.topRuleRow - 1
	followingDivider := renderer.frame.plainRows[ruleRow]

	scrollConversation(model, -1, renderer.frame)
	_ = renderModel(&renderer, model)
	scrolledDivider := renderer.frame.plainRows[ruleRow]
	if scrolledDivider == followingDivider {
		t.Fatalf("divider did not change after scrolling: %q", scrolledDivider)
	}

	scrollConversation(model, 1, renderer.frame)
	_ = renderModel(&renderer, model)
	if renderer.frame.plainRows[ruleRow] != followingDivider {
		t.Fatalf("divider was not restored: got %q, want %q", renderer.frame.plainRows[ruleRow], followingDivider)
	}
}

func TestConversationScrollingStopsAndResumesFollowing(t *testing.T) {
	model := newTUIModel(20, 8, Options{})
	for index := 0; index < 8; index++ {
		model.appendBlock(blockInfo, strings.Repeat(string(rune('a'+index)), 20))
	}
	var renderer tuiRenderer
	_ = renderModel(&renderer, model)
	bottom := model.scrollTop
	if bottom == 0 {
		t.Fatal("conversation did not overflow")
	}

	scrollConversation(model, -1, renderer.frame)
	if model.following || model.scrollTop >= bottom {
		t.Fatalf("after page up: following=%v top=%d bottom=%d", model.following, model.scrollTop, bottom)
	}
	oldTop := model.scrollTop
	model.appendBlock(blockInfo, "new output")
	_ = renderModel(&renderer, model)
	if model.scrollTop != oldTop {
		t.Fatalf("scrolled viewport moved from %d to %d", oldTop, model.scrollTop)
	}

	scrollConversation(model, 1, renderer.frame)
	for !model.following {
		scrollConversation(model, 1, renderer.frame)
	}
	if !model.following {
		t.Fatal("page down did not resume following")
	}
}
