# Design and Maintainability Audit

## Overall assessment

The codebase has a healthy acyclic dependency graph, clear resource ownership, a lean dependency set, and unusually thorough lifecycle/concurrency tests. The main maintainability cost is not in the core algorithms; it is at the application seams. Session lifecycle, terminal services, subagents, and provider capabilities are represented across several packages, so adding a cross-cutting feature requires coordinated edits in too many places.

Impact below reflects expected change fan-out and long-term maintenance cost, not bug severity.

## High impact

### 1. Replace terminal navigation encoded as errors with an explicit run outcome

- [x] **Completed** — terminal execution returns typed exit/new/resume outcomes, navigation happens after cleanup, and recursive joined-error decoding was removed.

**Files:** `terminal/api.go`, `terminal/tui_controller.go`, `terminal/tui.go`, `interactive/run.go`, `interactive/session.go`, `cmd/eul/main.go`

`/new` and `/resume` are normal lifecycle transitions, but `tuiController.applyAction` returns `NewSessionRequest` and `ResumeRequest` as errors. `agentSession.finish` joins those values with cleanup errors, after which `runSessions` recursively inspects error trees through `onlyNewSessionRequest` and `onlyResumeRequest`. Interruption receives similar recursive treatment in `cmd/eul/main.go`.

This makes expected control flow depend on `errors.Join` structure and forces cleanup semantics into action decoding.

**Recommendation:** make `Runner.Run` return a typed result plus an error, for example an action of `Exit`, `NewSession`, or `ResumeSession` with an optional session ID. Close the current session after receiving that result, and proceed only when cleanup returned no error. Reserve errors for failures and cancellation. This removes `NewSessionRequest`, `ResumeRequest`, both `only...Request` walkers, and their coupling to joined-error shape.

### 2. Establish an explicit application boundary instead of passing an application through `terminal.Options`

- [x] **Completed** — the composition package is now `interactive`; terminal startup config is separated from command, persistence, event, and service ports; storage owns its summary DTO and translates it at the terminal boundary; checkpoint persistence has one coordinator; and parent/child engine options share one policy helper.

**Files:** `terminal/api.go`, `terminal/tui.go`, `terminal/tui_controller.go`, `interactive/session.go`, `interactive/run.go`, `interactive/store.go`, `interactive/network_permission.go`

`terminal.Options` mixes static view configuration, I/O, initial state, mutable commands, persistence callbacks, navigation callbacks, asynchronous event channels, and account-usage loading. Goal commands arrive through `terminal.Engine`, while thinking/fast settings arrive through callbacks, and session navigation and checkpointing arrive through yet more callbacks. `newAgentSessionWithCheckpointing` consequently has to create and bind providers, metadata, usage, permissions, tools, subagents, engine state, persistence, and the entire terminal service bundle in one function.

The package named `interactive` is therefore the real application layer. Its storage and permission code also return terminal-owned types (`terminal.SessionSummary` and `terminal.PermissionRequest`), making TUI concepts visible below the adapter that needs them.

**Recommendation:**

- Treat `interactive` explicitly as the application/composition package; renaming it to `app` or `interactive` would be more honest than presenting it as a reusable session domain.
- Split terminal startup data from behavior: keep an immutable UI config, and pass a small consumer-owned application/controller port for commands and persistence. Group asynchronous sources separately rather than adding more fields to `Options`.
- Have storage return its own summary record and translate it at the terminal adapter.
- Reduce `newAgentSessionWithCheckpointing` to a coordinator over focused helpers for provider capabilities, engine/tool construction, and terminal binding.
- Share one helper for parent and child `agent.Options`; the duplicated construction in `interactive/session.go` and `interactive/subagent.go` is policy-bearing and can drift.

Avoid replacing the callback bag with one giant interface; two or three cohesive ports are sufficient.

### 3. Stop using `agent` as the shared DTO package for unrelated features

