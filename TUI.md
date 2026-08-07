# Full-screen TUI plan

## Objective

Replace the interactive line REPL with a minimal full-screen terminal interface made of:

1. a scrollable conversation window;
2. a single-line input bar; and
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

- Markdown rendering or syntax highlighting;
- mouse support;
- autocomplete;
- multiple panes, sessions, or dialogs;
- queued prompts while a turn is active;
- exact grapheme-cluster editing for every emoji sequence;
- interactive operation through pipes or redirected terminal streams;
- Windows support; or
- a reusable widget or terminal framework.

## Mode selection and terminal requirement

The CLI has two execution modes:

- a prompt argument selects `RunOneShot`, which remains line-oriented and preserves its current stdout/stderr behavior; and
- no prompt argument selects `terminal.Run`, which always means the full-screen TUI.

`terminal.Run` will require both interactive input and UI output to expose file descriptors accepted by `term.IsTerminal`. If either side is not a terminal, it will return a clear error instructing the user to provide a prompt for one-shot mode. Piped prompts and a redirected interactive REPL will not be supported.

The TUI will use one output stream for the entire frame; engine events must not write independently to stdout or stderr while it is active.

A failure after TUI setup begins must restore terminal state before returning an error. It must not attempt to switch to another interface after partially entering raw or alternate-screen mode.

## Layout

The normal layout reserves the last two rows for input and status. All remaining rows form the conversation viewport.

```text
 You
 Explain terminal raw mode.

 Assistant
 Raw mode provides key presses without canonical line buffering...

────────────────────────────────────────────────────────────
 > write the next prompt here█
 ⠋ thinking                         gpt-5.6 · ~/Code/yaah
```

The separator may be omitted when space is tight. The renderer will degrade by clipping content and prioritizing, in order:

1. the status and activity state;
2. the input bar; and
3. the conversation.

The status bar will prioritize the activity label over model and directory metadata on narrow terminals.

## Conversation model

The UI will retain semantic blocks instead of pre-rendered terminal lines. Blocks will be wrapped again whenever the terminal width changes.

Block types are:

- user prompt;
- assistant response;
- reasoning summary;
- tool activity;
- context/compaction notice; and
- error or informational notice.

Assistant and reasoning deltas append to the currently open block of the same type. A transition to a tool, compaction, user, or other block closes the previous streaming block. Tool calls and results use the existing concise summaries rather than raw arguments or output.

Reasoning, tool, and context blocks should be visually subdued but remain readable. This preserves information currently written to stderr instead of hiding it in the status bar.

The viewport follows new output while positioned at the bottom. If the user scrolls upward, streaming continues without moving the viewport; returning to the bottom resumes automatic following.

All untrusted text must pass through the existing control-character sanitization before rendering. Only the renderer may emit ANSI escape sequences.

## Input behavior

The initial editor remains single-line to preserve the current prompt semantics.

| Key | Behavior |
| --- | --- |
| Enter | Submit non-empty input |
| Left/Right | Move by rune |
| Home/End | Move to start/end |
| Backspace/Delete | Delete adjacent rune |
| Up/Down | Navigate submitted prompt history |
| Page Up/Page Down | Scroll the conversation |
| Ctrl-L | Force a complete redraw |
| Ctrl-C | Cancel the active turn, or exit while idle |
| Ctrl-D | Exit when the input is empty |

Raw mode turns Ctrl-C into an input byte rather than a terminal-generated SIGINT, so keyboard Ctrl-C and externally delivered `os.Interrupt` must enter the same cancellation path.

Bracketed paste will be enabled so pasted escape sequences and newlines are handled as input rather than terminal commands. Newlines in a paste will be normalized to spaces for the single-line editor. Input remains subject to `maxInputBytes`, valid UTF-8, and NUL rejection.

The editor is disabled during an active turn. Scrolling and cancellation remain available. The submitted prompt is added to the conversation immediately and the editor is cleared.

Commands retain their current meaning:

- `/help` adds command help to the conversation;
- `/clear` resets the engine and clears the visible conversation;
- `/exit` exits the TUI; and
- unknown commands add a sanitized error entry without invoking the engine.

## Status bar

The status bar contains a left-aligned activity indicator and optional right-aligned session metadata. Active states use a small spinner driven by a ticker; idle and error states are static.

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
7. disable bracketed paste, show the cursor, and leave the alternate screen; and
8. restore the saved terminal state with `term.Restore`.

Cleanup must be registered immediately after each successful setup step. Screen output should be buffered into a `strings.Builder` and written as one frame so resize and streaming updates do not interleave or visibly tear.

`golang.org/x/term` does not provide resize notifications. A small Unix-specific signal adapter will subscribe to `SIGWINCH` and send resize events to the UI loop, which then calls `term.GetSize` and performs a complete redraw.

## Rendering and text width

The renderer will build a complete frame from immutable UI state, clear each occupied row, place the cursor explicitly, and write the frame in one operation. It should avoid clearing the entire terminal between ordinary spinner frames to reduce flicker.

The implementation will use only `golang.org/x/term` as a new direct dependency. A small local cell-width helper will cover ordinary runes, combining marks, and standard wide-character ranges needed for wrapping and clipping. Input movement remains rune-based, so complex joined emoji may require multiple key presses. A dedicated grapheme-width dependency can be considered later only if this limitation proves material.

## Proposed files

Keep the public entry points and one-shot event renderer in `terminal/repl.go`, remove the interactive line REPL after the TUI reaches feature parity, and add focused files along these lines:

```text
terminal/tui.go          terminal setup, teardown, and event loop
terminal/tui_model.go    conversation, editor, viewport, and status state
terminal/tui_input.go    raw byte and escape-sequence decoding
terminal/tui_render.go   wrapping, clipping, ANSI frame generation
terminal/resize_unix.go  SIGWINCH subscription for macOS and Linux
```

The final names may be adjusted while implementing, but input decoding, state transitions, and rendering should remain independently testable without a real terminal.

## Implementation sequence

### 1. Establish the TUI-only interactive mode

- Add `golang.org/x/term`.
- Require usable terminal file descriptors in `terminal.Run` and return a clear non-terminal error otherwise.
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
- wrapping, clipping, scrolling, and resize at narrow and short dimensions;
- ANSI sanitization of assistant, reasoning, tool, error, and pasted text;
- frame snapshots with cursor placement;
- rejection of non-file and non-terminal streams with guidance to use one-shot mode;
- terminal restoration when the turn, renderer, or output fails; and
- preservation of the existing one-shot behavior.

A manual smoke test should cover Linux and macOS terminals:

1. start and exit normally;
2. resize repeatedly while streaming;
3. cancel before and during a tool call;
4. trigger `/clear` and `/exit`;
5. paste multiline and non-ASCII text;
6. redirect one-shot output successfully and confirm interactive mode rejects a non-terminal input or output; and
7. verify terminal echo and cursor state after every exit path.

Final verification:

```text
gofumpt -w <changed Go files>
go test ./...
go test -shuffle=on ./...
go vet ./...
git diff --check
```
