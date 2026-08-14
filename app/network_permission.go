package app

import (
	"context"
	"sync/atomic"

	"github.com/eul-ai/eul/terminal"
	"github.com/eul-ai/eul/tool"
)

func newNetworkPermissionBroker(noSandbox bool) (tool.NetworkAuthorizer, <-chan terminal.PermissionRequest) {
	if noSandbox {
		return func(context.Context, string) (bool, error) { return true, nil }, nil
	}

	requests := make(chan terminal.PermissionRequest)
	var sessionAllowed atomic.Bool
	authorize := func(ctx context.Context, command string) (bool, error) {
		if sessionAllowed.Load() {
			return true, nil
		}

		response := make(chan terminal.PermissionDecision, 1)
		request := terminal.PermissionRequest{
			Title:        "Network access requested",
			Subject:      "bash",
			Description:  "needs access to the network",
			Detail:       command,
			DetailPrefix: "$ ",
			Notice:       "This command and its descendants will have network access.",
			Response:     response,
		}

		select {
		case requests <- request:
		case <-ctx.Done():
			return false, ctx.Err()
		}

		select {
		case decision := <-response:
			switch decision {
			case terminal.PermissionAllowOnce:
				return true, nil
			case terminal.PermissionAllowSession:
				sessionAllowed.Store(true)
				return true, nil
			default:
				return false, nil
			}
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
	return authorize, requests
}