- [x] **Completed** — subagent types live in `tool/subagent`; account usage and model metadata capabilities live in `backend`; terminal maps account usage into terminal-owned view data; `agent` retains only generation/tool protocol types.

**Files:** `agent/provider.go`, `agent/tools.go`, `interactive/session.go`, `tool/subagent*.go`, `terminal/tui_render_subagents.go`

The core engine owns generation usage and tool events, but `agent` also defines types it never uses:

- account UI data and loading (`ProviderUsage`, `UsageWindow`, `UsageProvider`);
- model capability lookup (`ModelMetadata`, `ModelMetadataProvider`), consumed only during session setup;
- the complete subagent job/status model, produced by `tool` and rendered by `terminal`.

This keeps imports simple in the short term, but turns the most central package into a cross-feature type registry and obscures ownership.

**Recommendation:** move provider account usage and model-capability contracts to `backend` (or map them to terminal view data at the application boundary). Move subagent profile, progress, state, and status types into a focused subagent package. Keep only generation protocol, generation `Usage`, engine events, and tool execution contracts in `agent`.

## Medium impact

### 4. Consolidate the subagent subsystem around one cohesive package

- [x] **Completed** — `tool/subagent` owns profiles, status, lifecycle, finalization, checkpoints, and launch/wait/cancel adapters.

**Files:** `tool/subagent.go`, `tool/subagent_wait.go`, `tool/subagent_cancel.go`, `tool/subagent_policy.go`, `interactive/subagent.go`, `interactive/session.go`, `agent/tools.go`

Subagent behavior is spread across four layers: `tool/subagent.go` is both a launch tool and a concurrent job manager; companion tools reach into its private lifecycle; finalization policy is another root-tool file; `interactive` constructs child agents and derives progress from engine events; and `agent` owns the status DTOs. `Subagent` is also an imprecise name for an object that is simultaneously manager, registry, and launch tool.

**Recommendation:** create a focused package such as `tool/subagent` that owns profiles, status, the manager, budget/finalization policy, and the launch/wait/cancel tool adapters. Let `interactive` supply only a child-run function and model/tool policy. Within that package, distinguish `Manager` from `LaunchTool`, `WaitTool`, and `CancelTool`. This keeps the asynchronous state machine together without moving provider or terminal concerns into it.

### 5. Separate the LSP service from its agent-tool adapters

- [x] **Completed** — `tool/lsp` is now an agent-independent service, while root `tool` owns the tool definitions, argument handling, presentation, result formatting, and read/write policy.

**Files:** `tool/lsp/tools.go`, `tool/lsp/support.go`, `tool/lsp/client.go`, `tool/lsp/edit.go`, `tool/lsp/rename.go`, `interactive/toolset.go`

`tool/lsp` contains two layers: a language-server client/service and concrete Eul tool definitions. Because the child package imports both `agent` and its parent `tool`, it duplicates parent helpers for workspace resolution, strict schemas, argument decoding, result construction, and output bounding. `interactive` must also understand both `tool.Registry` and `lsp.Set` to assemble one toolset.

**Recommendation:** make `tool/lsp` a lower-level LSP service with operations such as diagnostics, hover, references, symbols, and rename, independent of `agent` and the parent `tool` package. Put the Eul tool adapters in the root `tool` package (or a clearly named adapter package). Then dependency direction becomes `tool -> lsp`, `interactive` can construct a single tool set through `tool`, and the duplicated code in `tool/lsp/support.go` largely disappears. Preserve intentionally different output limits rather than unifying them accidentally.

### 6. Split the LSP watcher implementation by responsibility, not by package

- [x] **Completed** — watcher state ownership remains unified while native watching, registration parsing, the command loop, reconciliation, and event delivery are split into focused files.

**Files:** `tool/lsp/watch.go`, `tool/lsp/watch_native.go`, `tool/lsp/watch_registration.go`, `tool/lsp/watch_loop.go`, `tool/lsp/watch_reconcile.go`, `tool/lsp/watch_events.go`

