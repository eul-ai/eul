package terminal

import (
	"slices"

	"github.com/eul-ai/eul/agent"
)

type steeringCoordinator struct {
	accepted [][]agent.ContentPart
	deferred [][]agent.ContentPart
}

func (coordinator *steeringCoordinator) enqueue(content []agent.ContentPart, accepted bool) {
	if len(coordinator.deferred) == 0 && accepted {
		coordinator.accepted = append(coordinator.accepted, cloneTerminalContent(content))
		return
	}

	coordinator.deferred = append(coordinator.deferred, cloneTerminalContent(content))
}

func (coordinator *steeringCoordinator) delivered(content []agent.ContentPart) bool {
	for index, accepted := range coordinator.accepted {
		if !contentEqual(accepted, content) {
			continue
		}
		coordinator.accepted = slices.Delete(coordinator.accepted, index, index+1)
		return true
	}
	return false
}

func (coordinator *steeringCoordinator) nextDeferred() ([]agent.ContentPart, bool) {
	if len(coordinator.deferred) == 0 {
		return nil, false
	}

	content := coordinator.deferred[0]
	coordinator.deferred[0] = nil
	coordinator.deferred = coordinator.deferred[1:]
	return cloneTerminalContent(content), true
}

func (coordinator *steeringCoordinator) restoreDeferred(content []agent.ContentPart) {
	coordinator.deferred = append([][]agent.ContentPart{cloneTerminalContent(content)}, coordinator.deferred...)
}

func (coordinator *steeringCoordinator) restore(clear func() [][]agent.ContentPart) [][]agent.ContentPart {
	if clear != nil {
		clear()
	}
	pending := coordinator.pending()
	coordinator.accepted = nil
	coordinator.deferred = nil
	return pending
}

func (coordinator *steeringCoordinator) pending() [][]agent.ContentPart {
	pending := make([][]agent.ContentPart, 0, len(coordinator.accepted)+len(coordinator.deferred))
	pending = append(pending, coordinator.accepted...)
	pending = append(pending, coordinator.deferred...)
	return pending
}
