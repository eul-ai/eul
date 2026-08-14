package terminal

import (
	"strings"
	"testing"

	"github.com/eul-ai/eul/agent"
)

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
	model := newTUIModel(30, 12, Options{Config: Config{WorkingDirectory: t.TempDir()}})
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
	model := newTUIModel(30, 8, Options{Config: Config{WorkingDirectory: t.TempDir()}})
	if err := model.insertInput("@"); err != nil {
		t.Fatal(err)
	}
	lines := renderFilePicker(model, model.filePickerHeight())
	if len(lines) != 1 || lines[0].style.foreground != currentTheme.muted {
		t.Fatalf("loading picker = %+v", lines)
	}
}

func TestFilePickerKeepsStableHeightWhileSearching(t *testing.T) {
	model := newTUIModel(30, 12, Options{Config: Config{WorkingDirectory: t.TempDir()}})
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
	if !model.filePickerVisible() || model.filePickerHeight() != height || len(lines) != 1 || lines[0].style.foreground != currentTheme.muted {
		t.Fatalf("empty picker: visible=%t height=%d lines=%+v", model.filePickerVisible(), model.filePickerHeight(), lines)
	}
}

func TestInputRendersImagesInline(t *testing.T) {
	model := newTUIModel(120, 8, Options{})
	if err := model.insertInput("Hey checkout this image: "); err != nil {
		t.Fatal(err)
	}
	if err := model.attachImage(agent.Image{MediaType: "image/png", Data: []byte("one")}); err != nil {
		t.Fatal(err)
	}
	if err := model.insertInput(". Compare it to this: "); err != nil {
		t.Fatal(err)
	}
	if err := model.attachImage(agent.Image{MediaType: "image/png", Data: []byte("two")}); err != nil {
		t.Fatal(err)
	}

	input := renderInput(model, model.width, maximumInputHeight(model.height, 0))
	firstText := "Hey checkout this image: "
	secondText := ". Compare it to this: "
	if len(input.lines) != 1 || !strings.Contains(input.lines[0], firstText) || strings.Index(input.lines[0], secondText) <= strings.Index(input.lines[0], firstText) {
		t.Fatalf("input = %+v", input)
	}
	if input.cursorColumn != cellWidth(input.lines[0])+1 {
		t.Fatalf("cursor = %d", input.cursorColumn)
	}
}

func TestInputWrapsInlineImageAsUnit(t *testing.T) {
	model := newTUIModel(20, 8, Options{})
	if err := model.insertInput("12345"); err != nil {
		t.Fatal(err)
	}
	if err := model.attachImage(agent.Image{MediaType: "image/png", Data: []byte("png")}); err != nil {
		t.Fatal(err)
	}
	input := renderInput(model, model.width, maximumInputHeight(model.height, 0))
	if len(input.lines) != 2 || input.lines[0] != "> 12345" || strings.TrimSpace(input.lines[1]) == "" {
		t.Fatalf("input = %+v", input)
	}
	if input.cursorRow != 1 || input.cursorColumn != cellWidth(input.lines[1])+1 {
		t.Fatalf("cursor = %d,%d", input.cursorRow, input.cursorColumn)
	}
}

func TestInputKeepsNarrowImageCursorPositionsDistinct(t *testing.T) {
	model := newTUIModel(3, 8, Options{})
	if err := model.attachImage(agent.Image{MediaType: "image/png", Data: []byte("png")}); err != nil {
		t.Fatal(err)
	}

	after := renderInput(model, model.width, maximumInputHeight(model.height, 0))
	model.moveLeft()
	before := renderInput(model, model.width, maximumInputHeight(model.height, 0))
	if len(after.lines) == 0 {
		t.Fatalf("after = %+v", after)
	}
	if before.cursorRow == after.cursorRow && before.cursorColumn == after.cursorColumn {
		t.Fatalf("before = %d,%d after = %d,%d", before.cursorRow, before.cursorColumn, after.cursorRow, after.cursorColumn)
	}
}

func TestSubmittedImageMarkerWrapsAtomically(t *testing.T) {
	content := []agent.ContentPart{
		{Kind: agent.ContentPartText, Text: "12345"},
		{Kind: agent.ContentPartImage, Image: &agent.Image{MediaType: "image/png"}},
	}
	lines := conversationBlockLines(conversationBlock{kind: blockUser, content: content}, 20)
	if len(lines) != 2 || lines[0].text != "12345" || lines[1].text == "" {
		t.Fatalf("lines = %+v", lines)
	}
}

func TestInputCursorMovesAcrossInlineImage(t *testing.T) {
	model := newTUIModel(40, 8, Options{})
	if err := model.attachImage(agent.Image{MediaType: "image/png", Data: []byte("png")}); err != nil {
		t.Fatal(err)
	}
	after := renderInput(model, model.width, maximumInputHeight(model.height, 0))
	model.moveLeft()
	before := renderInput(model, model.width, maximumInputHeight(model.height, 0))
	if before.cursorRow != after.cursorRow || before.cursorColumn >= after.cursorColumn {
		t.Fatalf("before = %d,%d after = %d,%d", before.cursorRow, before.cursorColumn, after.cursorRow, after.cursorColumn)
	}
}

func TestSubmittedImagesRenderInline(t *testing.T) {
	content := []agent.ContentPart{
		{Kind: agent.ContentPartText, Text: "before "},
		{Kind: agent.ContentPartImage, Image: &agent.Image{MediaType: "image/png"}},
		{Kind: agent.ContentPartText, Text: " after"},
	}
	lines := conversationBlockLines(conversationBlock{kind: blockUser, content: content}, 80)
	if len(lines) != 1 || !strings.Contains(lines[0].text, "before ") || !strings.Contains(lines[0].text, " after") || strings.Index(lines[0].text, "before ") >= strings.Index(lines[0].text, " after") {
		t.Fatalf("lines = %+v", lines)
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

func TestInputWrapsCursorAfterExactWidthText(t *testing.T) {
	model := newTUIModel(8, 6, Options{})
	if err := model.insertInput("123456"); err != nil {
		t.Fatal(err)
	}

	input := renderInput(model, model.width, 2)
	if len(input.lines) != 2 || input.lines[0] != "> 123456" || input.lines[1] != "  " {
		t.Fatalf("input = %+v", input)
	}
	if input.cursorRow != 1 || input.cursorColumn != 3 {
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
