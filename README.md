# Yaah

Yaah is a small, trusted-local coding agent written in Go. It provides a
full-screen interactive terminal, one-shot execution, an in-memory conversation,
coding tools, and an OpenAI Responses API adapter.

## Scope

- Go implementation; Bash is required at runtime.
- macOS and Linux are the initial targets.
- Tools run unsandboxed with the user's permissions.
- Provider adapters are selected at compile time.
- Sessions live only in memory and `/clear` discards them.

Yaah supports inline bold, italic, and code Markdown in assistant output, reasoning,
and tool-detail lines, but deliberately excludes broader Markdown rendering, dynamic
plugins, provider negotiation, model
catalogs, telemetry, MCP, project indexing, multimodal input, and session persistence.

## Usage

```text
yaah --model <model> [--thinking <level>] [--cwd <directory>]
yaah --model <model> [--thinking <level>] "one-shot prompt"
printf 'one-shot prompt' | yaah --model <model> [--thinking <level>]
yaah login [--device-auth]
yaah logout
```

`OPENAI_MODEL` and `YAAH_THINKING_LEVEL` provide flag defaults. Thinking levels
are `off`, `minimal`, `low`, `medium`, `high`, `xhigh`, and `max`; the default is
`medium`. Provider adapters map these generic levels to model-native controls.
Unavailable levels are clamped to the nearest level supported by the selected
model; `xhigh` and `max` require explicit model support.

`OPENAI_REASONING_SUMMARY` controls OpenAI reasoning summaries and defaults to
`auto`. Accepted values are `auto`, `concise`, `detailed`, and `none`. It changes
summary verbosity independently of reasoning effort; thinking level `off` still
disables summaries.

Yaah uses its own ChatGPT OAuth credential from `YAAH_HOME/auth.json` or the
operating system's user configuration directory. It does not read Pi or Codex
credential stores.

ChatGPT OAuth uses the subscription-backed Codex endpoint. OpenAI documents
ChatGPT sign-in for Codex, but the direct OAuth client, endpoint, headers, and
wire format used by independent clients are not a stable public third-party API
contract. This mode is experimental.

## Terminal

