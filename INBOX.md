# Subagent completion inbox plan

## Goal

Replace pull-based result retention with a session-owned completion inbox. A terminal subagent must no longer depend on the model remembering an ID and calling `subagent_wait` to release its result.

The design must guarantee that:

- an undelivered completion survives provider compaction;
- the next parent generation receives every available completion;
- a parent blocked in `subagent_wait` wakes when a completion arrives;
- a completion racing with a final answer is either delivered before settlement or retained for the next user turn;
- completed jobs immediately stop consuming concurrency slots;
- results are never silently expired.

Use at-least-once delivery with stable message IDs. Exactly-once delivery is not required; duplicate delivery after an interrupted checkpoint is preferable to losing a result.

## Chosen model

Split subagent state into two stores:

1. **Active jobs**: running, finalizing, or canceling child executions.
2. **Completion inbox**: immutable terminal messages waiting for a successful parent generation.

A terminal transition atomically removes the job from the active set, appends one completion message to the inbox, publishes status, and wakes waiters. There is no retained terminal job and no separate consume operation.

A completion message contains:

- a monotonic message sequence;
- subagent ID and task description;
- terminal status: complete, failed, or canceled;
- bounded final result or error text.

Capacity is based only on active jobs. The inbox is naturally bounded by the number of children that can finish between parent generations; additionally, each message and each delivered batch must use deterministic byte and line limits.

## Tool behavior

### `subagent`

Keep asynchronous launch, but update its description to state that terminal results arrive automatically as inbox messages. Returned IDs are for status and cancellation, not result retrieval.

### `subagent_wait`

Redesign it as a synchronization-only tool with no IDs:

- Return immediately when the inbox already contains a completion.
- Otherwise block until any active subagent becomes terminal.
- Return an error immediately when there are neither active jobs nor pending completions.
- On wake, return only a short acknowledgement. The result itself is delivered through the inbox before the next parent generation.
- Canceling the wait call must not cancel child jobs; child cancellation remains explicit.

Waiting for the next completion rather than selected IDs makes the operation recoverable after compaction and lets the parent process results incrementally. The model can call it again while active jobs remain.

When the parent has no independent work left, the normal flow is:

1. The model calls `subagent_wait`.
2. The tool blocks.
3. A child atomically queues its result and wakes the tool.
4. The tool returns its acknowledgement.
5. The engine compacts if necessary, then attaches the queued result.
6. The next parent generation sees both the wait acknowledgement and the completion message.

### `subagent_cancel`

Cancel active jobs only. Once a job reaches terminal state, its completion is already in the inbox and cannot be canceled. A confirmed cancellation also produces one terminal inbox message.

## Model-visible protocol

Add a dedicated `agent.InputInbox` input kind instead of representing completions as tool results. Encode a batch in a clearly delimited structured envelope, for example:

```text
<subagent_notifications>
[{"message_id":7,"subagent_id":"subagent-2","task":"inspect compaction","status":"complete","result":"..."}]
</subagent_notifications>
```

The system prompt should explain that these are system-generated, untrusted research results and that relevant findings should be incorporated before finishing.

Before every parent generation, also render a small dynamic status section containing active subagent IDs, descriptions, and states. Generate this from current runtime state rather than storing it in provider history. This lets the model recover cancellation targets and know whether another wait is useful after any compaction.

## Engine integration

Define a narrow inbox interface in `agent` and have the subagent coordinator implement it. Pass it to `agent.Engine` through `agent.Options`; `agent` must not import `tool`.

The interface needs operations equivalent to:

- open delivery for a parent run;
- peek a stable pending batch without removing it;
- acknowledge only the messages included in a successful generation;
- atomically check pending messages while closing the settlement window;
- render current active-job context;
- signal/wait for inbox changes for `subagent_wait`.

### Generation pipeline

For each generation:

1. Build the ordinary request from provider state and user/tool inputs.
2. Snapshot the pending inbox batch without removing it.
3. Use the combined size when deciding whether compaction is needed.
4. If compaction is needed, compact the ordinary conversation first; do not consume or rely on the compactor to preserve the undelivered inbox batch.
5. Attach the same inbox batch to the post-compaction request as `InputInbox`.
6. Generate the parent response.
7. After a successful response state has been accepted, acknowledge exactly the attached message IDs.

If generation fails, leave the batch pending. Context-limit recovery must compact the ordinary request and then reattach the same inbox batch before retrying.

Once a successful generation has seen a message, its input is represented in the returned provider state and the inbox copy can be removed. Compaction before first delivery cannot erase it because it remains outside provider state.

### Settlement race

At the no-tool-call settlement point, perform one atomic inbox operation:

- If a completion is pending, keep the run open and perform another generation with it.
- If none is pending, close inbox delivery for that run and settle.

A completion that acquires the inbox lock before closure forces another generation. One arriving after closure remains queued for the next user turn. Do not automatically start a new parent turn while idle.

Completions that arrive during a tool batch are delivered on the generation following that batch. Completions that arrive during generation are delivered on the next loop iteration or caught by the settlement check.

## Subagent coordinator refactor

In `tool/subagent.go`:

