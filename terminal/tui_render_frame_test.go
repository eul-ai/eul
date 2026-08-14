package terminal

import (
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
