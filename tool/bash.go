package tool

import (
	"time"

	"yaah/agent"
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

func (*Bash) Presentation(snapshot PresentationSnapshot) agent.ToolPresentation {
	return bashPresentation(snapshotString(snapshot, "command"))
}

func bashPresentation(command string) agent.ToolPresentation {
	arguments := ""
	if command != "" {
		arguments = displayToolArgument(command)
	}
	return agent.ToolPresentation{Title: bashToolName, Arguments: arguments}
}
