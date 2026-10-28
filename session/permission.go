package session

import (
	"context"
	"slices"
)

type PermissionDecision uint8

const (
	PermissionDenyOnce PermissionDecision = iota
	PermissionAllowOnce
	PermissionAllowSession
)

type PermissionRequest struct {
	Title        string
	Subject      string
	Description  string
	Detail       string
	DetailPrefix string
	Notice       string
	response     chan bool
}

// AuthorizeNetwork asks the user whether command may use the network. It
// matches tool.NetworkAuthorizer.
func (s *Session) AuthorizeNetwork(ctx context.Context, command string) (bool, error) {
	request := &PermissionRequest{
		Title:        "Network access requested",
		Subject:      "bash",
		Description:  "needs access to the network",
		Detail:       command,
		DetailPrefix: "$ ",
		Notice:       "This command and its descendants will have network access.",
		response:     make(chan bool, 1),
	}

	s.mu.Lock()
	switch {
	case s.networkAllowed:
		s.mu.Unlock()
		return true, nil
	case s.interrupted:
		s.mu.Unlock()
		return false, nil
	}
	s.permissions = append(s.permissions, request)
	s.mu.Unlock()
	s.publish(Update{Kind: UpdatePermissions})

	select {
	case allowed := <-request.response:
		return allowed, nil
	case <-ctx.Done():
		return false, ctx.Err()
	}
}

// Permissions returns the pending requests, the first of which awaits an
// answer.
func (s *Session) Permissions() []*PermissionRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.permissions)
}

// ResolvePermission answers the first pending request, or all of them when
// the user allows network access for the rest of the session.
func (s *Session) ResolvePermission(decision PermissionDecision) {
	s.mu.Lock()
	defer s.mu.Unlock()

	count := min(1, len(s.permissions))
	if decision == PermissionAllowSession {
		s.networkAllowed = true
		count = len(s.permissions)
	}
	s.answerLocked(count, decision != PermissionDenyOnce)
}

func (s *Session) answerLocked(count int, allowed bool) {
	for _, request := range s.permissions[:count] {
		request.response <- allowed
	}
	s.permissions = slices.Delete(s.permissions, 0, count)
}