- Keep only active jobs in the job map.
- Add the ordered completion inbox and a monotonic message sequence.
- Move terminal-state publication into one locked transition that removes the active job and queues its completion.
- Remove `jobsForIDs` for waiting, terminal snapshots, `consume`, and completed-job capacity accounting.
- Keep explicit cancellation and session shutdown for active workers.
- Publish UI status from active jobs plus inbox entries awaiting delivery.

In `tool/subagent_wait.go`:

- Remove the ID schema and result formatting.
- Wait on the coordinator change signal without claiming inbox messages.
- Do not propagate a canceled wait into child cancellation.

Update `tool/subagent_cancel.go` and all tool descriptions for the new lifecycle.

## Provider and compaction changes

In `agent/provider.go` and `backend/openai/codex/request.go`:

- Add and validate `InputInbox`.
- Encode it as a model-visible message, never as `function_call_output`.
- Keep its structured marker intact during normal state replay.

In `agent/engine.go`:

- Attach inbox messages only after automatic or manual compaction preparation.
- Reattach them after error-triggered compaction.
- Acknowledge them only after successful model visibility.

Provider compaction may summarize or discard inbox messages that were already delivered. It must never be the durability mechanism for messages that have not yet reached a parent generation.

## Checkpoints and session lifecycle

Store pending inbox messages and their sequence high-water mark in a subagent-owned checkpoint composed into the session record, not in `agent.Checkpoint`. Replace the affected session/checkpoint schemas with new versions and reject superseded stored sessions; do not add migrations or legacy decoding. Restore pending completions into the newly created coordinator before the next run.

Running goroutines are process-local and are not resumable. On a restored session, any checkpointed active-job descriptors should become terminal `interrupted` notifications rather than appearing to still run. Preserve the subagent ID high-water mark so restored sessions cannot reuse historical IDs.

Ensure an inbox transition marks session checkpoint state dirty. If the parent is active, the next normal composed session checkpoint captures it; if the parent is idle, the session checkpoint coordinator should save it directly. Graceful shutdown should cancel and join active children before the final composed checkpoint is saved.

## UI behavior

Replace the ambiguous completed-job display with two explicit categories:

- active subagents;
- results awaiting parent delivery.

An awaiting-delivery entry disappears after a successful parent generation acknowledges its inbox message. It does not count toward launch capacity.

## Implementation sequence

1. **Coordinator state machine**
   - Add completion messages, sequencing, atomic terminal transfer, bounded formatting, and active-only capacity.
   - Rewrite manager unit tests around the new invariants.

2. **Wait and cancellation tools**
   - Make wait argument-free and notification-based.
   - Decouple wait cancellation from child cancellation.
   - Update schemas, descriptions, presentations, and tests.

3. **Engine inbox delivery**
   - Add the inbox interface and `InputInbox`.
   - Integrate post-compaction attachment, acknowledgement, and the atomic settlement gate.
   - Add dynamic active-subagent context.

4. **Codex provider support**
   - Encode inbox inputs as structured messages.
   - Cover normal replay, automatic compaction, and context-error compaction.

5. **Session, checkpoint, and UI wiring**
   - Pass the coordinator to the engine.
   - Persist/restore inbox state and ID high-water marks.
   - Save idle completion transitions and update status rendering.

6. **Remove the old protocol**
   - Replace affected APIs and schemas atomically across all callers.
   - Delete selected-ID wait collection, terminal job retention, consume semantics, old fixtures, and compatibility tests.
   - Remove any prompt text implying that wait returns results.
   - Do not add aliases, forwarding adapters, dual schemas, migrations, feature flags, or fallback decoding.

## Required tests

- A terminal child leaves the active map, frees capacity, and queues exactly one bounded message.
- Complete, failed, and canceled children all produce the correct envelope.
- Wait blocks with active jobs, wakes on the first terminal transition, and does not drain the message.
- A canceled wait leaves children running.
- A pending completion is included in the next parent generation and acknowledged afterward.
- A failed generation does not acknowledge its inbox batch.
- Launch → completion → automatic compaction still delivers the result.
- Launch → blocked wait → completion → compaction delivers the result after wait returns.
- Context-limit recovery reattaches the same undelivered batch.
- A completion arriving before the settlement gate forces another generation.
- A completion arriving after settlement remains queued for the next user turn.
- Multiple simultaneous completions are delivered in sequence order in one bounded batch.
- Completed results do not consume any of the four active slots.
- Checkpoint round trips preserve pending messages and prevent ID reuse.
- Restore converts formerly active jobs into interrupted notifications.
- Session orchestration covers independent work followed by wait and synthesis.

Run focused package tests, `go test ./...`, and the relevant race-enabled tests for terminal transition, wait, acknowledgement, and settlement interleavings.

## Explicit decisions

- Do not add a TTL or automatic expiration. Automatic delivery and active-only capacity remove the reason for it, and silent result loss is worse than bounded retention.
- Do not make `subagent_wait` a result transport.
- Do not require the model to acknowledge or consume completed IDs explicitly.
- Do not wake an idle parent model automatically.
- Do not preserve the existing wait schema, output, checkpoint/session-record versions, package APIs, or completed-job semantics.
- Reject superseded persisted formats instead of migrating or interpreting them.
