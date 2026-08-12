# TODO

## Before merging

- [ ] Tie each asynchronous clipboard read to the editor state that initiated it. Cancel pending reads when the draft is submitted or cleared, and discard stale completions so an image cannot attach to a later prompt or change the status of a running turn.
- [ ] Reconcile the 10 MiB attachment limit with Codex's 16 MiB continuation-state limit. Base64 expands a 10 MiB image to about 13.3 MiB, so an image plus existing history and response output can fail while saving state after a successful provider response. Preflight the complete backend state, compact first when possible, or reject before sending the request; add an end-to-end size regression test.
- [ ] Cancel an outstanding clipboard read whenever the TUI exits. Make the completion send cancellation-aware so custom readers cannot leave a goroutine blocked after shutdown.
- [ ] Define recoverable checkpoint semantics for attachments. Idle checkpoints currently omit draft images, while the active checkpoint records only an image count before the agent has persisted the submitted bytes. Persist the pending submission atomically or avoid restoring an attachment marker that is absent from agent context; cover both interruption windows in tests.

## Correctness follow-ups

- [ ] Include `imageCount` in `conversationBlocksEqual`; rendering depends on it, but the conversation cache currently treats count-only changes as equal.
- [ ] Validate that clipboard bytes are actually a PNG before labeling and submitting them as `image/png`. Replace the test payload that currently treats the string `"png"` as a valid image.
- [ ] Preserve substantive `wl-paste`/`xclip` execution errors instead of reporting every helper or display failure as an empty clipboard.
- [ ] Decide and enforce ownership at the provider boundary. Image data is cloned when entering and checkpointing the engine, but `agent.Request` exposes the current backing bytes to provider implementations.

## Organization

- [ ] Replace the parallel `Run` and `RunWithImages` paths with one submission value containing text and attachments. This would remove branching from the terminal runner and keep future input types from expanding the engine interface again.
- [ ] Simplify `Input.Images` if possible; the pointer-to-`ImageAttachments` wrapper adds cloning and comparison complexity without representing useful extra state.
- [ ] Keep encoded request/state size policy in the Codex backend rather than coupling terminal tests to backend constants.
- [ ] Model clipboard loading as explicit controller state containing a request ID, originating editor generation, and cancel function instead of a boolean plus an unversioned result event.
