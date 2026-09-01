package terminal

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/eul-ai/eul/agent"
)

func TestRendererOnlyWritesChangedRows(t *testing.T) {
	model := newTUIModel(40, 8, Options{Config: Config{Model: "model"}})
	model.running = true
	model.activity = activity{kind: activityThinking}
	var renderer tuiRenderer

	first := renderModel(&renderer, model)
	for row := 1; row <= model.height; row++ {
		position := "\x1b[" + strconv.Itoa(row) + ";1H"
		if !strings.Contains(first, position) {
			t.Fatalf("initial frame omits row %d: %q", row, first)
		}
	}
	if unchanged := renderModel(&renderer, model); unchanged != "" {
		t.Fatalf("unchanged frame = %q", unchanged)
	}

	model.spinner++
	update := renderModel(&renderer, model)
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
	_ = renderModel(&renderer, model)

	height := renderer.frame.layout.conversationHeight
	region := "\x1b[1;" + strconv.Itoa(height) + "r"
	scrollConversationBy(model, -1, renderer.frame)
	up := renderModel(&renderer, model)
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
	down := renderModel(&renderer, model)
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
	_ = renderModel(&renderer, model)

	scrollConversationBy(model, -1, renderer.frame)
	model.appendBlock(blockInfo, "dynamic output")
	update := renderModel(&renderer, model)
	if strings.Contains(update, ansiScrollUp) || strings.Contains(update, ansiScrollDown) {
		t.Fatalf("dynamic conversation update used terminal scrolling: %q", update)
	}
}

func TestRendererForcesFullRedrawAfterResizeOrCtrlL(t *testing.T) {
	model := newTUIModel(20, 8, Options{})
	var renderer tuiRenderer
	_ = renderModel(&renderer, model)

	model.width++
	resized := renderModel(&renderer, model)
	if !strings.Contains(resized, ansiClearScreen) {
		t.Fatalf("resize did not clear the screen: %q", resized)
	}

	forced, next := renderer.renderPending(model, true)
	renderer.commit(next)
	if !strings.Contains(forced, ansiClearScreen) {
		t.Fatalf("forced redraw = %q", forced)
	}
}

func TestRendererLimitsHistoryAfterWidthResize(t *testing.T) {
	model := newTUIModel(40, 8, Options{})
	for index := range 300 {
		model.appendBlock(blockInfo, fmt.Sprintf("history %03d", index))
	}
	var renderer tuiRenderer
	_ = renderModel(&renderer, model)
	if renderer.conversationBlockStart != 0 {
		t.Fatalf("initial conversation starts at block %d", renderer.conversationBlockStart)
	}

	model.width = 30
	_ = renderModel(&renderer, model)
	if renderer.conversationBlockStart == 0 || !renderer.frame.conversationTruncated {
		t.Fatalf("resized conversation starts at block %d, truncated=%t", renderer.conversationBlockStart, renderer.frame.conversationTruncated)
	}
	conversationHeight := renderer.frame.layout.conversationHeight
	if len(renderer.conversationLines) < conversationHeight+resizeHistoryRows {
		t.Fatalf("resized conversation has %d cached rows", len(renderer.conversationLines))
	}
	full := modelConversationLines(model, model.width)
	fullTop := max(0, len(full)-conversationHeight)
	wantViewport := conversationViewport(full, fullTop, conversationHeight)
	wantPlain := make([]string, len(wantViewport))
	for index, line := range wantViewport {
		wantPlain[index] = renderedLineText(line, model.width)
	}
	if !slices.Equal(renderer.frame.plainRows[:conversationHeight], wantPlain) {
		t.Fatalf("resized viewport differs from full render:\ngot:  %q\nwant: %q", renderer.frame.plainRows[:conversationHeight], wantPlain)
	}
	conversation := strings.Join(renderer.frame.conversationLines, "\n")
	if strings.Contains(conversation, "history 000") || !strings.Contains(conversation, "history 299") {
		t.Fatalf("resized conversation cached the wrong history: %q", conversation)
	}
}

