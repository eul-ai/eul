package terminal

func (c *tuiController) handlePermission(request PermissionRequest) (bool, error) {
	if request.Response == nil {
		return false, nil
	}
	if c.permissionsAllowedForSession {
		respondPermission(request, PermissionAllowSession)
		return false, nil
	}
	if !c.model.running || c.model.interrupted || c.model.activity.kind == activityCanceling {
		respondPermission(request, PermissionDenyOnce)
		return false, nil
	}
	if c.permission != nil {
		c.queuedPermissions = append(c.queuedPermissions, request)
		c.model.permission.total++
		c.dirty = true
		return false, nil
	}

	c.permission = &request
	c.model.showPermission(request, 1, 1)
	c.dirty = true
	return false, nil
}

func (c *tuiController) resolvePermission(decision PermissionDecision) {
	if c.permission == nil {
		return
	}

	respondPermission(*c.permission, decision)
	c.permission = nil
	if decision == PermissionAllowSession {
		c.permissionsAllowedForSession = true
		for _, request := range c.queuedPermissions {
			respondPermission(request, decision)
		}
		c.queuedPermissions = nil
	}
	if len(c.queuedPermissions) == 0 {
		c.model.clearPermission()
		c.restoreActivityAfterPermission()
		return
	}

	next := c.queuedPermissions[0]
	c.queuedPermissions = c.queuedPermissions[1:]
	index := c.model.permission.index + 1
	total := c.model.permission.total
	c.permission = &next
	c.model.showPermission(next, index, total)
}

func (c *tuiController) denyPermissions() {
	if c.permission != nil {
		respondPermission(*c.permission, PermissionDenyOnce)
	}
	for _, request := range c.queuedPermissions {
		respondPermission(request, PermissionDenyOnce)
	}
	c.permission = nil
	c.queuedPermissions = nil
	c.model.clearPermission()
}

func (c *tuiController) restoreActivityAfterPermission() {
	if detail, ok := c.model.pendingToolActivity(); ok {
		c.model.activity = activity{kind: activityTool, detail: detail}
		return
	}
	c.model.activity = activity{kind: activityThinking}
}

func respondPermission(request PermissionRequest, decision PermissionDecision) {
	select {
	case request.Response <- decision:
	default:
	}
}
