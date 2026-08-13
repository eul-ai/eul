package terminal

import "slices"

type steeringCoordinator struct {
	accepted []string
	deferred []string
}

func (coordinator *steeringCoordinator) enqueue(steer func(string) bool, prompt string) {
	if len(coordinator.deferred) == 0 && steer != nil && steer(prompt) {
		coordinator.accepted = append(coordinator.accepted, prompt)
		return
	}

	coordinator.deferred = append(coordinator.deferred, prompt)
}

func (coordinator *steeringCoordinator) delivered(prompt string) bool {
	for index, accepted := range coordinator.accepted {
		if accepted != prompt {
			continue
		}
		coordinator.accepted = slices.Delete(coordinator.accepted, index, index+1)
		return true
	}
	return false
}

func (coordinator *steeringCoordinator) nextDeferred() (string, bool) {
	if len(coordinator.deferred) == 0 {
		return "", false
	}

	prompt := coordinator.deferred[0]
	coordinator.deferred = coordinator.deferred[1:]
	return prompt, true
}

func (coordinator *steeringCoordinator) restoreDeferred(prompt string) {
	coordinator.deferred = append([]string{prompt}, coordinator.deferred...)
}

func (coordinator *steeringCoordinator) restore(clear func() []string) []string {
	if clear != nil {
		clear()
	}
	pending := coordinator.pending()
	coordinator.accepted = nil
	coordinator.deferred = nil
	return pending
}

func (coordinator *steeringCoordinator) pending() []string {
	pending := make([]string, 0, len(coordinator.accepted)+len(coordinator.deferred))
	pending = append(pending, coordinator.accepted...)
	pending = append(pending, coordinator.deferred...)
	return pending
}
