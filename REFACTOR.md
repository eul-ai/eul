# Refactoring Review

## Overall assessment

The project is already well structured for its size. Dependency direction is clean, `cmd/yaah` is an appropriate composition root, and OpenAI wire details are isolated. No broad rewrite or major new abstraction is warranted.

## Prioritized refactors

### 1. Create a single TUI controller boundary

**Priority:** High

State transitions currently span `runTUI`, reducers, and `handleKeyWithOutput`:

- `terminal/tui.go:69-233`
- `terminal/tui.go:318-359`
- `terminal/tui_reducer.go:29-182`

Introduce an unexported controller that owns turn cancellation, exit state, dirty state, and effect execution. Channel events should enter one transition path, which can return effects such as starting or canceling a turn, resetting the engine, copying text, starting a file search, requesting provider usage, or exiting.

This would make cancellation, EOF, and engine-completion ordering easier to understand and test.

**Migration risk:** Medium-high. Preserve the current cancellation precedence and cancellation-draining behavior.

### 2. Separate durable TUI state from rendering caches

**Priority:** High

Rendering currently mutates `scrollTop`, wrapped-line caches, dirty flags, and redraw state:

- `terminal/tui_render.go:143-184`
- `terminal/tui_render.go:384-424`

Mouse handling also recomputes layouts and frames:

- `terminal/tui_mouse.go:67-115`

Move wrapped-line caches and the last frame/layout into `tuiRenderer`. Mouse selection should consume the last rendered frame rather than rebuilding it. Viewport normalization should happen as an explicit state transition before rendering.

This would provide one authoritative screen-coordinate model and make rendering closer to a pure projection.

**Migration risk:** Medium. Preserve viewport-following behavior and selection across wide and combining characters.

### 3. Refactor the LSP mutation pipeline, then extract LSP into `tool/lsp`

**Priority:** High

Rename resolves a position from one file read and then opens a second snapshot:

- `tool/lsp_rename.go:25-55`
- `tool/lsp_client.go:88-103`

Multi-file edits are assembled from map iteration and committed sequentially with `os.WriteFile`:

- `tool/lsp_edit.go:46-123`

A concurrent file change can invalidate the resolved position, and a later write failure can leave a partial rename.

First extract narrow shared primitives such as `loadTextFile` and atomic same-directory replacement from `tool/edit.go:76-144`. Use one source snapshot, deterministic commit ordering, expected-original-content checks, and explicit partial-commit reporting.

After tightening mutation behavior, move the LSP subsystem into `tool/lsp`. Keep `tool.Registry` and the basic tools in `tool`, with `cmd/yaah/session.go` wiring the LSP tools into the registry.

**Migration risk:** Medium-high, particularly around symlink behavior and multi-file failure semantics.

### 4. Move partial tool-argument parsing out of the provider

**Priority:** Medium

`ToolCallSnapshot` exposes both raw and parsed arguments:

- `agent/provider.go:30-36`

The OpenAI adapter therefore owns a tolerant partial-JSON parser solely for tool previews:

- `provider/openai/sse.go:215-229`
- `provider/openai/partial_json.go`

Providers should emit only call identity, name, raw JSON, and completion state. Parse partial arguments once in the tool-presentation path, likely in or immediately before `tool.Registry.Presentation`. Move the existing parser and tests rather than rewriting its behavior.

This avoids requiring every future streaming provider to duplicate presentation-specific parsing.

**Migration risk:** Moderate. Preserve existing behavior for nested, escaped, and incomplete JSON.

### 5. Tighten registry construction and lifecycle contracts

**Priority:** Medium

Duplicate tool names silently overwrite map entries while retaining duplicate definitions:

- `tool/registry.go:32-48`

Closing also occurs in nondeterministic map order:

- `tool/registry.go:84-95`

Validate nonempty, unique names during construction, preserve registration order for deterministic cleanup, define whether `Close` is idempotent, and make LSP client ownership explicit rather than attaching it implicitly to one wrapper tool.

