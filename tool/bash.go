package tool

import (
	"encoding/json"
	"time"

	"github.com/eul-ai/eul/agent"
)

const (
	bashToolName       = "bash"
	defaultBashTimeout = 120 * time.Second
	maximumBashTimeout = 10 * time.Minute
	defaultWaitDelay   = time.Second
	bashPreviewLines   = 5
	bashUpdateInterval = 100 * time.Millisecond
)

var bashToolDefinition = agent.ToolDefinition{
	Name:        bashToolName,
	Description: "Run unsandboxed, noninteractive Bash commands with the user's permissions, a timeout, and bounded output.",
	Parameters: strictObject(map[string]agent.JSONSchema{
		"command": {Type: "string", Description: "Command passed exactly to bash -c."},
		"timeout": nullable("integer", "Optional timeout in seconds; null uses the configured default."),
	}, "command", "timeout"),
}

type Bash struct {
	workspace      workspace
	shell          string
	defaultTimeout time.Duration
	maxTimeout     time.Duration
	waitDelay      time.Duration
}

type bashArguments struct {
	Command string `json:"command"`
	Timeout *int   `json:"timeout"`
}

func NewBash(cwd string) *Bash {
	return &Bash{
		workspace:      newWorkspace(cwd),
		shell:          bashToolName,
		defaultTimeout: defaultBashTimeout,
		maxTimeout:     maximumBashTimeout,
		waitDelay:      defaultWaitDelay,
	}
}

func (*Bash) Definition() agent.ToolDefinition {
	return bashToolDefinition
}

func (b *Bash) Presentation(snapshot PresentationSnapshot) agent.ToolPresentation {
	timeout := b.defaultTimeout
	if number, ok := snapshot.Arguments["timeout"].(json.Number); ok {
		if seconds, err := number.Int64(); err == nil {
			timeout = time.Duration(seconds) * time.Second
		}
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
