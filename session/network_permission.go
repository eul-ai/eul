package session

import (
	"context"

	"github.com/eul-ai/eul/terminal"
	"github.com/eul-ai/eul/tool"
)

func newNetworkPermissionBroker() (tool.NetworkAuthorizer, <-chan terminal.PermissionRequest) {
	requests := make(chan terminal.PermissionRequest)
	authorize := func(ctx context.Context, command string) (bool, error) {
		response := make(chan bool, 1)
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
		case allowed := <-response:
			return allowed, nil
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
	return authorize, requests
}
