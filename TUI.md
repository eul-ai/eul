# Full-screen TUI plan

## Objective

Replace the interactive line REPL with a minimal full-screen terminal interface made of:

1. a scrollable conversation window;
2. an expanding multiline input area; and
3. a status bar with an activity indicator.

Use `golang.org/x/term` for terminal detection, raw mode, restoration, and dimensions. Do not add a TUI framework. Keep one-shot execution line-oriented; interactive execution requires the full TUI.

The initial targets remain macOS and Linux with an ANSI-compatible terminal.

## Scope

The first version will support:

- user entries plus streamed assistant, reasoning, tool, compaction, and error entries;
- an editable prompt with command history;
- scrolling through the conversation;
- resize handling;
- `/help`, `/clear`, and `/exit`;
- Ctrl-C cancellation with the existing reset behavior;
- a visible status for thinking, responding, compacting, tools, cancellation, errors, and readiness; and
- safe terminal restoration on every normal return path.

The first version will not include:

- Markdown beyond inline bold, italic, and code spans, or syntax highlighting;
- mouse support;
- autocomplete;
- multiple panes, sessions, or dialogs;
- queued prompts while a turn is active;
- exact grapheme-cluster editing for every emoji sequence;
- a line-oriented interactive REPL for redirected streams;
- Windows support; or
- a reusable widget or terminal framework.

## Mode selection and terminal requirement

The CLI selects a mode from its prompt argument and stdin:

- a prompt argument selects `RunOneShot`, which preserves its current stdout/stderr behavior;
- without a prompt argument, non-terminal stdin is read to EOF as one prompt and passed to `RunOneShot`; and
- without a prompt argument, terminal stdin selects `terminal.Run`, which always means the full-screen TUI.

Piped and file-redirected prompts therefore use one-shot mode automatically:

```sh
printf 'explain this package' | yaah --model gpt-5.6-sol
yaah --model gpt-5.6-sol < prompt.txt > answer.txt
```

A piped prompt must be non-empty, valid UTF-8, contain no NUL, and remain within the input size limit. A prompt argument takes precedence if both an argument and piped stdin are present.

`terminal.Run` will require both its input and UI output to expose file descriptors accepted by `term.IsTerminal`. After mode selection, terminal stdin with redirected output is an error because there is neither a screen for the TUI nor a prompt to run one-shot. There is no redirected interactive REPL.

The TUI will use one output stream for the entire frame; engine events must not write independently to stdout or stderr while it is active.

A failure after TUI setup begins must restore terminal state before returning an error. It must not attempt to switch to another interface after partially entering raw or alternate-screen mode.

## Layout

The normal layout reserves the last four rows for the input bar, its two horizontal rules, and the status bar. All remaining rows form the conversation viewport.

```text
Explain terminal raw mode.

Raw mode provides key presses without canonical line buffering...

────────────────────────────────────────────────────────────
 > write the next prompt here█
────────────────────────────────────────────────────────────
 ⠋ thinking                    gpt-5.6-sol (xhigh) · context 31%
```

The input area sits between horizontal rules in the style of Claude Code and Pi. The rules may be omitted when space is tight. The renderer will degrade by clipping content and prioritizing, in order:

1. the status and activity state;
2. the input area;
3. its horizontal rules; and
4. the conversation.

The base canvas preserves the terminal's background instead of painting the theme background across the alternate screen. The conversation viewport has one blank row at the top and bottom. Block backgrounds span the full width while their text has a one-cell horizontal inset. User and assistant text use the base background, reasoning summaries use muted italic text with balanced space above and below, and compact tool blocks use horizontal and vertical padding with pending, success, or error backgrounds. Assistant and reasoning blocks render `**bold**`, `*italic*`, and backtick-delimited inline code; other block types preserve the markers literally. Inline emphasis and code spans compose when nested, and code uses the theme's `mdCode` foreground. Role labels are omitted. Input rules use the theme color for the selected thinking level.

On narrow terminals, the status bar will preserve the activity label and compact context percentage before truncating the model and thinking level.

## Conversation model

The UI will retain semantic blocks instead of pre-rendered terminal lines. Blocks will be wrapped again whenever the terminal width changes.

Block types are:

- user prompt;
- assistant response;
- reasoning summary;
- tool activity;
- context/compaction notice; and
- error or informational notice.

Assistant and reasoning deltas append to the currently open block of the same type. A transition to a tool, compaction, user, or other block closes the previous streaming block. Tool calls and results use the existing concise summaries rather than raw arguments or output. A result updates its pending tool block instead of creating a second block.

Reasoning, tool, and context blocks should be visually subdued but remain readable. This preserves information currently written to stderr instead of hiding it in the status bar.

The viewport follows new output while positioned at the bottom. If the user scrolls upward, streaming continues without moving the viewport; returning to the bottom resumes automatic following.

All untrusted text must pass through the existing control-character sanitization before rendering. Only the renderer may emit ANSI escape sequences.

## Input behavior

