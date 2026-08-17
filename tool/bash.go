package tool

import (
	"context"
	"encoding/json"
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
	Description: "Run noninteractive Bash commands with the user's filesystem permissions, optional network isolation, a timeout, and bounded output.",
	Parameters: StrictObject(map[string]agent.JSONSchema{
		"command": {Type: "string", Description: "Command passed exactly to bash -c."},
		"timeout": nullable("integer", "Optional timeout in seconds; null uses the configured default."),
		"network": {Type: "boolean", Description: "Whether the command and its descendants may access the network. false uses OS network isolation on Linux and macOS; true requires user approval."},
	}, "command", "timeout", "network"),
}

type NetworkAuthorizer func(context.Context, string) (bool, error)

type Bash struct {
	workspace        workspace
	shell            string
	authorizeNetwork NetworkAuthorizer
	noSandbox        bool
	defaultTimeout   time.Duration
	maxTimeout       time.Duration
	waitDelay        time.Duration
}

type bashArguments struct {
	Command string       `json:"command"`
	Timeout *json.Number `json:"timeout"`
	Network bool         `json:"network"`
}

func NewBash(cwd string) *Bash {
	return NewBashWithNetworkAuthorizer(cwd, nil)
}

func NewBashWithNetworkAuthorizer(cwd string, authorizeNetwork NetworkAuthorizer) *Bash {
	return newBash(cwd, authorizeNetwork, false)
}

func NewBashWithoutSandbox(cwd string) *Bash {
	return newBash(cwd, nil, true)
}

func newBash(cwd string, authorizeNetwork NetworkAuthorizer, noSandbox bool) *Bash {
	return &Bash{
		workspace:        newWorkspace(cwd),
		shell:            bashToolName,
		authorizeNetwork: authorizeNetwork,
		noSandbox:        noSandbox,
		defaultTimeout:   defaultBashTimeout,
		maxTimeout:       maximumBashTimeout,
		waitDelay:        defaultWaitDelay,
	}
}

func (*Bash) Definition() agent.ToolDefinition {
	return bashToolDefinition
}

func (b *Bash) Presentation(snapshot PresentationSnapshot) agent.ToolPresentation {
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
