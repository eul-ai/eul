package agent

import (
	"testing"
	"time"
)

func TestClearSteeringKeepsToolRoundWakeUp(t *testing.T) {
	var arbiter continuationArbiter
	arbiter.setRunning(true)
	defer arbiter.setRunning(false)

	steering := arbiter.beginToolRound()
	if cleared := arbiter.clearSteering(); len(cleared) != 0 {
		t.Fatalf("clearSteering returned unexpected batches: %v", cleared)
	}

	content := []ContentPart{{Kind: ContentPartText, Text: "steer"}}
	if !arbiter.steer(content) {
		t.Fatal("steer rejected after clearSteering")
	}

	select {
	case <-steering:
	case <-time.After(time.Second):
		t.Fatal("steering signal not woken by steer after clearSteering")
	}

	next, ok := arbiter.nextAfterTools()
	if !ok || len(next.content) != 1 || next.content[0].Text != "steer" {
		t.Fatalf("steered content not delivered after clearSteering: %+v, %v", next, ok)
	}
	if _, ok := arbiter.nextAfterTools(); ok {
		t.Fatal("steered content delivered twice")
	}
}