The editor expands vertically for explicit newlines and soft-wrapped text while preserving space for the status, rules, and at least one conversation row.

| Key | Behavior |
| --- | --- |
| Enter | Submit non-empty input |
| Shift-Enter | Insert a newline |
| Shift-Tab | Cycle through the model-supported thinking levels from off, minimal, low, medium, high, xhigh, and max |
| Left/Right | Move by rune |
| Home/End | Move to start/end |
| Backspace/Delete | Delete adjacent rune |
| Up/Down | Navigate submitted prompt history |
| Page Up/Page Down | Scroll the conversation |
| Ctrl-L | Force a complete redraw |
| Ctrl-C | Clear non-empty input, cancel the active turn, or exit when idle input is empty |
| Ctrl-D | Exit when the input is empty |

Raw mode turns Ctrl-C into an input event rather than a terminal-generated SIGINT. The TUI pushes Kitty keyboard-protocol disambiguation mode so modified Enter and Tab remain distinguishable, accepts optional Kitty event and alternate-key subparameters, and pops the mode during cleanup.

Bracketed paste will be enabled so pasted escape sequences and newlines are handled as input rather than terminal commands. Newlines in a paste remain normalized to spaces; Shift-Enter creates intentional editor line breaks. Input remains subject to `maxInputBytes`, valid UTF-8, and NUL rejection.

The editor is disabled during an active turn. Scrolling and cancellation remain available. The submitted prompt is added to the conversation immediately and the editor is cleared.

Commands retain their current meaning:

- `/help` adds command help to the conversation;
- `/clear` resets the engine and clears the visible conversation;
- `/exit` exits the TUI; and
- unknown commands add a sanitized error entry without invoking the engine.

## Status bar

The status bar contains three items: model with thinking level, context usage, and the activity indicator. Activity is left-aligned; model and context are right-aligned. Active states use a small spinner driven by a ticker; the spinner uses the Ayu Mirage accent shared with tool activity while its label remains muted. Idle and error states are static.

Context usage is the latest provider-reported context token count as a percentage of the selected model's context window. Known models may also show the compact count on wider terminals. Before the first response it is zero; `/clear` resets it to zero. Unknown context-window sizes display the token count without a percentage.

The CLI must pass the current thinking level, supported model levels, a thinking-level update callback, and model context-window metadata into the terminal options. The agent will emit a context-usage event after each provider response so the status reflects the current context rather than the cumulative usage returned for the whole turn. The provider-neutral default is `medium`.

| Trigger | Displayed state |
| --- | --- |
| TUI ready | `ready` |
| Prompt submitted | `thinking` |
| Reasoning delta | `thinking` |
| Assistant text delta | `responding` |
| Compaction starts | `compacting context` |
| Compaction completes | `thinking` |
| Tool starts | concise tool description such as `running bash` or `reading repl.go` |
| Tool ends | `thinking` |
| Cancellation requested | `canceling` |
| Turn succeeds | `ready` |
| Recoverable turn failure | `error` plus a truncated diagnostic |

Tool details will be produced from `summarizeCall` and clipped to available status width. Tool failures remain conversation entries even when the agent can continue.

The current `EventCompaction` notification only marks the beginning of compaction. Split it into explicit start and end events so the status does not continue to claim that compaction is occurring while the provider is generating the next response.

## Event loop

One UI event loop will own all mutable UI state and all terminal writes. Other goroutines communicate with it through typed events:

- decoded key or paste events;
- `agent.Event` values;
- turn completion and error;
- Ctrl-C or external interrupt;
- terminal resize; and
- spinner tick.

A turn runs in a goroutine with its own cancelable context. Its `agent.EventSink` sends events to the UI loop instead of rendering directly. The UI loop applies the existing interruption rules:

- the first interrupt during a turn requests cancellation and changes the status to `canceling`;
- further interrupts are ignored until the engine returns;
- an incomplete tool turn resets the engine and adds the existing cleared-conversation notice; and
- an interrupt while idle exits with `ErrInterrupted`.

Parent context cancellation cancels the turn, restores the terminal, and returns the context error without adding a recoverable error entry.

## Terminal lifecycle

The TUI session will:

1. obtain input and output file descriptors;
2. verify both with `term.IsTerminal`;
3. record dimensions with `term.GetSize`;
4. enter raw mode with `term.MakeRaw`;
5. enter the alternate screen, enable bracketed paste, and configure cursor visibility;
6. run the event loop;
7. end synchronized output, disable bracketed paste, show the cursor, and leave the alternate screen; and
8. restore the saved terminal state with `term.Restore`.

Cleanup must be registered immediately after each successful setup step. Screen updates should be buffered into a `strings.Builder`, wrapped in DEC synchronized-output sequences, and written in one operation so resize and streaming updates do not interleave or visibly tear.

`golang.org/x/term` does not provide resize notifications. A small Unix-specific signal adapter will subscribe to `SIGWINCH` and send resize events to the UI loop, which then calls `term.GetSize` and performs a complete redraw.