This 830-line file contains the fsnotify adapter, command-loop lifecycle, dynamic registration parsing, glob compilation, directory scanning, reconciliation and rollback, event suppression/coalescing, and LSP notification. The watcher is a valid cohesive subsystem, but these are independently changeable policies in one state-machine file.

**Recommendation:** retain the unexported watcher abstraction and existing native-watcher test seam, but divide it into files such as `watch_native.go`, `watch_registration.go`, `watch_loop.go`, and `watch_reconcile.go`. Do not create another package or split state ownership; the goal is navigability and reviewability.

### 7. Give steering one authoritative coordinator

- [x] **Completed** — a controller-owned steering coordinator tracks accepted and deferred messages and supplies the derived render/checkpoint view; the model no longer maintains a reconciled queue.

**Files:** `agent/continuation.go`, `terminal/tui_model.go`, `terminal/tui_controller.go`, `terminal/checkpoint.go`

Pending steering is represented three times: accepted messages in `continuationArbiter`, deferred messages in `tuiController.deferredSteering`, and displayed/persisted messages in `operationModel.steering`. Cancellation and turn boundaries reconcile them through `restoreQueuedInput`, `startDeferredTurn`, `deliverSteering`, `removeSteering`, and `restoreSteering`.

The timing distinction is real, but three mutable queues make restoration and persistence hard to reason about.

**Recommendation:** add one controller-owned steering coordinator that tracks accepted versus deferred messages and exposes a derived render/checkpoint view. Keep the engine responsible only for continuations it has accepted; do not make the model maintain an independently reconciled copy.

### 8. Make tool presentation an optional capability consistently

- [x] **Completed** — `agent.Toolbox` now requires only definitions and execution; presentation is an optional `ToolPresenter` capability with an engine-level generic fallback.

**Files:** `agent/tools.go`, `agent/tool_events.go`, `tool/registry.go`

At the concrete-tool level, presentation is optional through the private `presenter` interface and the registry supplies a fallback. At the engine boundary, however, `agent.Toolbox` requires `Presentation`, and `ToolPresentation` carries terminal-oriented concepts such as Markdown, diffs, tail lines, elapsed time, and timeout.

**Recommendation:** split the engine dependency into required execution (`Definitions`, `Execute`) and an optional presentation capability. `toolEventTracker` can use the capability when present and otherwise emit the existing generic title. Keep presentation on engine events, where it is useful, but do not require every alternate toolbox to implement UI enrichment.

### 9. Keep the generic backend contract free of current OpenAI and scheduling policy

- [x] **Completed** — the base driver no longer owns scheduling defaults or OAuth interactions; interactive configuration owns profile models, OAuth exposes its optional runtime capability, and Codex model constants are shared with the provider catalog.

**Files:** `backend/backend.go`, `backend/codex/driver.go`, `backend/codex/api/models.go`, `backend/codex/oauth/auth.go`, `cmd/eul/auth.go`, `interactive/config.go`, `interactive/subagent.go`

The backend abstraction is small, but its supposedly generic parts already encode current implementation policy:

- `AuthOptions.Device` and `Interaction.DeviceCode` describe OpenAI OAuth flows;
- `ModelDefaults.Fast` and `Balanced` are Eul subagent scheduling roles, not universal provider concepts;
- OpenAI model identifiers are repeated in the driver defaults and Codex metadata table.

A second provider with API-key auth or different model roles would have to conform to OpenAI/Eul-specific vocabulary or expand the shared interface again.

**Recommendation:** before adding another provider, move auth-method details to an OAuth-specific optional capability or a provider-declared method/event model. Keep profile-to-model defaults in application/provider configuration rather than the base `Driver` contract. Export narrowly named Codex model constants or a catalog so the driver does not repeat identifiers. The existing `Driver`, `Runtime`, and optional credential-check interfaces are otherwise worth retaining.

### 10. Separate skill discovery/parsing from engine behavior

- [x] **Completed** — the focused `skill` package owns discovery, canonicalization, frontmatter parsing, and command expansion; `agent` retains only prompt integration and content placement.