func TestRendererLoadsTruncatedResizeHistoryWhenScrollingPastTop(t *testing.T) {
	model := newTUIModel(40, 8, Options{})
	for index := range 300 {
		model.appendBlock(blockInfo, fmt.Sprintf("history %03d", index))
	}
	var renderer tuiRenderer
	_ = renderModel(&renderer, model)
	model.width = 30
	_ = renderModel(&renderer, model)

	partialTop := slices.Clone(renderer.conversationPlain[:renderer.frame.layout.conversationHeight])
	scrollConversationBy(model, -model.scrollTop, renderer.frame)
	if !model.historyExpansionRequested {
		t.Fatal("scrolling past cached history did not request expansion")
	}
	_ = renderModel(&renderer, model)

	if renderer.conversationBlockStart != 0 || renderer.frame.conversationTruncated || model.historyExpansionRequested {
		t.Fatalf("expanded conversation starts at block %d, truncated=%t requested=%t", renderer.conversationBlockStart, renderer.frame.conversationTruncated, model.historyExpansionRequested)
	}
	if !slices.Equal(renderer.frame.plainRows[:renderer.frame.layout.conversationHeight], partialTop) {
		t.Fatalf("viewport moved while expanding history:\ngot:  %q\nwant: %q", renderer.frame.plainRows[:renderer.frame.layout.conversationHeight], partialTop)
	}
	if !strings.Contains(strings.Join(renderer.frame.conversationLines, "\n"), "history 000") {
		t.Fatal("expanded conversation does not contain oldest history")
	}
}

func TestRendererPreservesViewportWhenHeightExtendsResizeHistory(t *testing.T) {
	model := newTUIModel(40, 8, Options{})
	for index := range 400 {
		model.appendBlock(blockInfo, fmt.Sprintf("history %03d", index))
	}
	var renderer tuiRenderer
	_ = renderModel(&renderer, model)
	model.width = 30
	_ = renderModel(&renderer, model)

	scrollConversationBy(model, -100, renderer.frame)
	_ = renderModel(&renderer, model)
	oldTop := renderer.frame.plainRows[0]
	oldBlockStart := renderer.conversationBlockStart
	model.height = 100
	_ = renderModel(&renderer, model)
	if renderer.conversationBlockStart >= oldBlockStart {
		t.Fatalf("height resize did not extend cached history: start=%d, old=%d", renderer.conversationBlockStart, oldBlockStart)
	}
	if renderer.frame.plainRows[0] != oldTop {
		t.Fatalf("height resize moved viewport from %q to %q", oldTop, renderer.frame.plainRows[0])
	}
}

func TestRendererReflowsFullHistoryWhenResizeHistoryIsScrolled(t *testing.T) {
	model := newTUIModel(40, 8, Options{})
	for index := range 300 {
		model.appendBlock(blockInfo, fmt.Sprintf("history %03d", index))
	}
	var renderer tuiRenderer
	_ = renderModel(&renderer, model)
	model.width = 35
	_ = renderModel(&renderer, model)

	scrollConversationBy(model, -20, renderer.frame)
	_ = renderModel(&renderer, model)
	oldBottom := len(renderer.frame.conversationLines) - renderer.frame.layout.conversationHeight
	oldDistanceFromBottom := oldBottom - renderer.frame.conversationTop
	model.width = 30
	_ = renderModel(&renderer, model)
	if renderer.conversationBlockStart != 0 || renderer.frame.conversationTruncated {
		t.Fatalf("scrolled conversation starts at block %d, truncated=%t", renderer.conversationBlockStart, renderer.frame.conversationTruncated)
	}
	newBottom := len(renderer.frame.conversationLines) - renderer.frame.layout.conversationHeight
	if distance := newBottom - renderer.frame.conversationTop; distance != oldDistanceFromBottom {
		t.Fatalf("distance from bottom = %d, want %d", distance, oldDistanceFromBottom)
	}
}

func TestRenderFrameHandlesTinyDimensions(t *testing.T) {
	for _, size := range [][2]int{{1, 1}, {2, 2}, {3, 3}, {4, 4}} {
		model := newTUIModel(size[0], size[1], Options{Config: Config{Model: "model", ThinkingLevel: agent.ThinkingHigh}})
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
