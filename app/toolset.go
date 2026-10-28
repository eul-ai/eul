package app

import (
	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/tool"
)

type toolAccess uint8

const (
	fullToolAccess toolAccess = iota
	subagentToolAccess
)

type toolsetFactory func(string, toolAccess, tool.NetworkAuthorizer, ...agent.Tool) []agent.Tool

func buildToolset(
	cwd string,
	access toolAccess,
	authorizeNetwork tool.NetworkAuthorizer,
	additional ...agent.Tool,
) []agent.Tool {
	tools := []agent.Tool{tool.NewRead(cwd), tool.NewBash(cwd, authorizeNetwork)}
	if access == fullToolAccess {
		tools = append(tools, tool.NewWrite(cwd), tool.NewEdit(cwd))
	}
	return append(tools, additional...)
}
