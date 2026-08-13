# Execution plan

This file defines the implementation order across:

- [`AUDIT.md`](AUDIT.md), which identifies broader design and maintainability work;
- [`INBOX.md`](INBOX.md), which specifies the new subagent completion protocol.

The strategy is **targeted prerequisite cleanup, then the inbox, then the remaining audit**. Do not complete the entire audit before fixing subagent delivery, and do not implement the inbox on top of the current cross-package ownership without first cleaning the seams it directly affects.

Each phase should land separately with the full test suite green. Keep mechanical moves separate from behavior changes so failures remain attributable and changes remain reviewable.

Within a phase, replace affected interfaces and schemas atomically. Update every caller, fixture, and test in the same change, then delete the superseded form. Do not introduce forwarding adapters, dual schemas, feature flags, legacy decoders, migrations, or fallback branches to bridge phases. It is acceptable for old stored sessions to become unsupported when their schema is replaced.

## Architectural decision overriding `INBOX.md`

The completion inbox is session-owned, not part of the core agent's durable state.

- A focused subagent package owns active jobs, pending completion messages, ID/message high-water marks, and its checkpoint type.
- The session record composes agent, subagent, and terminal checkpoints.
- `agent.Engine` receives only a narrow delivery/acknowledgement interface and owns the generic model-input protocol needed to expose inbox messages.
- `agent.Checkpoint` remains limited to engine conversation state, usage, pending engine inputs, and goals.

This replaces the `INBOX.md` proposal to add subagent data directly to `agent.Checkpoint`. The remaining inbox behavior and guarantees in that document still apply.

## Phase 1: Replace terminal navigation errors with explicit outcomes

Implement audit item 1 first.

- Make terminal execution return a typed outcome such as exit, new session, or resume session, plus an error for actual failures.
- Move navigation decisions into the session/application loop.
- Close the current session before applying a navigation outcome, and do not navigate if cleanup fails.
- Keep cancellation and interruption distinct from normal navigation.
- Remove `NewSessionRequest`, `ResumeRequest`, and recursive joined-error inspection once callers use typed outcomes.

### Exit criteria

- Normal navigation is not represented as an error.
- Cleanup ordering is explicit and tested for exit, new, and resume paths.
- Session-transition behavior no longer depends on the shape of `errors.Join`.

## Phase 2: Establish only the application seam required by background persistence

Implement the inbox-relevant portion of audit item 2, not the full application-boundary cleanup yet.

- Make `session` the clear owner of session lifecycle and the composed persistent record.
- Separate immutable terminal startup configuration from the minimum command, event-source, and persistence ports needed by the application. Use a few cohesive ports rather than another callback bag or one giant interface.
- Define one checkpoint coordinator that can compose the latest agent, subagent, and terminal snapshots.
- Provide a path for session-owned background state to request an idle checkpoint without starting a model turn.
- Define shutdown ordering so background intake stops, active children are canceled and joined, final state is snapshotted, and then persistence/resources are closed.
- Reduce `newAgentSessionWithCheckpointing` only as far as necessary to expose these ownership boundaries.

Defer package renaming, storage DTO cleanup, account-usage cleanup, and unrelated `terminal.Options` work.

### Exit criteria

- There is one owner for persistent session state.
- A future subagent completion can be saved while the parent model is idle.
- Terminal code does not need to own subagent durability.
- Shutdown has one tested ordering rather than relying on callback/error composition.

## Phase 3: Consolidate the subagent subsystem

Implement audit item 4 and only the subagent portion of audit item 3.

Create a focused `subagent` package that owns:

- model profiles and progress/status types;
- the manager and worker lifecycle;
- finalization policy;
- launch, wait, and cancel tool adapters;
- eventual inbox and checkpoint types.

Separate the current multi-purpose `Subagent` object into a manager and explicit tool adapters. Let `session` supply the child-run function and model/tool policy; it should not participate in the manager's state machine. Move subagent status DTOs out of `agent` and translate them to terminal view state at the application boundary where useful.

This phase should be primarily mechanical. Move the old pull/consume implementation only as source that Phase 4 will replace; do not expose both old and new package APIs, add aliases, or introduce compatibility wrappers. If Phases 3 and 4 cannot be separated without temporary public compatibility machinery, implement them as one clean-break change instead.

### Exit criteria

- Subagent lifecycle state has one package owner.
- `agent` no longer acts as the DTO owner for subagent state.
- Session construction depends on a manager and its adapters rather than private lifecycle details.
- Existing behavior remains covered while files and ownership move.

## Phase 4: Implement the completion inbox

Implement [`INBOX.md`](INBOX.md) on the new boundaries. No backward compatibility is required.

### 4.1 Coordinator and tools

