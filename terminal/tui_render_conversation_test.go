package terminal

import (
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/session"
	"github.com/eul-ai/eul/terminal/keys"
	"github.com/eul-ai/eul/terminal/text"
)

func TestConversationBlocksUseCurrentTheme(t *testing.T) {
	lines := conversationLines([]session.Entry{
		{Kind: session.EntryUser, Text: "user"},
		{Kind: session.EntryAssistant, Text: "assistant"},
		{Kind: session.EntryReasoning, Text: "summary"},
		{Kind: session.EntryToolPending, ToolName: "pending tool"},
		{Kind: session.EntryTool, ToolName: "successful tool"},
		{Kind: session.EntryToolError, ToolName: "failed tool"},
		{Kind: session.EntryContext, Text: "context"},
		{Kind: session.EntryError, Text: "error"},
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
	lines := conversationLines([]session.Entry{{
		Kind: session.EntryUser,
		Text: "Use **bold**, *italic*, and `code`",
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

func TestConversationProseWrapsAtWordBoundaries(t *testing.T) {
	lines := conversationLines([]session.Entry{{Kind: session.EntryAssistant, Text: "alpha beta"}}, 10)
	if len(lines) != 2 || lines[0].text != "alpha" || lines[1].text != "beta" {
		t.Fatalf("lines = %+v", lines)
	}
	if !lines[1].breakBefore.Continuation || lines[1].breakBefore.Separator != " " {
		t.Fatalf("soft-wrapped line = %+v", lines[1])
	}
}

func TestRawToolOutputRemainsHardWrapped(t *testing.T) {
	lines := conversationLines([]session.Entry{{
		Kind:     session.EntryTool,
		ToolName: "bash", Tool: agent.ToolPresentation{Lines: []string{"abc def"}},
	}}, 7)
	if !slices.ContainsFunc(lines, func(line styledLine) bool { return line.text == "abc d" }) {
		t.Fatalf("lines = %+v", lines)
	}
}

func TestToolDiffRemainsHardWrapped(t *testing.T) {
	lines := conversationLines([]session.Entry{{
		Kind:     session.EntryTool,
		ToolName: "edit",
		Tool: agent.ToolPresentation{
			Diff: []agent.ToolDiffLine{{Kind: agent.ToolDiffLineContext, OldLine: 1, NewLine: 1, Text: "abc def"}},
		},
	}}, 10)
	if !slices.ContainsFunc(lines, func(line styledLine) bool { return line.text == " 1 abc d" }) {
		t.Fatalf("lines = %+v", lines)
	}
}

func TestAssistantFencedCodeUsesCodePresentation(t *testing.T) {
	lines := conversationLines([]session.Entry{{
		Kind: session.EntryAssistant,
		Text: "before\n```go\n**literal**\n```\nafter",
	}}, 80)
	if len(lines) != 3 || lines[0].text != "before" || lines[1].text != "**literal**" || lines[2].text != "after" {
		t.Fatalf("lines = %+v", lines)
	}

	code := lines[1]
	if code.prefixText != "" || code.style.foreground != currentTheme.markdownCode || len(code.spans) != 1 || code.spans[0].Style != (text.Style{}) {
		t.Fatalf("code style = %+v", code)
	}

	var rendered strings.Builder
	renderLine(&rendered, 1, 80, code)
	want := ansiColors(currentTheme.markdownCode, terminalColor{}, false) + " **literal**"
	if !strings.Contains(rendered.String(), want) {
		t.Fatalf("rendered code = %q, want %q", rendered.String(), want)
	}
}

func TestAssistantThematicBreakUsesMutedPresentation(t *testing.T) {
	lines := conversationLines([]session.Entry{{Kind: session.EntryAssistant, Text: "---"}}, 10)
	if len(lines) != 1 || lines[0].text != "────────" {
		t.Fatalf("lines = %+v", lines)
	}
	if lines[0].style.foreground != currentTheme.muted || lines[0].style.bold || lines[0].style.italic {
		t.Fatalf("thematic break style = %+v", lines[0].style)
	}
}

func TestAssistantBlockQuoteUsesMutedGutter(t *testing.T) {
	lines := conversationLines([]session.Entry{{Kind: session.EntryAssistant, Text: "> quote"}}, 20)
	if len(lines) != 1 || lines[0].text != "│ quote" {
		t.Fatalf("lines = %+v", lines)
	}

	var rendered strings.Builder
	renderLine(&rendered, 1, 20, lines[0])
	want := ansiForeground(currentTheme.muted) + "│ " + ansiForeground(currentTheme.foreground) + "quote"
	if !strings.Contains(rendered.String(), want) {
		t.Fatalf("rendered quote = %q, want %q", rendered.String(), want)
	}
}

func TestAssistantLinksAreClickable(t *testing.T) {
	lines := conversationLines([]session.Entry{{
		Kind: session.EntryAssistant,
		Text: "Read [the docs](https://example.com/docs)",
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
	lines := conversationLines([]session.Entry{
		{Kind: session.EntryReasoning, Text: "**Planning**"},
		{Kind: session.EntryAssistant, Text: "Use *care* and **`code`**"},
		{Kind: session.EntryTool, ToolName: "launch_subagents", Tool: agent.ToolPresentation{
			Arguments: "(3)", Markdown: true,
			Lines: []string{"1. complete — Read `./terminal/api.go`"}, Outcome: "ok",
		}},
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
		case "launch_subagents (3) — ok":
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
	if !strings.Contains(headingText, ansiForeground(currentTheme.accent)+ansiBold+"launch_subagents") || !strings.Contains(headingText, ansiForeground(currentTheme.foreground)+ansiNormalIntensity+" (3)") {
		t.Fatalf("tool heading = %q", headingText)
	}
	var renderedDetail strings.Builder
	renderLine(&renderedDetail, 1, 80, *detail)
	if strings.Contains(detail.text, "`") || !strings.Contains(renderedDetail.String(), ansiForeground(currentTheme.markdownCode)+"./terminal/api.go"+ansiForeground(currentTheme.foreground)) {
		t.Fatalf("tool detail = %q", renderedDetail.String())
	}
}

func TestToolTruncationMarkerIsMuted(t *testing.T) {
	lines := conversationLines([]session.Entry{{
		Kind:     session.EntryTool,
		ToolName: "write",
		Tool: agent.ToolPresentation{
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
	lines := conversationLines([]session.Entry{{
		Kind:     session.EntryTool,
		ToolName: "bash",
		Tool: agent.ToolPresentation{
			Arguments: "go test ./...",
			Lines:     []string{"one", "two", "three", "ok github.com/eul-ai/eul/cmd", "ok github.com/eul-ai/eul/provider", "ok github.com/eul-ai/eul/terminal", "ok github.com/eul-ai/eul/tool", "ok github.com/eul-ai/eul/terminal race"},
			TailLines: 5,
			Elapsed:   2*time.Second + 900*time.Millisecond,
			Timeout:   120 * time.Second,
			Outcome:   "exit status: 0",
		},
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
	lines := conversationLines([]session.Entry{{
		Kind:     session.EntryToolPending,
		ToolName: "bash",
		Tool: agent.ToolPresentation{
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
	lines := conversationLines([]session.Entry{{
		Kind:     session.EntryTool,
		ToolName: "bash",
		Tool: agent.ToolPresentation{
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
	lines := conversationLines([]session.Entry{{
		Kind:     session.EntryTool,
		ToolName: "write", Tool: agent.ToolPresentation{Arguments: "README.md", Lines: []string{"Use `literal` and **markers**"}},
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
	lines := conversationLines([]session.Entry{{
		Kind:     session.EntryTool,
		ToolName: "edit",
		Tool: agent.ToolPresentation{
			Arguments: "sample.txt",
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
	lines := conversationLines([]session.Entry{{Kind: session.EntryTool, ToolName: "tool output"}}, 40)
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
	model := newTestModel(40, 8, Options{})
	model.appendEntry(session.EntryUser, "message")
	lines := modelConversationLines(model, 40)

	if len(lines) != 3 || lines[0].text != "" || lines[1].text != "message" || lines[2].text != "" {
		t.Fatalf("lines = %+v", lines)
	}
}

func TestRendererConversationBlockCacheMatchesUncachedProjection(t *testing.T) {
	model := newTestModel(48, 12, Options{Config: Config{Model: "model"}})
	model.appendEntry(session.EntryUser, "Use **bold** text")
	applyEvent(model, agent.Event{Kind: agent.EventToolStart, Call: agent.ToolCall{ID: "read-1", Name: "read"}, Presentation: agent.ToolPresentation{
		Arguments: "README.md", Lines: []string{"before"},
		Diff: []agent.ToolDiffLine{{Kind: agent.ToolDiffLineAdded, NewLine: 1, Text: "old diff"}},
	}})
	streamAssistantText(model, "Active `streaming` response")

	renderer := &tuiRenderer{}
	assertCachedConversationMatchesUncached(t, renderer, model)
	plainBeforeImage := slices.Clone(renderer.conversationBlocks[0].plain)
	model.entries[0].Content = []agent.ContentPart{
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

	streamAssistantText(model, " with another delta")
	assertCachedConversationMatchesUncached(t, renderer, model)
	if &renderer.conversationBlocks[0].lines[0] != firstBlockLine {
		t.Fatal("unchanged historical block was rerendered after active stream update")
	}
	if !slices.Equal(previousFrame.conversationLines, previousPlain) {
		t.Fatal("conversation update mutated the previous frame's plain lines")
	}

	model.entries[1].Kind = session.EntryTool
	model.entries[1].Tool = agent.ToolPresentation{
		Arguments: "README.md", Lines: []string{"after"}, Outcome: "ok",
		Diff: []agent.ToolDiffLine{{Kind: agent.ToolDiffLineAdded, NewLine: 1, Text: "new diff"}},
	}
	model.conversationChanged()
	assertCachedConversationMatchesUncached(t, renderer, model)

	beforeWidthChange := &renderer.conversationBlocks[0].lines[0]
	model.width = 31
	assertCachedConversationMatchesUncached(t, renderer, model)
	if &renderer.conversationBlocks[0].lines[0] == beforeWidthChange {
		t.Fatal("width change did not invalidate cached blocks")
	}

	setQueue(model, testTextContent("inspect another file"))
	assertCachedConversationMatchesUncached(t, renderer, model)
	setQueue(model)
	assertCachedConversationMatchesUncached(t, renderer, model)

	model.selection = textSelection{
		anchor: selectionPoint{row: 1, column: 1, conversation: true},
		focus:  selectionPoint{row: 1, column: 8, conversation: true},
		set:    true,
	}
	assertCachedConversationMatchesUncached(t, renderer, model)

	entries := model.entries
	model.entries = nil
	model.conversationChanged()
	assertCachedConversationMatchesUncached(t, renderer, model)
	if len(renderer.conversationBlocks) != 0 {
		t.Fatalf("cached blocks after clear = %d", len(renderer.conversationBlocks))
	}
	model.entries = entries
	model.conversationChanged()
	assertCachedConversationMatchesUncached(t, renderer, model)
}

func TestPendingSteeringRendersAndDeliversInTranscriptOrder(t *testing.T) {
	model := newTestModel(40, 8, Options{})
	appendUser(model, textContent("initial"))
	setRunning(model)
	streamAssistantText(model, "answer")
	setQueue(model, testTextContent("redirect"))
	streamAssistantText(model, " continues")

	lines := modelConversationLines(model, 40)
	var rendered []string
	for _, line := range lines {
		rendered = append(rendered, line.text)
	}
	redirectIndex := slices.IndexFunc(rendered, func(text string) bool { return strings.Contains(text, "redirect") })
	shortcutIndex := slices.IndexFunc(rendered, func(text string) bool { return strings.Contains(text, "alt+↑") })
	if len(model.entries) != 2 || model.entries[1].Text != "answer continues" || redirectIndex < 0 || shortcutIndex < 0 {
		t.Fatalf("blocks=%+v lines=%q", model.entries, rendered)
	}
	if frame := buildTerminalFrame(model); !frame.cursorVisible {
		t.Fatal("cursor hidden while agent is running")
	}

	applyEvent(model, agent.Event{Kind: agent.EventSteering, Content: testTextContent("redirect")})
	if len(model.queued) != 0 || len(model.entries) != 3 || model.entries[2].Kind != session.EntryUser || model.entries[2].Text != "redirect" {
		t.Fatalf("queued=%+v blocks=%+v", model.queued, model.entries)
	}
}

func TestPendingImageSteeringRendersAndDelivers(t *testing.T) {
	content := []agent.ContentPart{
		{Kind: agent.ContentPartText, Text: "describe "},
		{Kind: agent.ContentPartImage, Image: &agent.Image{MediaType: "image/png", Data: []byte("png")}},
	}
	model := newTestModel(40, 8, Options{})
	setQueue(model, content)

	lines := modelConversationLines(model, 40)
	if slices.IndexFunc(lines, func(line styledLine) bool { return strings.Contains(line.text, imageAttachmentLabel) }) < 0 {
		t.Fatalf("queued image lines = %+v", lines)
	}
	applyEvent(model, agent.Event{Kind: agent.EventSteering, Content: content})
	if len(model.queued) != 0 || len(model.entries) != 1 || !slices.Equal(model.entries[0].Content, []agent.ContentPart{{Kind: agent.ContentPartText, Text: "describe "}, {Kind: agent.ContentPartImage}}) {
		t.Fatalf("queued = %+v, blocks = %+v", model.queued, model.entries)
	}
}

func TestGoalContinuationHasDistinctTranscriptBlock(t *testing.T) {
	model := newTestModel(40, 8, Options{})
	appendUser(model, textContent("initial"))
	setRunning(model)
	streamAssistantText(model, "first response")
	setQueue(model, testTextContent("same text"))

	applyEvent(model, agent.Event{Kind: agent.EventGoalContinuation, Text: "same text"})
	if len(model.queued) != 1 || len(model.entries) != 3 || model.entries[2].Kind != session.EntryInfo {
		t.Fatalf("queued=%+v blocks=%+v", model.queued, model.entries)
	}
	streamAssistantText(model, "second response")
	if len(model.entries) != 4 || model.entries[3].Kind != session.EntryAssistant {
		t.Fatalf("goal continuation did not separate assistant streams: %+v", model.entries)
	}
}

func TestReasoningSummaryHasBalancedVerticalSpace(t *testing.T) {
	lines := conversationLines([]session.Entry{
		{Kind: session.EntryUser, Text: "user"},
		{Kind: session.EntryReasoning, Text: "\nsummary\n\n"},
		{Kind: session.EntryAssistant, Text: "answer"},
	}, 40)
	if len(lines) != 5 || lines[1].text != "" || lines[2].text != "summary" || lines[3].text != "" {
		t.Fatalf("lines = %+v", lines)
	}
	if !lines[2].style.italic || lines[2].style.paintBackground {
		t.Fatalf("reasoning style = %+v", lines[2].style)
	}
}

func TestRenderFrameSanitizesConversationText(t *testing.T) {
	model := newTestModel(40, 8, Options{})
	streamAssistantText(model, "safe\x1b[31m\rrewrite")
	frame := renderFrame(model)
	if strings.Contains(frame, "safe\x1b[31m") || !strings.Contains(frame, "safe�[31m�rewrite") {
		t.Fatalf("unsafe frame = %q", frame)
	}
}

func TestTUIModelCorrelatesAndSanitizesStreamingToolBlocks(t *testing.T) {
	model := newTestModel(80, 24, Options{})
	applyEvent(model, agent.Event{
		Kind: agent.EventToolStart, Call: agent.ToolCall{ID: "one", Name: "write"},
		Presentation: agent.ToolPresentation{Lines: []string{"partial"}},
	})
	applyEvent(model, agent.Event{
		Kind: agent.EventToolStart, Call: agent.ToolCall{ID: "two", Name: "launch_subagents"},
		Presentation: agent.ToolPresentation{Lines: []string{"running"}},
	})
	applyEvent(model, agent.Event{
		Kind: agent.EventToolUpdate, Call: agent.ToolCall{ID: "one", Name: "write"},
		Presentation: agent.ToolPresentation{
			Lines: []string{"safe\x1b[31m"},
			Diff:  []agent.ToolDiffLine{{Kind: agent.ToolDiffLineAdded, NewLine: 1, Text: "new\x1b[32m"}},
		},
	})
	applyEvent(model, agent.Event{
		Kind: agent.EventToolEnd, Call: agent.ToolCall{ID: "two", Name: "launch_subagents"},
		Presentation: agent.ToolPresentation{Lines: []string{"complete"}},
		Result:       agent.ToolResult{Tool: "launch_subagents"},
	})

	first := model.entries[0]
	second := model.entries[1]
	var rendered []string
	for _, line := range conversationBlockLines(first, 80) {
		rendered = append(rendered, renderedLineText(line, 80))
	}
	output := strings.Join(rendered, "\n")
	if first.Kind != session.EntryToolPending || strings.Contains(output, "\x1b") || !strings.Contains(output, "safe�[31m") || !strings.Contains(output, "new�[32m") {
		t.Fatalf("first block = %+v, rendered = %q", first, output)
	}
	if second.Kind != session.EntryTool || second.Tool.Lines[0] != "complete" {
		t.Fatalf("second block = %+v", second)
	}
	if model.activity.kind != activityTool || model.activity.detail != "write" {
		t.Fatalf("activity = %+v", model.activity)
	}
}

func TestCanceledTurnKeepsCancelingActivity(t *testing.T) {
	model := newTestModel(80, 24, Options{})
	model.activity = activity{kind: activityCanceling}
	applyEvent(model, agent.Event{Kind: agent.EventAssistantText, Text: "late text"})
	if model.activity.kind != activityCanceling {
		t.Fatalf("activity = %+v", model.activity)
	}
}

func TestTUIToolActivityOmitsArguments(t *testing.T) {
	model := newTestModel(80, 24, Options{})
	applyEvent(model, agent.Event{
		Kind: agent.EventToolStart, Call: agent.ToolCall{ID: "read-1", Name: "read"},
		Presentation: agent.ToolPresentation{Arguments: "first\n" + strings.Repeat("x", 1_000)},
	})
	block := sanitizeEntry(model.entries[0])
	if strings.Contains(block.Tool.Arguments, "\n") || len(block.Tool.Arguments) > maxToolPresentationSummaryBytes || model.activity.detail != "read" {
		t.Fatalf("presentation=%+v activity=%q", block.Tool, model.activity.detail)
	}
}

func TestConversationDividerIndicatesTextBelow(t *testing.T) {
	model := newTestModel(20, 8, Options{})
	for index := 0; index < 8; index++ {
		model.appendEntry(session.EntryInfo, strings.Repeat(string(rune('a'+index)), 20))
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
	if !strings.Contains(scrolledDivider, "↓ more (alt+↓)") {
		t.Fatalf("divider does not show jump shortcut: %q", scrolledDivider)
	}

	scrollConversation(model, 1, renderer.frame)
	_ = renderModel(&renderer, model)
	if renderer.frame.plainRows[ruleRow] != followingDivider {
		t.Fatalf("divider was not restored: got %q, want %q", renderer.frame.plainRows[ruleRow], followingDivider)
	}
}

func TestAltDownJumpsConversationToBottom(t *testing.T) {
	model := newTestModel(20, 8, Options{})
	for index := 0; index < 8; index++ {
		model.appendEntry(session.EntryInfo, strings.Repeat(string(rune('a'+index)), 20))
	}

	var renderer tuiRenderer
	_ = renderModel(&renderer, model)
	bottom := model.scrollTop
	scrollConversationBy(model, -bottom, renderer.frame)
	_ = renderModel(&renderer, model)
	if model.following || renderer.frame.conversationTop != 0 {
		t.Fatalf("conversation did not scroll to top: top=%d following=%t", renderer.frame.conversationTop, model.following)
	}

	if _, err := handleKeyInput(model, keys.Event{Code: keys.AltDown}, renderer.frame); err != nil {
		t.Fatal(err)
	}
	if model.scrollTop != bottom || !model.following {
		t.Fatalf("conversation did not jump to bottom: top=%d bottom=%d following=%t", model.scrollTop, bottom, model.following)
	}

	reduceMouse(model, keys.Mouse{Kind: keys.MouseWheelUp}, renderer.frame)
	if model.scrollTop != bottom-mouseWheelScrollLines {
		t.Fatalf("residual scroll jumped to stale viewport: top=%d bottom=%d", model.scrollTop, bottom)
	}
}

func TestConversationScrollingStopsAndResumesFollowing(t *testing.T) {
	model := newTestModel(20, 8, Options{})
	for index := 0; index < 8; index++ {
		model.appendEntry(session.EntryInfo, strings.Repeat(string(rune('a'+index)), 20))
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
	model.appendEntry(session.EntryInfo, "new output")
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