## Rendering and text width

The renderer builds a complete logical frame from UI state and compares its rendered rows with the previous frame. It writes each changed row once, places the cursor explicitly, and sends the update as synchronized output. Ordinary streaming and spinner updates do not clear or repaint unchanged rows. Resize and Ctrl-L invalidate the previous frame and perform a synchronized complete redraw.

The implementation will use only `golang.org/x/term` as a new direct dependency. A small local cell-width helper will cover ordinary runes, combining marks, and standard wide-character ranges needed for wrapping and clipping. Input movement remains rune-based, so complex joined emoji may require multiple key presses. A dedicated grapheme-width dependency can be considered later only if this limitation proves material.

The current theme is a minimal Go representation of [Ayu Mirage](https://github.com/iodic/pi-ayu-themes/blob/main/themes/ayu-mirage.json). Only the core palette and state colors needed by the terminal UI are retained; the source link remains next to the theme definition for future expansion.

## Proposed files

Keep the public entry points and one-shot event renderer in `terminal/repl.go`, remove the interactive line REPL after the TUI reaches feature parity, and add focused files along these lines:

```text
terminal/tui.go          terminal setup, teardown, and event loop
terminal/tui_model.go    conversation, editor, viewport, and status state
terminal/tui_input.go    raw byte and escape-sequence decoding
terminal/tui_render.go   clipping and ANSI frame generation
terminal/markdown.go     inline emphasis parsing and styled wrapping
terminal/theme.go        current terminal theme colors
terminal/resize_unix.go  SIGWINCH subscription for macOS and Linux
```

The final names may be adjusted while implementing, but input decoding, state transitions, and rendering should remain independently testable without a real terminal.

## Implementation sequence

### 1. Establish mode selection and TUI-only interactive mode

- Add `golang.org/x/term`.
- Route non-terminal stdin without a prompt argument to one-shot mode.
- Require usable terminal input and output file descriptors in `terminal.Run` and return a clear error otherwise.
- Keep `RunOneShot` and its output behavior unchanged.
- Remove the interactive line reader once its commands and interruption behavior are represented in the TUI.

### 2. Add terminal lifecycle and static layout

- Enter and restore raw/alternate-screen modes.
- Read terminal dimensions and handle `SIGWINCH`.
- Render static conversation, input, and status regions.
- Guarantee cleanup for setup errors, EOF, commands, interrupts, and context cancellation.

### 3. Add model, wrapping, and scrolling

- Store semantic conversation blocks.
- Wrap and clip them to the viewport.
- Implement bottom-follow and Page Up/Page Down.
- Rewrap on resize.

### 4. Add the editor and key decoder

- Decode UTF-8, control keys, CSI key sequences, and bracketed paste.
- Implement cursor movement, deletion, submission, and history.
- Enforce existing input validation and size limits.

### 5. Integrate engine streaming and status

- Route engine events into the UI loop.
- Pass the thinking level, supported model levels, and model context-window metadata to the TUI.
- Emit current context usage after each provider response and reset it with the conversation.
- Add explicit compaction start/end events.
- Implement status transitions and the active spinner.
- Preserve cancellation, error, and reset behavior.

### 6. Documentation and cleanup

- Update `README.md` to replace statements that explicitly exclude raw mode, ANSI rendering, and a full-screen TUI.
- Document interactive controls, the terminal requirement, and one-shot behavior.
- Remove line-REPL code and renderer code that is no longer shared, while keeping one-shot output behavior intact.

## Testing

Most behavior should be tested without a live terminal:

- key decoding, including escape sequences split across reads;
- UTF-8 editing, history, paste normalization, and input limits;
- conversation block transitions and streaming append behavior;
- every status transition, including compaction completion and cancellation;
- model, thinking level, context count, context percentage, and narrow-width status formatting;
- wrapping, clipping, scrolling, and resize at narrow and short dimensions;
- ANSI sanitization of assistant, reasoning, tool, error, and pasted text;
- frame snapshots with cursor placement;
- piped and file-redirected stdin routed to one-shot mode, including empty and invalid input;
- rejection of terminal-input TUI mode when its output is not a terminal;
- terminal restoration when the turn, renderer, or output fails; and
- preservation of the existing one-shot behavior.

A manual smoke test should cover Linux and macOS terminals:

1. start and exit normally;
2. resize repeatedly while streaming;
3. cancel before and during a tool call;
4. trigger `/clear` and `/exit`;
5. paste multiline and non-ASCII text;
6. insert an intentional newline with Shift-Enter, cycle thinking levels with Shift-Tab, and clear input with Ctrl-C;
7. pipe and file-redirect a prompt into one-shot mode, including redirected output;
8. confirm terminal-input TUI mode rejects non-terminal output; and
9. verify terminal echo and cursor state after every exit path.

Final verification:

```text
gofumpt -w <changed Go files>
go test ./...
go test -shuffle=on ./...
go vet ./...
git diff --check
```