**Files:** `skill/skill.go`, `agent/skills.go`, `agent/prompt.go`, `interactive/config.go`

`agent/skills.go` combines recursive filesystem discovery, symlink deduplication, a custom frontmatter parser, prompt serialization, and runtime command expansion. `interactive` chooses the search paths, while the engine needs only resolved metadata and expansion behavior.

**Recommendation:** move loading, canonicalization, and frontmatter parsing to a focused `skill` package, leaving system-prompt integration in `agent`. At minimum, split the current file into loading, parsing, and prompt/expansion files. This narrows the core engine package's filesystem responsibilities without inventing a general plugin framework.

## Lower-impact cleanup

### 11. Remove CLI flag-presence mechanics from `interactive.Options`

- [x] **Completed** — model overrides use optional strings, flag-presence stays in CLI parsing, and stored/new session reconstruction uses local option helpers.

**Files:** `cmd/eul/config.go`, `interactive/config.go`, `interactive/run.go`

The former model-set booleans exposed `flag` parsing mechanics through the session API and made resume/new-session reconstruction verbose.

**Recommendation:** represent overrides as pointers or a small optional string value, and add local `optionsFromRecord` / `optionsFromConfig` helpers. Keep all default resolution in `resolveConfig`.

### 12. Align names and helpers with what they actually do

- [x] **Completed** — input handling and terminal API files now match their roles, the duplicate conversation projection and production test wrappers were removed, builtin backend composition moved to `cmd/eul`, and the subscription-backed provider is named `backend/codex`.

**Files:** `terminal/tui_input_handler.go`, `terminal/api.go`, `terminal/tool_presentation.go`, `terminal/tui_render_frame.go`, `interactive/toolset.go`, `cmd/eul/main.go`, `backend/codex/driver.go`

- The “reducer” mutates the model and returns effects; rename it to input handling (`applyKey`, `handleKeyInput`) unless it is made genuinely pure.
- `terminal/repl.go` contains the public API and presentation sanitizers, not a REPL loop; split or rename it to `api.go` and `tool_presentation.go`.
- `modelConversationLines` duplicates the conceptual flattening in `tuiRenderer.prepareConversation` and is used primarily by tests. Extract one shared projection or move the test helper into `_test.go`.
- `buildToolset` and `buildToolsetWithHome` are production-file wrappers used only by tests; move them to test helpers.
- `backend/builtin` contains one registry constructor used only by `cmd/eul`; fold it into the composition root until there is a second consumer or a meaningful builtin catalog.
- The subscription-backed ChatGPT Codex integration should use the specific `backend/codex` name before another OpenAI integration makes a generic provider name ambiguous.

## Strengths to preserve

- `agent.Provider` and concrete optional retry/compaction interfaces are small, consumer-oriented, and idiomatic.
- `tool.Registry` cleanly owns validation, dispatch, defensive definition copies, and reverse-order resource closure.
- Agent and terminal checkpoints are independently versioned, while the session record composes them; that is a sound persistence boundary.
- The `backend/codex/api` transport and `backend/codex/oauth` credential split is cohesive.
- Terminal rendering is already divided by concern and its cached frame model is justified. Splitting `terminal` into subpackages would force many UI internals to become exported and is unlikely to help.
- `agent.Engine` is large but cohesive around one generation/tool loop. Refactor its policies only when extracting a real independent seam, not merely to reduce line count.
- Lifecycle, checkpoint, terminal-input, LSP, and subagent tests exercise meaningful boundaries and should continue to guide refactors.

## Suggested order

1. Introduce explicit terminal run outcomes.
2. Reshape the terminal/application boundary and make settings/persistence have one owner.
3. Relocate auxiliary types out of `agent` and consolidate subagents.
4. Separate LSP service code from tool adapters, then split the watcher file.
5. Clean the backend/config contracts before adding another provider.
6. Apply the naming, duplicate-helper, and test-only cleanup last.
