# Eul

Eul is a focused coding agent for the terminal. It can explore a codebase, edit
files, run commands, use language-server features, and work through multi-step
tasks in an interactive conversation.

Eul is heavily inspired by [Pi](https://pi.dev/), with *some*
batteries included while still staying deliberately minimal.

Eul is intended for trusted local use on macOS and Linux. Conversations are
persisted locally, and tools run directly on your machine with your permissions.

## Features

- Full-screen terminal interface with streaming responses and tool activity
- File reading, writing, and targeted editing with visible diffs
- Shell command execution with streamed output
- Optional language-server diagnostics, navigation, symbol lookup, and rename
- Adjustable model thinking levels and reasoning summaries
- File search and references from the prompt with `@`
- Steering messages that can be queued while the agent is working
- Autonomous goals for longer tasks
- Selective asynchronous read-only subagents for parallel research
- Project instructions from `AGENTS.md`
- Global and project-specific [Agent Skills](https://agentskills.io)
- Automatic context compaction and usage information for supported models
- Durable sessions with direct and interactive resumption
- Browser and device-code sign-in with ChatGPT

## Getting started

Building Eul from source requires Go 1.26.

```sh
go build -o eul ./cmd/eul
```

Sign in and start an interactive session:

```sh
./eul login
./eul --cwd /path/to/project
```

For a headless environment, use device authorization:

```sh
./eul login --device-auth
```

ChatGPT sign-in uses an experimental integration with the subscription-backed
Codex service. Its third-party API behavior may change.

## Usage

```text
eul [--provider <provider>] [--model <model>] [--fast-model <model>] [--balanced-model <model>] [--thinking <level>] [--cwd <directory>] [--no-sandbox]
eul --resume[=<session-id>]
eul login [--provider <provider>] [--device-auth]
eul logout [--provider <provider>]
```

`--provider` selects a provider backend. The default and currently built-in
backend is `openai-codex`. `--cwd` chooses the working directory for the session.
Relative tool paths are resolved from that directory.

Thinking levels are:

- off
- minimal
- low
- medium (default)
- high
- xhigh
- max

Availability depends on the selected model.

The main and powerful-subagent model defaults to `gpt-5.6-sol`. Balanced
subagents default to `gpt-5.6-terra`, and fast subagents default to
`gpt-5.6-luna`. Use `--model`, `--balanced-model`, and `--fast-model` to
override them for a new session.

`EUL_HOME` overrides the credential, session, and global LSP configuration directory.

## Interactive mode

The terminal interface keeps the conversation, reasoning summaries, tool calls,
diffs, errors, context usage, and model activity visible while you work.

### Controls

| Control | Action |
| --- | --- |
| Enter | Submit a prompt, queue steering, or apply a selected completion or permission response |
| Shift-Enter | Insert a newline |
| Shift-Tab | Cycle supported thinking levels |
| `/` | Show commands at the start of the editor |
| `@` | Search for a file and insert its path |
| Tab | Apply a completion or switch the selected permission response |
| Up / Down | Navigate prompt history or completion results, or scroll permission details |
| Page Up / Page Down | Scroll the conversation or permission details |
| Mouse wheel | Scroll the conversation |
| Mouse drag | Select and copy conversation text |
| Alt-Up | Restore queued steering messages to the editor |
| Y / N | Allow once or deny an active permission request |
| Escape | Close completions, deny a permission request, or cancel the active turn |
| Ctrl-C | Clear the editor, cancel the active turn, or exit from an empty prompt |
| Ctrl-D | Exit from an empty prompt |
| Ctrl-L | Redraw the terminal |

Bracketed multiline paste preserves newlines and blank lines.

### Commands

| Command | Action |
| --- | --- |
| `/help` | Show available commands |
| `/resume` | Select a saved session for the current working directory |
| `/new` | Start a new session |
| `/compact` | Compact the conversation context |
| `/exit` | Exit Eul |
| `/goal <objective>` | Set or replace an autonomous goal |
| `/goal` | Show the active goal |
| `/goal clear` | Clear the active goal |
| `/skill:<name> [instructions]` | Load a skill explicitly |

## Tool access

Eul can read and edit files, run Bash commands, navigate code with configured
language servers, and delegate research to read-only subagents.

Bash commands run without network access by default. Commands that need network
access pause for one-time user approval. `--no-sandbox` disables Bash network
isolation and automatically allows network access.

## Subagents

Eul may launch up to four independent read-only subagents for parallel research.
They run in the background and can be canceled when no longer needed.

## Goals

An active goal lets Eul continue working after an individual response would
normally finish. This is useful for larger objectives that require several rounds
of inspection, changes, and verification.

You can steer the agent at any time. Steering takes priority over autonomous goal
continuation. A goal remains active until it is completed or cleared.

## Sessions

Sessions are saved under `EUL_HOME`, or the platform user configuration directory
when `EUL_HOME` is unset. `--resume` opens the most recently updated session for
the current working directory, while `--resume=<session-id>` opens a specific
session. Use `/resume` to select interactively from sessions for the current
working directory, or `/new` to leave the current session intact and start a new
one.

If a process ended during an active turn, the restored session remains idle and
shows a warning. Review the conversation before continuing because tool side
effects may already have occurred.

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

- Apart from Bash network isolation on Linux and macOS, tools are not sandboxed.
  Shell commands and file operations can make arbitrary changes accessible to
  your user account.
- Saved sessions can contain prompts, source code, and tool output. They are
  protected with local filesystem permissions but are not encrypted at rest.
- Canceling a turn does not undo tool side effects that already occurred.