- Split state into active jobs and an ordered completion inbox.
- Atomically transfer each terminal job out of active state and into exactly one bounded completion message.
- Count only active jobs against concurrency.
- Redesign `subagent_wait` as a synchronization tool with no IDs and an optional bounded timeout; it waits for inbox activity and never transports or consumes results.
- Wake a blocked wait for steering, and do not cancel children when a wait is canceled, times out, or is interrupted by steering.
- Keep cancellation explicit; a terminal cancellation produces a completion message like other terminal outcomes.
- Remove selected-ID collection, terminal-job retention, and consume semantics.

### 4.2 Engine delivery and race handling

- Add a narrow inbox source interface to `agent.Engine` without importing the subagent package.
- Add a dedicated generic inbox input kind for model-visible completion envelopes.
- Snapshot pending messages before generation and acknowledge them only after a successful parent response has accepted that input.
- Leave messages pending on generation failure.
- Compact ordinary conversation state first, then attach undelivered inbox messages to the post-compaction request.
- Reattach the same pending batch after context-error compaction.
- Add an atomic settlement gate: a completion that arrives before closure forces another generation; one arriving after closure remains pending for the next user turn.
- Do not wake an idle parent automatically.
- Add dynamic active-subagent context before generations so IDs and states remain recoverable across compaction.

### 4.3 Provider support

- Encode inbox inputs as clearly delimited model-visible messages, never as `function_call_output`.
- Ensure normal replay includes delivered inbox input in returned provider state.
- Never rely on provider compaction to preserve a message that has not yet reached a parent generation.
- Include pending inbox size in compaction decisions.

### 4.4 Persistence and restoration

- Replace the session-record schema with one that composes a versioned subagent checkpoint; bump the record/checkpoint versions and reject superseded stored sessions rather than migrating them.
- Persist pending completion messages plus subagent ID and message-sequence high-water marks.
- Represent process-local active jobs as interrupted completion notifications on restoration rather than pretending their goroutines survived.
- Save terminal transitions while idle through the Phase 2 checkpoint coordinator.
- Use at-least-once recovery: duplication after interruption is acceptable; silent loss is not.

### 4.5 UI and old-code removal

- Show active jobs separately from results awaiting parent delivery.
- Remove an awaiting-delivery entry after successful engine acknowledgement.
- Delete the old wait schema, result formatter, consume paths, compatibility tests, and prompt wording.
- Do not add TTL expiration; automatic delivery and active-only capacity remove the original need.

### Exit criteria

All required tests listed in `INBOX.md` pass, including:

- blocked-wait completion delivery;
- automatic and error-triggered compaction;
- pre-settlement and post-settlement races;
- idle retention;
- failed-generation redelivery;
- active-only concurrency;
- checkpoint restoration and ID non-reuse.

Run focused package tests, race-enabled lifecycle tests, and `go test ./...`.

## Phase 5: Reassess and finish the directly adjacent audit work

Re-read `AUDIT.md` after the inbox lands because package ownership and line references will have changed.

Recommended order:

1. Give steering one authoritative coordinator (audit item 7), now that inbox delivery and settlement have established the other asynchronous input path.
2. Finish the broader application/terminal boundary work from audit item 2.
3. Move the remaining unrelated DTOs out of `agent` from audit item 3, especially provider account usage and model capability lookup.
4. Make tool presentation an optional capability consistently (audit item 8).

Do not mix these changes into the inbox phase unless a concrete blocker requires the minimum relevant piece.

## Phase 6: Complete the independent subsystem cleanup

These items are independent of inbox correctness and should follow it:

1. Separate the LSP service from tool adapters (audit item 5).
2. Split the LSP watcher by responsibility without changing ownership (audit item 6).
3. Clean generic backend/auth/model-profile contracts before adding another provider (audit item 9).
4. Separate skill loading/parsing from engine behavior (audit item 10).
5. Apply flag, naming, duplicate-helper, and test-only cleanup last (audit items 11 and 12).

## Guardrails

- Do not clean the entire codebase before implementing inboxes.
- Do not add inbox behavior to `terminal.Options` as more unrelated callbacks.
- Do not put subagent persistence into `agent.Checkpoint` or subagent domain DTOs into `agent`.
- Do not preserve the old wait/consume protocol, tool schema, checkpoint schema, package API, or stored-session format for compatibility.
- Do not add migrations, legacy decoding, aliases, adapters, feature flags, or fallback handling for superseded forms.
- Prefer separating mechanical package moves from the inbox state-machine rewrite, but combine them when separation would require temporary compatibility infrastructure.
- Do not start unrelated LSP, backend, skill, or naming work before inbox correctness is complete.
- Keep the strengths listed in `AUDIT.md`: small provider capabilities, registry resource ownership, independently versioned checkpoints, and lifecycle-focused tests.
