package terminal

import (
	"slices"
	"testing"

	"github.com/eul-ai/eul/agent"
)

func testTextContent(text string) []agent.ContentPart {
	return []agent.ContentPart{{Kind: agent.ContentPartText, Text: text}}
}

func steeringTexts(messages [][]agent.ContentPart) []string {
	texts := make([]string, len(messages))
	for index, content := range messages {
		texts[index] = contentText(content)
	}
	return texts
}

func TestSteeringCoordinatorTracksAcceptedAndDeferredMessages(t *testing.T) {
	var engineQueue [][]agent.ContentPart
	engine := &fakeEngine{
		steerFunction: func(content []agent.ContentPart) bool {
			if contentText(content) == "accepted" {
				engineQueue = append(engineQueue, cloneTerminalContent(content))
				return true
			}
			return false
		},
		clearFunction: func() [][]agent.ContentPart {
			queued := engineQueue
			engineQueue = nil
			return queued
		},
	}
	var coordinator steeringCoordinator

	accepted := testTextContent("accepted")
	deferred := testTextContent("deferred")
	coordinator.enqueue(accepted, engine.Steer(accepted))
	coordinator.enqueue(deferred, engine.Steer(deferred))
	if got := steeringTexts(coordinator.pending()); !slices.Equal(got, []string{"accepted", "deferred"}) {
		t.Fatalf("pending = %q", got)
	}
	if !coordinator.delivered(accepted) || coordinator.delivered(accepted) {
		t.Fatal("accepted delivery was not tracked exactly once")
	}
	if got := steeringTexts(coordinator.pending()); !slices.Equal(got, []string{"deferred"}) {
		t.Fatalf("pending after delivery = %q", got)
	}
}

func TestSteeringCoordinatorRestoresImagesToEditor(t *testing.T) {
	image := &agent.Image{MediaType: "image/png", Data: []byte("png")}
	queued := []agent.ContentPart{
		{Kind: agent.ContentPartText, Text: "describe "},
		{Kind: agent.ContentPartImage, Image: image},
	}
	model := newTUIModel(80, 24, Options{})
	if err := model.insertInput("draft"); err != nil {
		t.Fatal(err)
	}

	model.restoreSteering([][]agent.ContentPart{queued})
	image.Data[0] = 'x'

	content := editorContent(model.input)
	if len(content) != 3 || content[0].Text != "describe " || content[1].Image == nil || string(content[1].Image.Data) != "png" || content[2].Text != "\n\ndraft" {
		t.Fatalf("restored content = %+v", content)
	}
}

func TestSteeringCoordinatorIgnoresStaleDeliveryAfterRestore(t *testing.T) {
	engine := &fakeEngine{clearFunction: func() [][]agent.ContentPart { return [][]agent.ContentPart{testTextContent("accepted")} }}
	coordinator := steeringCoordinator{
		accepted: [][]agent.ContentPart{testTextContent("accepted")},
		deferred: [][]agent.ContentPart{testTextContent("deferred")},
	}

	if restored := steeringTexts(coordinator.restore(engine.ClearSteering)); !slices.Equal(restored, []string{"accepted", "deferred"}) {
		t.Fatalf("restored = %q", restored)
	}
	if coordinator.delivered(testTextContent("accepted")) || len(coordinator.pending()) != 0 {
		t.Fatalf("stale delivery changed coordinator: %q", steeringTexts(coordinator.pending()))
	}
}