Interactive mode requires terminal stdin and stdout and opens a full-screen TUI
with a conversation viewport, an expanding multiline input area, and a status
bar. The status shows current activity on the left and model, thinking level, context
usage, and provider-reported usage limits on the right. Available reset times are shown
as relative countdowns and updated locally once a minute. Usage limits are loaded once
when the TUI starts and refreshed after each completed turn when the active provider
supports them. Reasoning summaries, tool activity, compaction notices, and
errors remain visible in the conversation. The TUI preserves the terminal's base
background and uses thinking-level-colored input rules. Conversation text has a one-cell
horizontal inset while block backgrounds retain the full width, with one blank
row above and below the viewport content. Reasoning summaries use muted italic
text, while compact tool blocks use padded pending,
success, and error backgrounds. Tool names use the accent color while their arguments
and detail text use the regular foreground color. Successful `edit` calls show the committed
change with green additions, red removals, and muted context lines. `bash` calls stream the last five visible output lines, report how many earlier lines were omitted, and show live elapsed time followed by the final duration. Inline Markdown is rendered
in tool detail lines. Tool blocks are correlated by call ID and may replace their content while
arguments or execution progress streams. The OpenAI
adapter exposes partial function-call arguments, so `write` previews its first
ten lines before execution and reports how many additional lines were omitted;
the file is still written only after the complete call is validated. Typing `@` at a token boundary opens a working-directory file search between the input and status bar. It uses `fd` when available and falls back to filesystem traversal. Selecting a result inserts its working-directory-relative reference into the prompt. Rendering uses synchronized differential row updates to avoid
flicker during streaming. The current palette is based on the
[Ayu Mirage theme](https://github.com/iodic/pi-ayu-themes/blob/main/themes/ayu-mirage.json).

Interactive controls include:

- Enter or keypad Enter to submit; while the agent is working, it queues a steering message for delivery after the current response and its complete tool batch
- Alt-Up to restore all undelivered steering messages to the editor without canceling the active turn
- Shift-Enter to insert a newline
- Shift-Tab to cycle supported thinking levels
- `@` to search files under the working directory; Up/Down select, Enter/Tab insert, and Escape dismisses
- Left/Right and Home/End to move within the prompt; Ctrl-A/Ctrl-E move to the beginning/end
- Up/Down to navigate prompt history
- Page Up/Page Down or the mouse wheel to scroll the conversation
- Drag with the primary mouse button to select text and copy it to the clipboard on release
- Ctrl-L to redraw
- Ctrl-D to exit when the prompt is empty and no turn is active
- Escape to cancel the active turn and restore undelivered steering messages to the editor
- Ctrl-C to clear a non-empty prompt while idle, cancel the active turn and restore undelivered steering messages, or exit while idle with an empty prompt
- `/help`, `/clear`, and `/exit`
- `/goal <objective>` to set or replace an autonomous goal, `/goal` to show it, and `/goal clear` to remove it

An active goal keeps the same main agent working in the current in-memory conversation after it would otherwise settle. User steering remains higher priority than goal continuation. The model stops autonomous continuation by calling `update_goal` after verifying the objective is complete; `/goal clear` stops it at the next safe settlement boundary without interrupting an executing tool batch. Cancellation or an execution error ends the current run but retains the configured goal, while `/clear` removes both conversation and goal state.

Bracketed multiline paste preserves newlines and blank lines in the editor.

A prompt argument runs one-shot without opening the TUI. Non-terminal stdin is
read to EOF as a single one-shot prompt, so piped input and redirected one-shot output
are supported. One-shot assistant text is streamed to stdout; reasoning summaries,
tool activity, compaction and goal-continuation notices, and errors go to stderr. Intermediate tool
presentation snapshots are omitted in one-shot mode, which prints only execution
start and completion summaries.

## Tools

All relative paths resolve against the session's fixed working directory.
Calls execute sequentially in model-provided order, and results are bounded
before returning to the model.

- `read(path, offset?, limit?)` reads regular UTF-8 text files, up to 2,000
  lines or 50 KiB.
- `write(path, content)` creates or directly overwrites a regular file and
  creates parent directories. Its TUI block previews at most ten lines and 4 KiB
  while arguments stream; previews never mutate the filesystem.
- `edit(path, oldText, newText)` replaces one uniquely matching fragment using
  a same-directory temporary file and rename, then shows the committed diff with
  up to four unchanged context lines on each side. Diff presentations are capped
  at 2,000 lines or 50 KiB.
- `bash(command, timeout?)` runs `bash -c` without subprocess stdin and keeps
  the last 2,000 lines or 50 KiB.
- `lsp_diagnostics(path)` returns current language-server diagnostics.
- `lsp_hover(path, line, character)` returns type and documentation details.
- `lsp_definition(path, line, character)` returns definition locations.
- `lsp_references(path, line, character, includeDeclaration?)` returns reference
  locations.
- `lsp_symbols(path)` returns document symbols.
- `lsp_rename(path, line, character, oldName, newName)` resolves the named
  symbol near the approximate position and renames it across the workspace.
- `subagent(tasks)` runs one to four explicitly requested, independent tasks
  concurrently, shows each child's elapsed time and cumulative reported token
  usage while it runs, and returns their ordered results.
- `update_goal(status)` marks the current main-agent goal complete. It accepts
  only `status: "complete"` and should be called only after verifying every goal
  requirement. Read-only subagents do not receive this tool.

LSP line numbers and UTF-16 character offsets are zero-based. LSP tools are
registered only when a configured language server is installed; currently `.go`
files use gopls when available.

Unknown tools, malformed arguments, and ordinary tool failures become
correlated tool-result errors. Cancellation stops the turn. Tools are not a
security boundary: absolute paths, shell escape, and side effects are possible.

## Subagents

The main agent may use subagents only when the user explicitly requests them.
Each call starts up to four fresh child engines concurrently and waits for every
result before the main agent continues. Its tool block replaces a compact status
list as children finish, while final results remain in input order. Results return
through the normal tool output path and become part of the main conversation.

Subagents are read-only: they receive `read` and, when available, the five
non-mutating LSP tools, but not Bash, file-editing tools, rename, or further
subagents. They use the main model, thinking level, working directory, project
instructions, and available skills, and are not persisted or run in the background.

## Skills

Yaah discovers [Agent Skills](https://agentskills.io) recursively from
`~/.agents/skills` and `.agents/skills` under the fixed working directory. A skill
is a directory containing `SKILL.md`; Yaah does not load arbitrary Markdown files
from these directories or inspect harness-specific skill locations. Project skills
win name collisions with global skills.

Skills can provide instructions for any task. Each is defined by a `SKILL.md`
with limited frontmatter; for example:

```markdown
---
name: code-review
description: Reviews code for correctness and maintainability.
disable-model-invocation: false
---

# Code review
```

The parser supports top-level plain, single-quoted, and double-quoted string values
plus literal `true` and `false` booleans. It does not implement general YAML features
such as block scalars, arrays, mappings, anchors, or tags. `description` is required;
`name` defaults to the skill directory name. Unknown fields are ignored. Unreadable or
malformed skills are skipped silently, and other metadata is used without validation.

Only skill names, descriptions, and absolute `SKILL.md` paths are included in the
system prompt. The model reads the full file on demand when a task matches. Use
`/skill:<name> [instructions]` to load a skill explicitly. Setting
`disable-model-invocation: true` hides a skill from the model's available-skill list
while preserving explicit invocation. Skills are trusted local instructions and may
direct the model to run bundled scripts or make other changes with the user's
permissions.

## Architecture

```text
cmd/yaah/        CLI and dependency wiring
agent/           provider/tool contracts, prompt, and tool-call loop
auth/openai/     browser and device OAuth plus credential refresh
provider/openai/ Responses API requests, SSE decoding, and continuation state
tool/            coding tools, subagents, registry, and output limits
tool/lsp/        optional LSP tools, client lifecycle, rename, and workspace edits
tool/textfile/   shared text snapshots and atomic replacement
terminal/        full-screen TUI and one-shot event rendering
```

The `agent` package owns the narrow provider and toolbox interfaces, including the
provider-neutral thinking levels carried by each request. Provider adapters own
model support and native mappings. The OpenAI adapter owns all wire types and
preserves opaque response output items for stateless continuation. The agent stores
that state without interpreting it.
The optional `tool/lsp` package uses `go.lsp.dev/protocol` for protocol types and JSON-RPC. It owns language-server resources explicitly and is wired into the registry by `cmd/yaah`.

The OpenAI adapter uses bounded SSE streams. Reasoning summaries, output text,
and refusals are delivered incrementally. Completed output items are retained
for tool calls and continuation replay. Requests use `store: false` and follow
the experimental Codex wire contract.

The engine has no fixed tool-round limit. Without an active goal, it continues until
the model returns a final response or the turn ends with an interruption or error. With
an active goal, a final response triggers another goal continuation until the model
marks the goal complete, the user clears it, or the run is interrupted or fails. On an
interruption or error, the engine preserves the existing conversation plus pending user
and tool-result inputs for the next prompt.
Calls that could not execute receive synthetic error results so the provider continuation
remains valid; tool side effects that already occurred remain.

## Compaction

The engine supports compaction through an optional provider capability and asks
the provider before each generation. The OpenAI adapter compacts known GPT-5.6
Codex models at 90% of their 272,000-token context window, including an estimate
of pending user or tool-result input. Unknown models and providers without
compaction support continue without automatic compaction.

The OpenAI adapter uses the stateless
`/codex/responses/compact` endpoint. Its output becomes the canonical opaque
continuation state; the previous history is not appended or interpreted by the
agent. Compaction can occur before a new user response or between tool rounds.

## Authentication

Browser login uses authorization-code OAuth with PKCE and a loopback callback.
Device authorization is available for headless environments. Credentials are
stored privately and atomically, refreshed before expiry, and updated when the
server rotates refresh tokens. Logout removes only yaah's credential file.

Credentials are sent only in HTTP headers.

## Verification

Normal changes should pass:

```text
gofumpt -w <changed Go files>
go test ./...
go test -shuffle=on ./...
go vet ./...
git diff --check
```

Tests use fake providers and tools, `httptest.Server`, temporary directories,
and buffer-backed terminal sessions. Normal tests require no live OpenAI
credential.
