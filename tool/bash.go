package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/eul-ai/eul/agent"
)

const (
	bashToolName       = "bash"
	bashShell          = "bash"
	defaultBashTimeout = 120 * time.Second
	maximumBashTimeout = time.Hour
	bashWaitDelay      = time.Second
	bashPreviewLines   = 5
	bashUpdateInterval = 100 * time.Millisecond
)

var bashToolDefinition = agent.ToolDefinition{
	Name:        bashToolName,
	Description: "Run a noninteractive Bash command.",
	Parameters: strictObject(map[string]agent.JSONSchema{
		"command": {Type: "string", Description: "Command passed to bash -c."},
		"timeout": nullable("integer", fmt.Sprintf("Timeout in seconds; null uses the default (%d seconds).", defaultBashTimeout/time.Second)),
		"network": {Type: "boolean", Description: "Allow network access; true requires approval."},
	}, "command", "timeout", "network"),
}

type NetworkAuthorizer func(context.Context, string) (bool, error)

type bashTool struct {
	workspace        workspace
	authorizeNetwork NetworkAuthorizer
}

type bashArguments struct {
	Command string       `json:"command"`
	Timeout *json.Number `json:"timeout"`
	Network bool         `json:"network"`
}

// NewBash returns an unsandboxed Bash tool when authorizeNetwork is nil.
func NewBash(cwd string, authorizeNetwork NetworkAuthorizer) Tool {
	bash := bashTool{workspace: newWorkspace(cwd), authorizeNetwork: authorizeNetwork}
	return spec[bashArguments]{
		definition: bashToolDefinition,
		present:    presentBash,
		run:        bash.run,
	}
}

func (b bashTool) sandboxed() bool {
	return b.authorizeNetwork != nil
}

func presentBash(args bashArguments) agent.ToolPresentation {
	timeout := defaultBashTimeout
	if args.Timeout != nil {
		if seconds, err := args.Timeout.Int64(); err == nil {
			timeout = time.Duration(seconds) * time.Second
		}
	}
	return bashPresentation(args.Command, timeout)
}

func bashPresentation(command string, timeout time.Duration) agent.ToolPresentation {
	return agent.ToolPresentation{Title: bashToolName, Arguments: displayToolArgument(command), Timeout: timeout}
}
