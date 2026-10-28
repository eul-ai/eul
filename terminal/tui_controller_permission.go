package terminal

import "slices"

func (c *tuiController) handlePermission(request PermissionRequest) (bool, error) {
	switch {
	case request.Response == nil:
		return false, nil
	case c.permissionsAllowedForSession:
		respondPermission(request, PermissionAllowSession)
		return false, nil
	case c.model.running && (c.model.interrupted || c.model.activity.kind == activityCanceling):
		respondPermission(request, PermissionDenyOnce)
		return false, nil
	}

	c.model.permission.queue = append(c.model.permission.queue, request)
	c.dirty = true
	return false, nil
}

func (c *tuiController) resolvePermission(decision PermissionDecision) {
	queue := c.model.permission.queue
	if len(queue) == 0 {
		return
	}

	if decision == PermissionAllowSession {
		c.permissionsAllowedForSession = true
		c.respondPermissions(decision)
		return
	}
	respondPermission(queue[0], decision)
	c.model.permission = permissionState{queue: slices.Delete(queue, 0, 1)}
}

func (c *tuiController) respondPermissions(decision PermissionDecision) {
	for _, request := range c.model.permission.queue {
		respondPermission(request, decision)
	}
	c.model.permission = permissionState{}
}

func respondPermission(request PermissionRequest, decision PermissionDecision) {
	select {
	case request.Response <- decision:
	default:
	}
}
