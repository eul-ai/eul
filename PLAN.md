# Incremental Session Persistence Plan

## Goals

- Stop serializing and rewriting the full terminal transcript on every session checkpoint.
- Preserve the current checkpoint boundaries, active/idle recovery semantics, locking, permissions, and empty-session behavior.
- Keep the format in the standard library and make a clean break from monolithic session JSON files.

## Final layout

```text
~/.config/eul/sessions/<workspace-hash>/<session-id>/
├── lock
├── state.json
├── transcript-a.jsonl
└── transcript-b.jsonl
```

`state.json` is the commit point. It stores session metadata, the complete agent and subagent checkpoints, terminal state excluding conversation blocks, and the active transcript slot with its committed byte offset and block count.

Each transcript record replaces a suffix of the prior committed block list:

```json
{"replace_from":15820,"blocks":[...]}
```

The inactive transcript slot is used only to publish a compacted transcript safely.

## Checkpoints

- [x] **Checkpoint 1: terminal persistence primitives**
  - Split a terminal checkpoint into transcript blocks and non-transcript state.
  - Reassemble a complete terminal checkpoint from those parts.
  - Compute and apply suffix-replacement transcript deltas without persistent block IDs.
  - Keep encoding and validation owned by `terminal`.
  - Add focused round-trip, append, replacement, truncation, no-op, and malformed-delta tests.
  - Run `gofumpt` and focused `terminal` tests.
  - Commit the checkpoint.

- [x] **Checkpoint 2: incremental session store**
  - Replace monolithic session files with per-session directories.
  - Add compact `state.json` encoding containing metadata, agent/subagent checkpoints, terminal state, and transcript head information.
  - Append and sync transcript deltas before atomically replacing and syncing `state.json`.
  - Restore by replaying exactly the committed transcript prefix.
  - Port create/open/list/find/lock/empty-session and permission behavior to the new layout.
  - Ignore old `<session-id>.json` records; add no migration or fallback.
  - Preserve the existing `sessionHandle.Record` and `Save` facade where practical.
  - Add crash-boundary tests for uncommitted tails and invalid committed prefixes.
  - Run `gofumpt` and focused `app` and `terminal` tests.
  - Commit the checkpoint.

- [x] **Checkpoint 3: two-slot transcript compaction**
  - Track base and delta byte counts in `state.json`.
  - Compact when accumulated delta bytes reach the canonical base size.
  - Write the canonical transcript to the inactive slot, sync it, then atomically switch `state.json`.
  - Ensure interrupted compaction always leaves the previously selected slot recoverable.
  - Reclaim the old inactive slot only after a successful state commit.
  - Add compaction equivalence and interrupted-switch tests.
  - Run `gofumpt` and focused `app` tests.
  - Commit the checkpoint.

- [ ] **Checkpoint 4: integration, cleanup, and performance verification**
  - Port persistence and lifecycle tests that inspect the old file directly.
  - Preserve active-before-launch, failed-checkpoint, interrupted-tool, and subagent recovery behavior.
  - Replace obsolete full-record benchmarks with state-only save, one-block append, replay, and listing benchmarks.
  - Remove monolithic record encoding/decoding and obsolete compatibility tests/helpers.
  - Verify state-only saves do not append transcript bytes and one-block saves write only the suffix record.
  - Run `gofumpt` on all changed Go files.
  - Run focused package tests, `go test ./...`, and `staticcheck ./...`.
  - Commit the checkpoint.

## Persistence protocol

1. Capture and validate the next agent, subagent, and terminal checkpoints.
2. Split terminal state from transcript blocks and compute the transcript suffix delta.
3. Append and sync a delta only when the transcript changed.
4. Encode the complete next `state.json` with the new committed transcript offset.
5. Write and sync a temporary state file, rename it over `state.json`, and sync the session directory.
6. Update in-memory committed state only after the directory sync succeeds.
7. Treat bytes beyond the offset in the previous `state.json` as uncommitted and ignore or truncate them on recovery.

## Acceptance criteria

- A state-only save does not modify the transcript.
- Appending one block writes only one suffix delta, independent of prior transcript bytes.
- Session listing reads no transcript data.
- Restore reproduces the exact committed terminal, agent, and subagent state.
- Crashes during append, state replacement, or compaction recover either the previous or next complete state.
- Existing active/idle and checkpoint-failure semantics remain intact.
- The committed journal remains bounded through two-slot compaction.
