# TODO

## Inline image attachments

### Terminal editor model

- [x] Replace the parallel `input []rune` and `images []agent.Image` fields in `terminal/tui_model.go` with one ordered editor-item slice. Model each rune, attached image, and pending clipboard image as one item, with `cursor` remaining a boundary between items.
- [x] Add focused projections from editor items:
  - Plain text for slash commands, steering, history, descriptions, and text byte limits.
  - Ordered text/image content for submission, coalescing adjacent runes into text parts without inserting or trimming whitespace.
  - Inline display content using one `[image attached]` marker per image.
- [x] Make all editing and navigation item-aware:
  - Insert completed or pending images at the cursor.
  - Move Left/Right across an image in one step.
  - Make Backspace remove the item before the cursor and Delete remove the item after it, including adjacent images one at a time.
  - Update Home/End/Up/Down and newline handling so images participate in logical lines as single editor items.
  - Update clear, empty-input, Ctrl-C, Ctrl-D, image-count, image-byte, and text-byte checks for the unified representation.
- [x] Adapt `terminal/tui_commands.go` and `terminal/tui_file_picker.go` to use text projections while mapping replacement ranges back to editor-item indexes. Images should terminate command/file tokens rather than becoming text.
- [x] Keep prompt history text-only, but preserve and restore the complete mixed draft while temporarily navigating history.

### Clipboard lifecycle

- [x] Reserve a pending image item at the current cursor as soon as Ctrl-V is pressed so later typing remains after that attachment even if the clipboard read is slow.
- [x] Model clipboard loading as explicit controller state with a request ID, the originating pending item/editor generation, and a cancel function. Replace only that pending item when the matching read completes.
- [x] Remove and cancel pending reads when their item is deleted, the draft is cleared or submitted, or the TUI exits. Discard stale completions so they cannot attach to a later prompt or overwrite the activity of a running turn.
- [x] Make clipboard completion delivery cancellation-aware so a custom reader cannot leave a goroutine blocked after shutdown.

### Rendering and transcript

- [x] Rewrite `renderInput` in `terminal/tui_render_input.go` to render image markers at their actual editor positions instead of prepending an aggregate attachment line.
- [x] Treat each marker as an atomic wrapping unit, account for its terminal cell width, and keep distinct cursor positions before and after it, including in narrow terminals.
- [x] Preserve the same inline order in submitted user conversation blocks and restored transcripts. Replace the aggregate `imageCount` presentation with ordered display content, or include all ordered image metadata in `conversationBlocksEqual` if a separate representation remains.

### Ordered agent and provider content

- [x] Add an ordered user-content representation in `agent/provider.go` with text and image parts. Retain `Input.Text` where needed for tool results and existing text-only compatibility, and remove the parallel `Input.Images`/`ImageAttachments` shape.
- [x] Deep-copy image bytes when content enters the engine, when conversation state is cloned/checkpointed, and at the provider ownership boundary.
- [x] Replace the terminal's parallel `Run`/`RunWithImages` branch with one ordered-content submission path. Keep `Run(string, ...)` as the text-only convenience used by existing callers.
- [x] Update skill-command expansion to operate without collapsing, reordering, or losing image parts.
- [x] Update `backend/openai/codex/request.go` to encode ordered content as Codex parts in exact sequence, for example `input_text`, `input_image`, `input_text`, `input_image`. Do not synthesize spaces or trim text around images.
- [x] Preserve the current Codex wire shape for text-only input and multipart behavior for image-only input, while retaining ordered parts in continuation state.
- [x] Update Codex input-token estimation to include text from the ordered content representation.

### Checkpoints and recovery

- [x] Serialize and clone ordered pending user content, including image bytes, in agent checkpoints while retaining compatibility with existing version-one text-only checkpoints.
- [x] Persist inline marker positions for submitted terminal conversation blocks without duplicating submitted image bytes in the terminal checkpoint.
- [x] Define and enforce draft attachment semantics. If idle terminal checkpoints continue to omit unsent image bytes, project the draft to text plus a valid rune-relative cursor and test that behavior explicitly.
- [x] Eliminate the active-checkpoint window where the terminal records image markers before the agent checkpoint contains the submitted content. Persist the submission atomically or avoid restoring markers without corresponding agent state; cover both interruption windows.

### Tests and verification

- [x] Add terminal model/reducer tests for text-image-text insertion, adjacent images, cursor movement, Backspace/Delete, multiline navigation, image-only submission, limits, clear/exit behavior, history, commands, and file completion.
- [x] Add controller tests for preserving the Ctrl-V insertion point, typing while a read is pending, deleting/clearing/submitting pending items, stale completions, and shutdown cancellation.
- [x] Add rendering tests for exact inline order, wrapping, narrow widths, cursor positions around images, submitted turns, restored turns, and conversation-cache invalidation.
- [x] Add agent tests for exact ordered content, image ownership/deep copies, skill expansion, and checkpoint round trips.
- [x] In Codex tests, unmarshal the generated message and assert part order and payloads by index; retain explicit text-only and image-only regressions.
- [x] Run `gofumpt` on changed Go files, focused package tests, and `go test ./...`.

## Other before merging

- [x] Reserve Codex continuation-state headroom for response output. Compact before generation when existing state plus pending input would consume the response budget, reject responses that still cannot fit, and cover the recovery path end to end.
- [x] Validate that clipboard bytes are actually a PNG before labeling and submitting them as `image/png`. Replace tests that treat the string `"png"` as a valid image.
- [x] Preserve substantive `wl-paste`/`xclip` execution errors instead of reporting every helper or display failure as an empty clipboard.
