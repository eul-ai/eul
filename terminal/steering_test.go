package terminal

import (
	"slices"
	"testing"
)

func TestSteeringCoordinatorTracksAcceptedAndDeferredMessages(t *testing.T) {
	var engineQueue []string
	engine := &fakeEngine{
		steerFunction: func(prompt string) bool {
			if prompt == "accepted" {
				engineQueue = append(engineQueue, prompt)
				return true
			}
			return false
		},
		clearFunction: func() []string {
			queued := append([]string(nil), engineQueue...)
			engineQueue = nil
			return queued
		},
	}
	var coordinator steeringCoordinator

	coordinator.enqueue("accepted", engine.Steer("accepted"))
	coordinator.enqueue("deferred", engine.Steer("deferred"))
	if !slices.Equal(coordinator.pending(), []string{"accepted", "deferred"}) {
		t.Fatalf("pending = %q", coordinator.pending())
	}
	if !coordinator.delivered("accepted") || coordinator.delivered("accepted") {
		t.Fatal("accepted delivery was not tracked exactly once")
	}
	if !slices.Equal(coordinator.pending(), []string{"deferred"}) {
		t.Fatalf("pending after delivery = %q", coordinator.pending())
	}
}

func TestSteeringCoordinatorIgnoresStaleDeliveryAfterRestore(t *testing.T) {
	engine := &fakeEngine{clearFunction: func() []string { return []string{"accepted"} }}
	coordinator := steeringCoordinator{
		accepted: []string{"accepted"},
		deferred: []string{"deferred"},
	}

	if restored := coordinator.restore(engine.ClearSteering); !slices.Equal(restored, []string{"accepted", "deferred"}) {
		t.Fatalf("restored = %q", restored)
	}
	if coordinator.delivered("accepted") || len(coordinator.pending()) != 0 {
		t.Fatalf("stale delivery changed coordinator: %q", coordinator.pending())
	}
}
