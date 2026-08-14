# Eul

Eul is a focused coding agent for the terminal. It can explore a codebase, edit
files, run commands, and work through multi-step tasks in a conversation.

Eul is heavily inspired by [Pi](https://pi.dev/), with *some* batteries included
while remaining deliberately minimal. It is implemented using only the Go
standard library, with zero external dependencies.

Eul is intended for trusted local use on macOS and Linux. Conversations are
persisted locally, and tools run directly on your machine with your permissions.

## Features

- Full-screen terminal interface with streaming responses and tool activity
- File reading, writing, and targeted editing with visible diffs
- Shell command execution with streamed output
- File search and references from the prompt
- Image attachments in prompts
- Steering while the agent is working
- Autonomous goals for longer-running tasks
- Parallel research with subagents
- Project instructions from `AGENTS.md`
- Global and project-specific Agent Skills
- Automatic context compaction
- Resumable sessions
- OpenAI Codex and OpenRouter support

## Getting started

Building Eul from source requires Go 1.26.

```sh
go build -o eul ./cmd/eul
```

Sign in with ChatGPT and start a session:

```sh
./eul login
./eul --cwd /path/to/project
```

To use OpenRouter, provide an API key and model:

```sh
OPENROUTER_API_KEY=... ./eul --provider openrouter --model <provider/model>
```

OpenRouter feature support depends on the selected model.

## Safety and limitations

- Apart from Bash network isolation, tools are not sandboxed. Shell commands and
  file operations can make arbitrary changes accessible to your user account.
- Saved sessions can contain prompts, source code, and tool output. They are
  protected with local filesystem permissions but are not encrypted at rest.
- Canceling a turn does not undo tool side effects that already occurred.
