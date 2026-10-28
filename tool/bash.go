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
	defaultBashTimeout = 120 * time.Second
	maximumBashTimeout = time.Hour
	defaultWaitDelay   = time.Second
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
	shell            string
	authorizeNetwork NetworkAuthorizer
	defaultTimeout   time.Duration
	maxTimeout       time.Duration
	waitDelay        time.Duration
}

type bashArguments struct {
	Command string       `json:"command"`
	Timeout *json.Number `json:"timeout"`
	Network bool         `json:"network"`
}

// NewBash returns an unsandboxed Bash tool when authorizeNetwork is nil.
func NewBash(cwd string, authorizeNetwork NetworkAuthorizer) Tool {
	return newBash(cwd, authorizeNetwork)
}

func newBash(cwd string, authorizeNetwork NetworkAuthorizer) *bashTool {
	return &bashTool{
		workspace:        newWorkspace(cwd),
		shell:            bashToolName,
		authorizeNetwork: authorizeNetwork,
		defaultTimeout:   defaultBashTimeout,
		maxTimeout:       maximumBashTimeout,
		waitDelay:        defaultWaitDelay,
	}
}

func (b *bashTool) sandboxed() bool {
	return b.authorizeNetwork != nil
}

func (*bashTool) Definition() agent.ToolDefinition {
	return bashToolDefinition
}

func (b *bashTool) Presentation(snapshot presentationSnapshot) agent.ToolPresentation {
	timeout := b.defaultTimeout
	var number json.Number
	switch value := snapshot.Arguments["timeout"].(type) {
	case json.Number:
		number = value
	case string:
		number = json.Number(value)
	}
	if seconds, err := number.Int64(); err == nil {
		timeout = time.Duration(seconds) * time.Second
	}

	return bashPresentation(snapshotString(snapshot, "command"), timeout)
}

func bashPresentation(command string, timeout time.Duration) agent.ToolPresentation {
	arguments := ""
	if command != "" {
		arguments = displayToolArgument(command)
	}

	return agent.ToolPresentation{Title: bashToolName, Arguments: arguments, Timeout: timeout}
}