Do not add deep schema copying unless the registry becomes externally exposed.

**Migration risk:** Medium because constructor errors would propagate into `cmd/yaah/session.go`.

### 6. Narrow the authentication API

**Priority:** Medium-low

The public `auth/openai.Credentials` type exposes persistence fields and refresh tokens:

- `auth/openai/oauth.go:44-51`

The CLI immediately narrows it to an access token and account ID:

- `cmd/yaah/main.go:158-184`

Keep stored OAuth credentials private and expose a smaller access credential or token-source surface. Login can return success or reduced metadata instead of the persisted credential record.

This keeps refresh-token storage and rotation entirely inside `auth/openai`.

**Migration risk:** Medium. The CLI's `oauthManager` seam and authentication tests would change, but the engine contract would not.

## Conditional or opportunistic refactors

### Clarify `agent.Engine` concurrency ownership

`Engine` holds mutable conversation state, while `Run`, `Reset`, and `SetThinkingLevel` are unsynchronized:

- `agent/engine.go:25-35`
- `agent/engine.go:249-260`

Current callers serialize access, so document the type as not safe for concurrent use or add an `ErrBusy` guard. Do not extract a separate session package unless a real second use case requires it.

### Replace positional provider callbacks with a stream observer

`Provider.Generate` currently accepts three adjacent callbacks:

- `agent/provider.go:75-82`

An observer value would centralize ordering, concurrency, and callback-error semantics. Defer this until another provider or stream event makes the current signature costly.

### Validate OpenAI requests before resolving credentials

`Generate` and `Compact` resolve authentication before decoding continuation state and enforcing request bounds:

- `provider/openai/client.go:122-143`
- `provider/openai/client.go:197-215`

Perform local validation and bounded encoding first, then resolve credentials immediately before the HTTP request. This is a small ordering cleanup rather than an architectural change.

### Decompose Bash within the existing package

`tool/bash.go` combines argument policy, process lifecycle, timeout classification, capture, streaming, result formatting, and presentation. Split it into focused files for execution, streaming, and tail capture, but retain one `tool` package and the existing `Bash` facade.

### Split file-picker responsibilities by file

`terminal/file_picker.go` contains both OS search execution and picker/editor state. Split it into files such as `file_search.go` and `tui_file_picker.go`, but keep both in `terminal`; a subpackage would require exporting private UI protocols without another consumer.

## Refactors to avoid for now

- Do not introduce a generic multi-provider CLI configuration layer until a second provider exists.
- Do not merge `auth/openai` with `provider/openai`.
- Do not split terminal input, rendering, or mouse handling into subpackages; they share private layout and styling types.
- Do not merge `read`, `write`, and `edit` behind a generic file-tool abstraction; their contracts differ materially.
- Do not move `ToolPresentation` into `terminal`; both TUI and one-shot output consume it, while tools own their semantic summaries.
- Do not split `provider/openai` merely because individual files are large. Its request, response, SSE, continuation, model, and usage files form one cohesive adapter.
- Do not split packages merely because individual test files are large.
- Keep child-agent construction in `cmd/yaah` and subagent execution/aggregation in `tool/subagent.go`.

## Suggested implementation order

1. Add the TUI controller boundary.
2. Move renderer caches and frame ownership out of `tuiModel`.
3. Tighten registry lifecycle contracts and extract shared text-file mutation primitives.
4. Make LSP rename snapshot-consistent and improve multi-file commit reporting.
5. Extract the LSP subsystem into `tool/lsp`.
6. Move partial-argument parsing out of `provider/openai`.
7. Apply the lower-priority API and file-level cleanups opportunistically.

The TUI and registry/LSP tracks are independent and can be implemented separately.

## Review verification

The review was read-only. Before this document was added, the repository was clean and the following checks passed:

- `go test ./...`
- `go test -shuffle=on ./...`
- `go test -race ./...`
- `go vet ./...`
- `git diff --check`

Overall statement coverage was approximately 83%.
