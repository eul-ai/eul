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

func renderFrame(model *tuiModel) string {
	var renderer tuiRenderer
	return renderer.render(model)
}

func TestRenderFrameShowsRuledInputAndStatus(t *testing.T) {
	model := newTUIModel(72, 12, Options{
		Model: "gpt-5.6-sol", ThinkingLevel: agent.ThinkingXHigh, ContextWindow: 272_000,
	})
	model.contextTokens = 84_320
	model.appendBlock(blockUser, "hello")
	model.appendStream(blockAssistant, "answer")
	model.activity = activity{kind: activityThinking}

	frame := renderFrame(model)
	for _, want := range []string{
		"hello", "answer", "> ", "────────────────", "gpt-5.6-sol (xhigh)",
		"context 31%", "thinking",
	} {
		if !strings.Contains(frame, want) {
			t.Fatalf("frame omits %q:\n%q", want, frame)
		}
	}
	if strings.Contains(frame, "You") || strings.Contains(frame, "Assistant") {
		t.Fatalf("frame includes role labels: %q", frame)
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
	if strings.Contains(frame, "\x1b[48;2;23;27;36m") || !strings.Contains(frame, "\x1b[49m") {
		t.Fatalf("frame does not preserve the terminal background: %q", frame)
	}
	if !strings.HasPrefix(frame, ansiBeginSynchronizedOutput+ansiHideCursor) || !strings.HasSuffix(frame, ansiEndSynchronizedOutput) {
		t.Fatalf("frame does not use synchronized output: %q", frame)
	}
}

func TestStatusTruncatesSessionID(t *testing.T) {
	model := newTUIModel(120, 12, Options{
		Model: "model", SessionID: "0123456789abcdef0123456789abcdef",
	})

	_, status := renderStatus(model, model.width)
	want := "model (medium) · session 01234567 · context 0"
	if status != want {
		t.Fatalf("status = %q, want %q", status, want)
	}
}

func TestRunningSubagentsRenderAboveInput(t *testing.T) {
	started := time.Unix(100, 0)
	model := newTUIModel(100, 10, Options{})
	model.subagentStatus = agent.SubagentStatus{Jobs: []agent.SubagentJobStatus{
		{
			ID: "subagent-1", Task: "inspect layout", State: agent.SubagentRunning, Started: started,
			Usage: agent.Usage{InputTokens: 1_200, OutputTokens: 34}, Generations: 3, GenerationLimit: 20,
		},
		{
			ID: "subagent-2", Task: "review progress", State: agent.SubagentFinalizing, Started: started,
			Generations: 20, GenerationLimit: 20, FinalizationReason: agent.FinalizationReasonGenerations,
		},
	}}

	lines := renderSubagentsAt(model, 2, started.Add(time.Minute+5*time.Second))
	first := renderedLineText(lines[0], model.width)
	second := renderedLineText(lines[1], model.width)
	if !strings.Contains(first, "subagent-1  running (1m5s, 1.2k input, 34 output, 3/20 generations) — inspect layout") {
		t.Fatalf("running line = %q", first)
	}
	if !strings.Contains(second, "subagent-2  finalizing — generation limit (1m5s, 20/20 generations) — review progress") {
		t.Fatalf("finalizing line = %q", second)
	}

	_, layout := modelInputLayout(model)
	if layout.subagentHeight != 2 || layout.subagentRow != layout.conversationHeight+1 || layout.topRuleRow != layout.subagentRow+layout.subagentHeight {
		t.Fatalf("layout = %+v", layout)
	}
	frame := buildTerminalFrame(model)
	if !strings.Contains(frame.plainRows[layout.subagentRow-1], "subagent-1") || frame.cursorRow != layout.inputRow {
		t.Fatalf("frame layout=%+v rows=%q", layout, frame.plainRows)
	}
}

func TestSubagentPanelIsCappedOnSmallTerminals(t *testing.T) {
	model := newTUIModel(40, 8, Options{})
	model.subagentStatus.Jobs = make([]agent.SubagentJobStatus, 4)
	for index := range model.subagentStatus.Jobs {
		model.subagentStatus.Jobs[index] = agent.SubagentJobStatus{ID: "subagent-" + strconv.Itoa(index+1), State: agent.SubagentRunning}
	}

	_, layout := modelInputLayout(model)
	if layout.subagentHeight != 3 || layout.conversationHeight != 1 || layout.inputHeight != 1 || layout.statusRow != 8 {
		t.Fatalf("layout = %+v", layout)
	}
}

func TestStatusOmitsBackgroundSubagents(t *testing.T) {
	model := newTUIModel(120, 12, Options{Model: "model"})
	model.subagentStatus = agent.SubagentStatus{
		Running: 1,
		Jobs:    []agent.SubagentJobStatus{{ID: "subagent-1", State: agent.SubagentRunning}},
	}

	left, _ := renderStatus(model, model.width)
	if left != "ready" {
		t.Fatalf("ready status = %q", left)
	}

	model.activity = activity{kind: activityThinking}
	left, _ = renderStatus(model, model.width)
	if left != "⠋ thinking" {
		t.Fatalf("thinking status = %q", left)
	}
}

func TestStatusShowsProviderUsageWindows(t *testing.T) {
	now := time.Date(2027, time.January, 2, 10, 0, 0, 0, time.UTC)
	model := newTUIModel(180, 12, Options{Model: "model"})
	model.providerUsage = agent.ProviderUsage{Windows: []agent.UsageWindow{
		{Duration: 7 * 24 * time.Hour, UsedPercent: 20, ResetsAt: now.Add(3*24*time.Hour + 5*time.Hour)},
		{Duration: 5 * time.Hour, UsedPercent: 42, ResetsAt: now.Add(3*time.Hour + 5*time.Minute)},
	}}

	_, wide := renderStatusAt(model, 180, now)
	for _, want := range []string{"model (medium)", "context 0", "5h limit 58% (resets in 3h 5m) · 7d limit 80% (resets in 3d 5h)"} {
		if !strings.Contains(wide, want) {
			t.Fatalf("wide status %q omits %q", wide, want)
		}
	}

	_, narrow := renderStatusAt(model, 70, now)
	if narrow != "context 0 · 5h 58% (resets in 3h 5m) · 7d 80% (resets in 3d 5h)" {
		t.Fatalf("narrow status = %q", narrow)
	}
}

func TestStatusUsesCompactContextAndSingleLimit(t *testing.T) {
	now := time.Date(2027, time.January, 2, 10, 0, 0, 0, time.UTC)
	model := newTUIModel(120, 12, Options{
		Model: "gpt-5.6-sol", ThinkingLevel: agent.ThinkingXHigh, ContextWindow: 272_000,
	})
	model.providerUsage = agent.ProviderUsage{Windows: []agent.UsageWindow{{
		Duration: 7 * 24 * time.Hour, UsedPercent: 59, ResetsAt: now.Add(9*time.Hour + 41*time.Minute),
	}}}

	_, status := renderStatusAt(model, 120, now)
	want := "gpt-5.6-sol (xhigh) · context 0% · limit 41% (resets in 9h 41m)"
	if status != want {
		t.Fatalf("status = %q, want %q", status, want)
	}
}

func TestResetCountdownUsesTwoLargestUnits(t *testing.T) {
	now := time.Date(2027, time.January, 2, 10, 0, 0, 0, time.UTC)
	tests := []struct {
		remaining time.Duration
		want      string
	}{
		{remaining: 3*24*time.Hour + 5*time.Hour + 20*time.Minute, want: "3d 5h"},
		{remaining: 5*time.Hour + 12*time.Minute, want: "5h 12m"},
		{remaining: 30 * time.Second, want: "1m"},
		{remaining: -time.Second, want: "now"},
	}
	for _, test := range tests {
		if got := resetCountdown(now.Add(test.remaining), now); got != test.want {
			t.Fatalf("resetCountdown(%s) = %q, want %q", test.remaining, got, test.want)
		}
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
			Lines: []string{"1. complete — Read `./terminal/repl.go`"},
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
		case "1. complete — Read ./terminal/repl.go":
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
	if strings.Contains(detail.text, "`") || !strings.Contains(renderedDetail.String(), ansiForeground(currentTheme.markdownCode)+"./terminal/repl.go"+ansiForeground(currentTheme.foreground)) {
		t.Fatalf("tool detail = %q", renderedDetail.String())
	}
}

func TestToolTruncationMarkerIsMuted(t *testing.T) {
	lines := conversationLines([]conversationBlock{{
		kind: blockTool,
		tool: agent.ToolPresentation{
			Title:          "write",
			Arguments:      "tool/subagent.go",
			Lines:          []string{"package tool", "… (235 more lines, 245 total)"},
			LinesTruncated: true,
		},
	}}, 80)

	for _, line := range lines {
		if line.text == "… (235 more lines, 245 total)" {
			if line.style.foreground != currentTheme.muted {
				t.Fatalf("truncation style = %+v", line.style)
			}
			return
		}
	}
	t.Fatalf("tool lines = %+v", lines)
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
	for _, want := range []string{"bash go test ./... — exit status: 0", "... (3 earlier lines)", "ok github.com/eul-ai/eul/cmd", "ok github.com/eul-ai/eul/terminal race", "Took 2.9s (120s timeout)"} {
		if !slices.Contains(texts, want) {
			t.Fatalf("lines = %+v, missing %q", lines, want)
		}
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
	if !slices.Contains(texts, "running") || !slices.Contains(texts, "Elapsed 1.2s") || slices.Contains(texts, "Took 1.2s") {
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
	foundOmission := false
	for _, line := range lines {
		if line.text == "... (+5)" {
			foundOmission = true
		}
		if line.text == strings.Repeat("x", 10) {
			bodyLines++
		}
	}
	if !foundOmission || bodyLines != 5 {
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

func TestMultilineInputExpandsEditorAndMovesCursor(t *testing.T) {
	model := newTUIModel(20, 8, Options{})
	if err := model.insertInput("first"); err != nil {
		t.Fatal(err)
	}
	if err := model.insertNewline(); err != nil {
		t.Fatal(err)
	}
	if err := model.insertInput("second"); err != nil {
		t.Fatal(err)
	}

	input := renderInput(model, model.width, maximumInputHeight(model.height, 0))
	if len(input.lines) != 2 || input.lines[0] != "> first" || input.lines[1] != "  second" {
		t.Fatalf("input = %+v", input)
	}
	if input.cursorRow != 1 || input.cursorColumn != 9 {
		t.Fatalf("cursor = %d,%d", input.cursorRow, input.cursorColumn)
	}
	layout := calculateLayout(model.height, len(input.lines), 0, 0)
	if layout.conversationHeight != 3 || layout.inputRow != 5 || layout.inputHeight != 2 {
		t.Fatalf("layout = %+v", layout)
	}
	frame := buildTerminalFrame(model)
	if frame.cursorRow != 6 || frame.cursorColumn != 9 {
		t.Fatalf("frame cursor = %d,%d", frame.cursorRow, frame.cursorColumn)
	}
}

func TestFilePickerExpandsBetweenInputAndStatus(t *testing.T) {
	model := newTUIModel(30, 12, Options{WorkingDirectory: t.TempDir()})
	if err := model.insertInput("@"); err != nil {
		t.Fatal(err)
	}
	request := takePickerRequest(t, model)
	model.applyFileSearchResult(fileSearchResult{id: request.id, paths: []string{"a.go", "b.go", "c.go", "d.go", "e.go", "f.go"}})

	_, layout := modelInputLayout(model)
	if layout.conversationHeight != 3 || layout.inputRow != 5 || layout.bottomRuleRow != 6 || layout.pickerRow != 7 || layout.pickerHeight != 5 || layout.statusRow != 12 {
		t.Fatalf("layout = %+v", layout)
	}
	picker := renderFilePicker(model, layout.pickerHeight)
	if len(picker) != 5 || picker[0].prefixText != "> " || !picker[0].style.paintBackground || picker[0].style.background != currentTheme.selectedBackground {
		t.Fatalf("picker = %+v", picker)
	}

	frame := buildTerminalFrame(model)
	if frame.cursorRow != layout.inputRow || !strings.Contains(frame.rows[layout.pickerRow-1], "a.go") || layout.statusRow != frame.height {
		t.Fatalf("frame cursor=%d layout=%+v rows=%q", frame.cursorRow, layout, frame.rows)
	}
}

func TestFilePickerShowsLoadingRow(t *testing.T) {
	model := newTUIModel(30, 8, Options{WorkingDirectory: t.TempDir()})
	if err := model.insertInput("@"); err != nil {
		t.Fatal(err)
	}
	lines := renderFilePicker(model, model.filePickerHeight())
	if len(lines) != 1 || lines[0].text != "  searching files…" {
		t.Fatalf("loading picker = %+v", lines)
	}
}

func TestFilePickerKeepsStableHeightWhileSearching(t *testing.T) {
	model := newTUIModel(30, 12, Options{WorkingDirectory: t.TempDir()})
	if err := model.insertInput("@"); err != nil {
		t.Fatal(err)
	}
	request := takePickerRequest(t, model)
	height := model.filePickerHeight()
	model.applyFileSearchResult(fileSearchResult{id: request.id, paths: []string{"a.go", "b.go"}})
	if model.filePickerHeight() != height {
		t.Fatalf("result height = %d, want %d", model.filePickerHeight(), height)
	}

	if err := model.insertInput("missing"); err != nil {
		t.Fatal(err)
	}
	request = takePickerRequest(t, model)
	model.applyFileSearchResult(fileSearchResult{id: request.id})
	lines := renderFilePicker(model, model.filePickerHeight())
	if !model.filePickerVisible() || model.filePickerHeight() != height || len(lines) != 1 || lines[0].text != "  no matching files" {
		t.Fatalf("empty picker: visible=%t height=%d lines=%+v", model.filePickerVisible(), model.filePickerHeight(), lines)
	}
}

func TestInputPreservesBlankPastedLines(t *testing.T) {
	model := newTUIModel(20, 8, Options{})
	if err := model.insertInput("abc\n\ndef"); err != nil {
		t.Fatal(err)
	}

	input := renderInput(model, model.width, maximumInputHeight(model.height, 0))
	if len(input.lines) != 3 || input.lines[0] != "> abc" || input.lines[1] != "  " || input.lines[2] != "  def" {
		t.Fatalf("input = %+v", input)
	}
}

func TestInputWrapsAndKeepsCursorVisible(t *testing.T) {
	model := newTUIModel(8, 6, Options{})
	if err := model.insertInput("1234567"); err != nil {
		t.Fatal(err)
	}
	input := renderInput(model, model.width, 2)
	if len(input.lines) != 2 || input.lines[0] != "> 123456" || input.lines[1] != "  7" {
		t.Fatalf("input = %+v", input)
	}
	if input.cursorRow != 1 || input.cursorColumn != 4 {
		t.Fatalf("cursor = %d,%d", input.cursorRow, input.cursorColumn)
	}
}

func TestInputWrapsAtWordBoundaries(t *testing.T) {
	model := newTUIModel(10, 6, Options{})
	if err := model.insertInput("hello world"); err != nil {
		t.Fatal(err)
	}

	input := renderInput(model, model.width, 2)
	if len(input.lines) != 2 || input.lines[0] != "> hello " || input.lines[1] != "  world" {
		t.Fatalf("input = %+v", input)
	}
	if input.cursorRow != 1 || input.cursorColumn != 8 {
		t.Fatalf("cursor = %d,%d", input.cursorRow, input.cursorColumn)
	}
}

func TestPaddedBlockBackgroundFillsWidth(t *testing.T) {
	style := blockPresentation(blockTool)
	var frame strings.Builder
	renderLine(&frame, 1, 6, styledLine{text: "x", style: style, padding: conversationPadding})

	want := ansiColors(style.foreground, style.background, true) + strings.Repeat(" ", conversationPadding) + "x" + strings.Repeat(" ", 6-conversationPadding-1) + ansiReset
	if !strings.Contains(frame.String(), want) {
		t.Fatalf("line = %q, want full-width background sequence %q", frame.String(), want)
	}
	if strings.Count(frame.String(), "\x1b[1;1H") != 1 {
		t.Fatalf("line was painted more than once: %q", frame.String())
	}
}

func TestPendingSteeringRendersAndDeliversInTranscriptOrder(t *testing.T) {
	model := newTUIModel(40, 8, Options{})
	model.beginTurn("initial")
	model.appendStream(blockAssistant, "answer")
	model.queueSteering("redirect")
	model.appendStream(blockAssistant, " continues")

	lines := modelConversationLines(model, 40)
	var rendered []string
	for _, line := range lines {
		rendered = append(rendered, line.text)
	}
	if len(model.blocks) != 2 || model.blocks[1].text != "answer continues" || !slices.Contains(rendered, "Queued: redirect") {
		t.Fatalf("blocks=%+v lines=%q", model.blocks, rendered)
	}
	if frame := buildTerminalFrame(model); !frame.cursorVisible {
		t.Fatal("cursor hidden while agent is running")
	}

	model.applyAgentEvent(agent.Event{Kind: agent.EventSteering, Text: "redirect"})
	if len(model.steering) != 0 || len(model.blocks) != 3 || model.blocks[2].kind != blockUser || model.blocks[2].text != "redirect" {
		t.Fatalf("steering=%q blocks=%+v", model.steering, model.blocks)
	}
}

func TestGoalContinuationHasDistinctTranscriptBlock(t *testing.T) {
	model := newTUIModel(40, 8, Options{})
	model.beginTurn("initial")
	model.appendStream(blockAssistant, "first response")
	model.queueSteering("same text")

	model.applyAgentEvent(agent.Event{Kind: agent.EventGoalContinuation, Text: "same text"})
	if len(model.steering) != 1 || len(model.blocks) != 3 || model.blocks[2].kind != blockInfo || model.blocks[2].text != "Goal continuing" {
		t.Fatalf("steering=%q blocks=%+v", model.steering, model.blocks)
	}
	model.appendStream(blockAssistant, "second response")
	if len(model.blocks) != 4 || model.blocks[3].kind != blockAssistant {
		t.Fatalf("goal continuation did not separate assistant streams: %+v", model.blocks)
	}
}

func TestRendererOnlyWritesChangedRows(t *testing.T) {
	model := newTUIModel(40, 8, Options{Model: "model"})
	model.running = true
	model.activity = activity{kind: activityThinking}
	var renderer tuiRenderer

	first := renderer.render(model)
	for row := 1; row <= model.height; row++ {
		position := "\x1b[" + strconv.Itoa(row) + ";1H"
		if !strings.Contains(first, position) {
			t.Fatalf("initial frame omits row %d: %q", row, first)
		}
	}
	if unchanged := renderer.render(model); unchanged != "" {
		t.Fatalf("unchanged frame = %q", unchanged)
	}

	model.spinner++
	update := renderer.render(model)
	statusPosition := "\x1b[8;1H"
	if !strings.Contains(update, statusPosition) || strings.Count(update, "\x1b[8;") != 1 {
		t.Fatalf("spinner update does not paint the status row once: %q", update)
	}
	for row := 1; row < model.height; row++ {
		position := "\x1b[" + strconv.Itoa(row) + ";1H"
		if strings.Contains(update, position) {
			t.Fatalf("spinner update repaints row %d: %q", row, update)
		}
	}
}

func TestRendererUsesScrollRegionForSingleConversationRow(t *testing.T) {
	model := newTUIModel(20, 8, Options{})
	model.appendBlock(blockAssistant, "one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\nnine\nten")
	var renderer tuiRenderer
	_ = renderer.render(model)

	height := renderer.frame.layout.conversationHeight
	region := "\x1b[1;" + strconv.Itoa(height) + "r"
	scrollConversationBy(model, -1, renderer.frame)
	up := renderer.render(model)
	if !strings.Contains(up, region+ansiScrollDown+ansiResetScrollRegion) {
		t.Fatalf("scroll up did not shift the terminal region: %q", up)
	}
	if !strings.Contains(up, "\x1b[1;1H") {
		t.Fatalf("scroll up did not paint the exposed top row: %q", up)
	}
	for row := 2; row <= height; row++ {
		position := "\x1b[" + strconv.Itoa(row) + ";1H"
		if strings.Contains(up, position) {
			t.Fatalf("scroll up repainted retained row %d: %q", row, up)
		}
	}

	scrollConversationBy(model, 1, renderer.frame)
	down := renderer.render(model)
	if !strings.Contains(down, region+ansiScrollUp+ansiResetScrollRegion) {
		t.Fatalf("scroll down did not shift the terminal region: %q", down)
	}
	bottomPosition := "\x1b[" + strconv.Itoa(height) + ";1H"
	if !strings.Contains(down, bottomPosition) {
		t.Fatalf("scroll down did not paint the exposed bottom row: %q", down)
	}
	for row := 1; row < height; row++ {
		position := "\x1b[" + strconv.Itoa(row) + ";1H"
		if strings.Contains(down, position) {
			t.Fatalf("scroll down repainted retained row %d: %q", row, down)
		}
	}
}

func TestRendererDoesNotScrollRegionWhenConversationChanges(t *testing.T) {
	model := newTUIModel(20, 8, Options{})
	model.appendBlock(blockAssistant, "one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\nnine\nten")
	var renderer tuiRenderer
	_ = renderer.render(model)

	scrollConversationBy(model, -1, renderer.frame)
	model.appendBlock(blockInfo, "dynamic output")
	update := renderer.render(model)
	if strings.Contains(update, ansiScrollUp) || strings.Contains(update, ansiScrollDown) {
		t.Fatalf("dynamic conversation update used terminal scrolling: %q", update)
	}
}

func TestRendererForcesFullRedrawAfterResizeOrCtrlL(t *testing.T) {
	model := newTUIModel(20, 8, Options{})
	var renderer tuiRenderer
	_ = renderer.render(model)

	model.width++
	resized := renderer.render(model)
	if !strings.Contains(resized, ansiClearScreen) {
		t.Fatalf("resize did not clear the screen: %q", resized)
	}

	forced, next := renderer.renderPending(model, true)
	renderer.commit(next)
	if !strings.Contains(forced, ansiClearScreen) {
		t.Fatalf("forced redraw = %q", forced)
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
	model := newTUIModel(80, 8, Options{Model: "safe\x1b[31m", ThinkingLevel: agent.ThinkingLevel("high\a")})
	left, right := renderStatus(model, 80)
	status := left + right
	if strings.ContainsAny(status, "\x1b\a") || !strings.Contains(right, "safe [31m (high)") {
		t.Fatalf("status = %q / %q", left, right)
	}
}

func TestRenderStatusPrioritizesActivityAndContext(t *testing.T) {
	model := newTUIModel(80, 20, Options{Model: "very-long-model", ThinkingLevel: agent.ThinkingMax, ContextWindow: 100})
	model.contextTokens = 50
	model.activity = activity{kind: activityCompacting}

	wideLeft, wideRight := renderStatus(model, 80)
	if wideLeft != "⠋ compacting context" || wideRight != "very-long-model (max) · context 50%" {
		t.Fatalf("wide status = %q / %q", wideLeft, wideRight)
	}
	narrowLeft, narrowRight := renderStatus(model, 33)
	if strings.Contains(narrowRight, "very-long-model") || narrowLeft != "⠋ compacting context" || narrowRight != "context 50%" {
		t.Fatalf("narrow status = %q / %q", narrowLeft, narrowRight)
	}
}

func TestTUIModelShowsGenerationRetries(t *testing.T) {
	model := newTUIModel(80, 24, Options{})
	model.applyAgentEvent(agent.Event{Kind: agent.EventGenerationRetry, Attempt: 2})

	left, _ := renderStatus(model, 80)
	if model.activity.kind != activityRetrying || left != "⠋ retrying response (attempt 2)" {
		t.Fatalf("activity = %+v, status = %q", model.activity, left)
	}
}

func TestTUIModelTracksActivityAndContext(t *testing.T) {
	model := newTUIModel(80, 24, Options{})
	model.contextTokens = 100
	model.applyAgentEvent(agent.Event{Kind: agent.EventCompactionStart})
	if model.activity.kind != activityCompacting {
		t.Fatalf("activity = %+v", model.activity)
	}
	model.applyAgentEvent(agent.Event{Kind: agent.EventCompactionEnd})
	if model.activity.kind != activityThinking || model.contextTokens != 0 {
		t.Fatalf("activity = %+v, context = %d", model.activity, model.contextTokens)
	}
	model.applyAgentEvent(agent.Event{Kind: agent.EventContextUsage, Usage: agent.Usage{TotalTokens: 123}})
	if model.contextTokens != 123 {
		t.Fatalf("context tokens = %d", model.contextTokens)
	}
	model.applyAgentEvent(agent.Event{Kind: agent.EventToolStart, Call: agent.ToolCall{ID: "call-1", Name: "read"}, Presentation: agent.ToolPresentation{Title: "read file.go"}})
	if model.activity.kind != activityTool || !strings.Contains(model.activity.detail, "file.go") || model.blocks[len(model.blocks)-1].kind != blockToolPending {
		t.Fatalf("activity = %+v, blocks = %+v", model.activity, model.blocks)
	}
	toolBlocks := len(model.blocks)
	model.applyAgentEvent(agent.Event{Kind: agent.EventToolEnd, Call: agent.ToolCall{ID: "call-1", Name: "read"}, Presentation: agent.ToolPresentation{Title: "read file.go"}, Result: agent.ToolResult{Tool: "read", IsError: true, Output: "failed"}})
	last := model.blocks[len(model.blocks)-1]
	if model.activity.kind != activityThinking || len(model.blocks) != toolBlocks || last.kind != blockToolError || last.toolOutcome != "error: failed" {
		t.Fatalf("activity = %+v, blocks = %+v", model.activity, model.blocks)
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
	if second.kind != blockTool || second.tool.Lines[0] != "complete" || second.toolOutcome != "ok" {
		t.Fatalf("second block = %+v", second)
	}
	if model.activity.kind != activityTool || model.activity.detail != "write one" {
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

func TestTUIBashActivityOmitsCommand(t *testing.T) {
	model := newTUIModel(80, 24, Options{})
	model.applyAgentEvent(agent.Event{
		Kind: agent.EventToolStart, Call: agent.ToolCall{ID: "bash-1", Name: "bash"},
		Presentation: agent.ToolPresentation{Title: "bash", Arguments: "first\n" + strings.Repeat("x", 1_000)},
	})
	block := model.blocks[model.toolBlockIndex("bash-1")]
	if strings.Contains(block.tool.Arguments, "\n") || len(block.tool.Arguments) > maxToolPresentationSummaryBytes || model.activity.detail != "bash" || toolActivityDetail(agent.ToolCall{}, block.tool) != "bash" {
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
	if block.kind != blockToolError || block.toolOutcome != "canceled" {
		t.Fatalf("block = %+v", block)
	}
}

func TestConversationDividerIndicatesTextBelow(t *testing.T) {
	model := newTUIModel(20, 8, Options{})
	for index := 0; index < 8; index++ {
		model.appendBlock(blockInfo, strings.Repeat(string(rune('a'+index)), 20))
	}

	var renderer tuiRenderer
	_ = renderer.render(model)
	ruleRow := renderer.frame.layout.topRuleRow - 1
	if strings.Contains(renderer.frame.plainRows[ruleRow], "↓ more") {
		t.Fatalf("divider shows text below while following: %q", renderer.frame.plainRows[ruleRow])
	}

	scrollConversation(model, -1, renderer.frame)
	_ = renderer.render(model)
	if renderer.frame.plainRows[ruleRow] != "───────↓ more───────" {
		t.Fatalf("scrolled divider = %q", renderer.frame.plainRows[ruleRow])
	}

	scrollConversation(model, 1, renderer.frame)
	_ = renderer.render(model)
	if strings.Contains(renderer.frame.plainRows[ruleRow], "↓ more") {
		t.Fatalf("divider still shows text below at bottom: %q", renderer.frame.plainRows[ruleRow])
	}
}

func TestConversationScrollingStopsAndResumesFollowing(t *testing.T) {
	model := newTUIModel(20, 8, Options{})
	for index := 0; index < 8; index++ {
		model.appendBlock(blockInfo, strings.Repeat(string(rune('a'+index)), 20))
	}
	var renderer tuiRenderer
	_ = renderer.render(model)
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
	_ = renderer.render(model)
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

func TestRenderFrameHandlesTinyDimensions(t *testing.T) {
	for _, size := range [][2]int{{1, 1}, {2, 2}, {3, 3}, {4, 4}} {
		model := newTUIModel(size[0], size[1], Options{Model: "model", ThinkingLevel: agent.ThinkingHigh})
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
