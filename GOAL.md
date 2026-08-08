# Goal

Implement one process-local autonomous goal for Yaah, verified by focused engine, tool, terminal, and session-wiring tests plus the full Go test suite, while preserving existing provider continuation, tool-result, steering, cancellation, compaction, and command behavior.

## Required behavior

- `/goal <objective>` sets or replaces the current goal and starts pursuing it.
- Bare `/goal` shows the current goal, or reports that none is set.
- `/goal clear` removes the goal.
- Do not add goal pause, resume, status, statusbar, token-budget, persistence, or history commands.
- Existing non-goal commands remain unchanged.
- The main model can call `update_goal({"status":"complete"})` only when the active goal is complete.
- The completion tool is available only to the main agent, not read-only subagents.
- While a goal is set, the engine continues autonomously whenever it would otherwise settle.
- Steering remains FIFO and takes priority over goal continuation.
- Steering is eligible after a complete tool batch and before settlement; goal continuation is eligible only before settlement.
- All tool calls must receive correlated results before steering or goal input is added.
- `/goal clear` must work while the engine is running and prevent another goal continuation at the next safe settlement boundary without interrupting an already executing tool batch.
- Cancellation or an execution error ends the current run without clearing the configured goal. `/goal clear` removes it permanently for the process.
- `/clear` continues to reset conversation state and also clears the configured goal.

## Architecture

- Add a narrow internal continuation arbiter rather than a public plugin or general lifecycle-hook framework.
- The arbiter owns run-local steering and the single process-local goal, and atomically chooses the next continuation at fixed engine safe points.
- Priority before settlement is: required tool results, oldest steering message, active goal continuation, then settle.
- After a tool batch, only steering may be selected.
- Steering and goal input share one transactional delivery path: append the provider-neutral user input, emit a distinct event, and roll back the input if event delivery fails.
- Keep `EventSteering` and add a distinct goal-continuation event so the TUI does not attribute autonomous input to the user or disturb steering queue accounting.
- Reuse ordinary `InputUser` plus opaque provider state; do not change provider request/response contracts.

## Scope and boundaries

- Keep goal state in memory; do not add session persistence.
- Do not add token accounting or token budgets.
- Do not add multiple goals or subagent goal inheritance.
- Do not add generic command registration, dynamic plugin hooks, provider middleware, or speculative configuration.
- Keep changes focused in the agent continuation lifecycle, the completion tool and main-session wiring, and terminal command/presentation handling.

## Verification

Tests must demonstrate:

1. Ordinary runs without a goal settle exactly as before.
2. An active goal causes another provider generation using the prior opaque state and a generated continuation prompt.
3. Accepted steering is delivered FIFO before a goal continuation.
4. Steering is delivered only after every tool result in a complete tool batch; goal continuation is not selected after a tool batch.
5. `update_goal({"status":"complete"})` stops autonomous continuation and rejects invalid or inactive completion attempts.
6. `/goal <objective>`, bare `/goal`, and `/goal clear` have the agreed behavior, including clear while running.
7. Sink failure rolls back unseen continuation input without losing completed response state or tool results.
8. Cancellation and provider/tool errors do not start another goal generation and do not silently clear the configured goal.
9. `/clear` clears both conversation state and the goal.
10. Main-session wiring includes the completion tool while subagents remain unchanged.

Run `gofumpt` on every changed Go file, focused tests for changed packages, `go test ./...`, `go test -shuffle=on ./...`, `go vet ./...`, and `git diff --check`.

## Iteration and stop policy

After each failing test or review finding, identify whether it concerns continuation ordering, checkpointing, command behavior, tool wiring, or presentation; fix the smallest underlying cause and rerun the affected focused tests before the full suite. Do not broaden the architecture to solve hypothetical future extension needs. If the provider protocol, existing steering guarantees, or terminal lifecycle cannot support a requirement without a materially larger design, stop with the exact conflict, evidence gathered, attempted approach, and the decision needed.
