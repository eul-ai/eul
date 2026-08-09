# Eul

Eul is a focused coding agent for the terminal. It can explore a codebase, edit
files, run commands, use language-server features, and work through multi-step
tasks in an interactive conversation.

Eul is heavily inspired by [Pi](https://github.com/badlogic/pi-mono), with *some*
batteries included while still staying deliberately minimal.

Eul is intended for trusted local use on macOS and Linux. Conversations are kept
in memory and tools run directly on your machine with your permissions.

## Features

- Full-screen terminal interface with streaming responses and tool activity
- File reading, writing, and targeted editing with visible diffs
- Shell command execution with streamed output
- Optional language-server diagnostics, navigation, symbol lookup, and rename
- Adjustable model thinking levels and reasoning summaries
- File search and references from the prompt with `@`
- Steering messages that can be queued while the agent is working
- Autonomous goals for longer tasks
- Read-only parallel subagents when explicitly requested
- Project instructions from `AGENTS.md`
- Global and project-specific [Agent Skills](https://agentskills.io)
- Automatic context compaction and usage information for supported models
- Inline bold, italic, and code Markdown in the conversation
- Browser and device-code sign-in with ChatGPT

## Getting started

Build Eul from source:

```sh
go build -o eul ./cmd/eul
```

Sign in and start an interactive session:

```sh
./eul login
./eul --model <model> --cwd /path/to/project
```

For a headless environment, use device authorization:

```sh
./eul login --device-auth
```

ChatGPT sign-in uses an experimental integration with the subscription-backed
Codex service. Its third-party API behavior may change.

## Usage

```text
eul --model <model> [--thinking <level>] [--cwd <directory>]
eul login [--device-auth]
eul logout
```

`--cwd` chooses the working directory for the session. Relative tool paths are
resolved from that directory.

Thinking levels are:

```text
off, minimal, low, medium, high, xhigh, max
```

The default is `medium`. Availability depends on the selected model.

These environment variables provide common defaults:

| Variable | Purpose |
| --- | --- |
| `EUL_HOME` | Credential storage directory |
| `EUL_THINKING_LEVEL` | Default thinking level |
| `OPENAI_MODEL` | Default model |
| `OPENAI_REASONING_SUMMARY` | Reasoning summary mode: `auto`, `concise`, `detailed`, or `none` |

## Interactive mode

The terminal interface keeps the conversation, reasoning summaries, tool calls,
diffs, errors, context usage, and model activity visible while you work.

### Controls

| Control | Action |
| --- | --- |
| Enter | Submit a prompt or queue steering while the agent is working |
| Shift-Enter | Insert a newline |
| Shift-Tab | Cycle supported thinking levels |
| `@` | Search for a file and insert its path |
| Up / Down | Navigate prompt history or file-search results |
| Page Up / Page Down | Scroll the conversation |
| Mouse wheel | Scroll the conversation |
| Mouse drag | Select and copy conversation text |
| Alt-Up | Restore queued steering messages to the editor |
| Escape | Close file search or cancel the active turn |
| Ctrl-C | Clear the editor, cancel the active turn, or exit from an empty prompt |
| Ctrl-D | Exit from an empty prompt |
| Ctrl-L | Redraw the terminal |

Bracketed multiline paste preserves newlines and blank lines.

### Commands

| Command | Action |
| --- | --- |
| `/help` | Show available commands |
| `/clear` | Clear the conversation and active goal |
| `/exit` | Exit Eul |
| `/goal <objective>` | Set or replace an autonomous goal |
| `/goal` | Show the active goal |
| `/goal clear` | Clear the active goal |
| `/skill:<name> [instructions]` | Load a skill explicitly |

## Built-in tools

Eul can use the following capabilities as needed:

- Read UTF-8 text files
- Create and overwrite files
- Replace a specific matching section of a file
- Run Bash commands
- Inspect language-server diagnostics
- Look up hover information, definitions, references, and document symbols
- Rename symbols across a workspace
- Delegate independent read-only research to parallel subagents

Language-server features are available when a supported server is installed. Go
projects use `gopls` when available.

## Goals

An active goal lets Eul continue working after an individual response would
normally finish. This is useful for larger objectives that require several rounds
of inspection, changes, and verification.

You can steer the agent at any time. Steering takes priority over autonomous goal
continuation. A goal remains active until it is completed or cleared.

## Skills

Eul discovers Agent Skills from:

- `~/.agents/skills`
- `.agents/skills` in the working directory

Project skills take precedence over global skills with the same name. Matching
skills can be selected automatically, or loaded directly with
`/skill:<name> [instructions]`.

Skills are trusted local instructions and can direct the agent to use tools or
run bundled scripts with your permissions.

## Safety and limitations

- Tools are not sandboxed. Shell commands and file operations can make arbitrary
  changes accessible to your user account.
- Sessions are not persisted. `/clear` discards the current conversation and goal.
- Canceling a turn does not undo tool side effects that already occurred.
- Subagents are read-only and are used only when explicitly requested.
- Markdown rendering is limited to inline bold, italic, and code formatting.
- Eul currently uses ChatGPT authentication for OpenAI Codex models.
