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
eul [--model <model>] [--thinking <level>] [--cwd <directory>]
eul --resume[=<session-id>]
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

The model defaults to `gpt-5.6-sol`.

These environment variables provide common defaults:

| Variable | Purpose |
| --- | --- |
| `EUL_HOME` | Credential, session, and global LSP configuration directory |
| `EUL_THINKING_LEVEL` | Default thinking level |
| `OPENAI_MODEL` | Override the default model and powerful subagent model |
| `OPENAI_MODEL_BALANCED` | Balanced subagent model; defaults to `OPENAI_MODEL` |
| `OPENAI_MODEL_FAST` | Fast subagent model; defaults to `OPENAI_MODEL` |
| `OPENAI_REASONING_SUMMARY` | Reasoning summary mode: `auto`, `concise`, `detailed`, or `none` |

## Interactive mode

The terminal interface keeps the conversation, reasoning summaries, tool calls,
diffs, errors, context usage, and model activity visible while you work.

### Controls

| Control | Action |
| --- | --- |
| Enter | Submit a prompt, queue steering, or apply a selected completion |
| Shift-Enter | Insert a newline |
| Shift-Tab | Cycle supported thinking levels |
| `/` | Show commands at the start of the editor |
| `@` | Search for a file and insert its path |
| Tab | Apply the selected command or file completion |
| Up / Down | Navigate prompt history or completion results |
| Page Up / Page Down | Scroll the conversation |
| Mouse wheel | Scroll the conversation |
| Mouse drag | Select and copy conversation text |
| Alt-Up | Restore queued steering messages to the editor |
| Escape | Close the completion window or cancel the active turn |
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

## Built-in tools

Eul can use the following capabilities as needed:

- Read UTF-8 text files
- Create and overwrite files
- Replace a specific matching section of a file
- Run Bash commands
- Inspect language-server diagnostics
- Look up hover information, definitions, references, and document symbols
- Rename symbols across a workspace
- Launch independent read-only research in background subagents and wait for selected results

Language-server configuration is loaded from `lsp.json` under `EUL_HOME`, or
the platform user configuration directory when `EUL_HOME` is unset. For
development, Eul falls back to `lsp.json` in the project root when the global
file does not exist. If neither file exists, the session starts without
language-server tools. Each entry defines a server command and the source
extensions it handles:

```json
[
  {
    "name": "gopls",
    "command": "gopls",
    "languageID": "go",
    "extensions": [".go"]
  }
]
```

Language-server features are available when a configured command is installed.

## Subagents

Eul may launch up to four independent read-only subagents when separate context
and parallel investigation are useful. Launches return immediately; completed
results remain available until `subagent_wait` collects them, and uncollected
results continue to occupy capacity. `subagent_cancel` stops selected work.
Canceling a turn while `subagent_wait` is active also stops the selected agents;
canceling unrelated main-context work does not.

A launch may select one model profile for its batch from `fast`, `balanced`, or
`powerful`; the default is `balanced`. The fast and balanced profiles use
`OPENAI_MODEL_FAST` and `OPENAI_MODEL_BALANCED`, while powerful uses the main
session model. A launch may also select a thinking level from `off`, `minimal`,
`low`, `medium`, or `high`; the default is `low`. Subagents begin a tool-free final
response after five minutes or 20 normal provider generations. The final response
is separate from the generation budget. While waiting, Eul shows cumulative input
and output usage, normal-generation progress, and the reason finalization began.

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

- Tools are not sandboxed. Shell commands and file operations can make arbitrary
  changes accessible to your user account.
- Saved sessions can contain prompts, source code, and tool output. They are
  protected with local filesystem permissions but are not encrypted at rest.
- Canceling a turn does not undo tool side effects that already occurred.
- Canceling a subagent wait cancels its selected background tasks; canceling
  unrelated main-context work leaves background tasks running.
- Subagents are read-only, session-local, canceled on session shutdown, and are
  not restored after process restart.
- Markdown rendering is limited to inline bold, italic, and code formatting.
- Eul currently uses ChatGPT authentication for OpenAI Codex models.
