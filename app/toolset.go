package app

import (
	"errors"

	"github.com/eul-ai/eul/tool"
)

type toolAccess uint8

const (
	fullToolAccess toolAccess = iota
	subagentToolAccess
)

type toolsetFactory func(string, toolAccess, bool, tool.NetworkAuthorizer, ...tool.Tool) (*tool.Registry, error)

func buildToolset(
	cwd string,
	access toolAccess,
	noSandbox bool,
	authorizeNetwork tool.NetworkAuthorizer,
	additional ...tool.Tool,
) (*tool.Registry, error) {
	bash := tool.NewBashWithNetworkAuthorizer(cwd, authorizeNetwork)
	if noSandbox {
		bash = tool.NewBashWithoutSandbox(cwd)
	}

	var tools []tool.Tool
	switch access {
	case fullToolAccess:
		tools = []tool.Tool{
			tool.NewRead(cwd),
			tool.NewWrite(cwd),
			tool.NewEdit(cwd),
			bash,
		}
	case subagentToolAccess:
		tools = []tool.Tool{tool.NewRead(cwd), bash}
	default:
		return nil, errors.New("unknown tool access")
	}

	return tool.NewRegistry(append(tools, additional...))
}
