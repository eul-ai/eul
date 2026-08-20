package app

import (
	"errors"

	"github.com/eul-ai/eul/tool"
)

type toolAccess uint8

const (
	fullToolAccess toolAccess = iota
	readOnlyToolAccess
)

type toolsetFactory func(string, toolAccess, bool, tool.NetworkAuthorizer, ...tool.Tool) (*tool.Registry, error)

func buildToolset(
	cwd string,
	access toolAccess,
	noSandbox bool,
	authorizeNetwork tool.NetworkAuthorizer,
	additional ...tool.Tool,
) (*tool.Registry, error) {
	var tools []tool.Tool
	switch access {
	case fullToolAccess:
		bash := tool.NewBashWithNetworkAuthorizer(cwd, authorizeNetwork)
		if noSandbox {
			bash = tool.NewBashWithoutSandbox(cwd)
		}
		tools = []tool.Tool{
			tool.NewRead(cwd),
			tool.NewWrite(cwd),
			tool.NewReplace(cwd),
			tool.NewInsert(cwd),
			bash,
		}
	case readOnlyToolAccess:
		tools = []tool.Tool{tool.NewRead(cwd)}
	default:
		return nil, errors.New("unknown tool access")
	}

	return tool.NewRegistry(append(tools, additional...))
}
