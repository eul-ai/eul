# Basic subagents

Status: implementation plan only.

## Goal

Add one small, synchronous subagent mechanism to Yaah:

- the main agent may use it only when the user explicitly asks for subagents;
- one request may contain several independent tasks;
- those tasks run concurrently in fresh child engines;
- the main engine waits for every child;
- the collected child responses return as an ordinary tool result, so the main model can synthesize them in the same turn.

The first version is advisory and read-only. The main agent remains the only writer.

## User-visible behavior

A user can ask normally, for example:

```text
Use three subagents to review the authentication code from different angles, then summarize their findings.
```

The main model may then call a new `subagent` tool once:

```json
{
  "tasks": [
    "Review auth/openai for correctness and identify concrete defects.",
    "Review auth/openai for cancellation and concurrency problems.",
    "Review auth/openai tests for important missing coverage."
  ]
}
```

Yaah runs the tasks concurrently, waits for all of them, and returns an ordered result such as:

```text
Subagent 1:
...

Subagent 2:
...

Subagent 3:
...
```

That text is returned through the existing tool-result path. The next main-model generation receives it as a correlated function result and produces the final answer. No separate context-merging protocol is needed.

The existing terminal tool notices are sufficient for the first version:

```text
[tool] subagent
[tool] subagent — ok
```

## Explicit invocation rule

The tool definition and main system prompt will state that `subagent` may be called only when the current user explicitly requests subagents. The engine will never invoke it automatically, and children will not receive the tool.

This is a model-behavior rule, not an intent parser or security boundary. The first version will not add keyword detection, an enable flag, or a `/subagents` command. A hard mechanical authorization gate can be added later if model compliance proves insufficient.

## Tool contract

Add `tool/subagent.go` with this definition:

- name: `subagent`;
- required argument: `tasks`, an array of strings;
- minimum tasks: 1;
- maximum tasks: 4;
- every task must contain non-whitespace text.

The fixed limit bounds concurrent requests and accidental subscription usage without adding configuration.

`NewSubagent` should accept one callback with the shape:

```go
func(context.Context, string) (agent.RunResult, error)
```

The tool owns argument decoding, goroutine coordination, deterministic result formatting, ordinary child-error formatting, and final output bounding. The callback owns construction and execution of one fresh child engine. This keeps provider and CLI wiring out of the generic tool package and makes the tool straightforward to test.

## Child engines

For every task, the callback creates:

1. a fresh OpenAI provider client using the existing OAuth token source and reasoning effort;
2. a fresh `agent.Engine` using the same model and project instructions as the parent;
3. a fresh read-only tool registry;
4. one `Run` call with the delegated task and a sink that discards child streaming events.

Child state exists only for that task and is discarded when it finishes. Parent continuation state is never copied into a child. The main model must put all necessary scope, file names, and context into each task.

### Child tool access

Children receive only:

- `read`;
- the five non-mutating LSP tools: diagnostics, hover, definition, references, and symbols.

They do not receive `write`, `edit`, `bash`, `lsp_rename`, or `subagent`. Each child gets a separately constructed registry and LSP client, avoiding concurrent access to the existing LSP session map.

This makes parallel goroutines useful without introducing concurrent workspace writes, recursive delegation, worktrees, or write-conflict handling.

## Concurrency and waiting

`subagent.Execute` will:

1. decode and validate all tasks before launching anything;
2. allocate a result slot for each task;
3. start one goroutine per task;
4. call the injected callback from each goroutine;
5. wait with `sync.WaitGroup`;
6. format results in the original task order, regardless of completion order;
7. return the combined text as one `agent.ToolResult`.

All children receive the tool call's context. Ctrl-C or parent cancellation therefore reaches every child provider request. After cancellation, the tool waits for the child goroutines to return and then propagates `ctx.Err()` so the existing engine and reset behavior remains authoritative.

An ordinary error from one child does not cancel its siblings. Yaah waits for all tasks and returns successful outputs alongside labeled child errors. The combined tool result is marked as an error when any child failed, allowing the main model to explain partial failure while still using successful feedback.

The existing 50 KiB tool-output bound applies to the final combined result. No child streaming is forwarded to the terminal in the first version, preventing interleaved output.

## CLI wiring

Update `cmd/yaah/main.go` so the main registry includes the new tool.

The injected callback closes over:

- the existing OAuth token source;
- selected model;
- selected reasoning effort;
- working directory;
- loaded `AGENTS.md` instructions;
- provider factory.

Provider creation remains lazy: the main provider is created once as today, and each child provider is created only when the tool actually runs. Merely starting Yaah or completing a normal turn does not launch or initialize a child.

Add a small `buildSubagentTools(cwd)` helper for the read-only child registry. Update `buildTools` to accept the already-constructed `subagent` tool while preserving all existing main-agent tools.

## State and usage semantics

- Child continuation state is private and short-lived.
- Parent state changes only through the existing function-call and function-result items.
- Child responses become part of parent state because the OpenAI adapter already preserves tool calls and outputs.
- Child token usage is not added to `RunResult.Usage` in the first version; that field continues to describe provider work performed directly by the main engine and its compaction calls.
- There is no persistence, resume, status lookup, background execution, or cross-turn child handle.

## Failure behavior

- Invalid task arrays return a normal tool error before any goroutine starts.
- Failure to create one child provider is reported for that child while siblings continue.
- Ordinary child generation or tool errors are labeled in the combined result.
- Parent cancellation propagates as cancellation rather than being converted into child feedback.
- A final output with mixed successes and failures is bounded once after formatting.

No retries are added. Existing OAuth refresh, HTTP limits, provider error redaction, and request cancellation remain in force through each child provider.

## Files

Expected changes:

- `tool/subagent.go`: definition, validation, concurrent execution, ordered aggregation.
- `tool/subagent_test.go`: contract and concurrency tests.
- `cmd/yaah/main.go`: provider callback and main/child tool wiring.
- `cmd/yaah/main_test.go`: lazy construction and wiring coverage.
- `agent/prompt_test.go`: explicit-invocation wording remains present in the generated prompt.
- `README.md`: brief description, explicit-use rule, read-only limitation, and maximum of four tasks.

No provider wire-format changes are required.

## Tests

### Tool tests

Cover:

- one task;
- several tasks running concurrently rather than serially;
- output ordered by input position despite reverse completion order;
- mixed success and failure results;
- rejection of zero, more than four, or blank tasks before launch;
- parent cancellation reaching all callbacks;
- combined output using the existing tool-output bound.

Use channels as barriers in concurrency tests rather than timing assumptions.

### Wiring and engine behavior

Cover:

- child providers are not created during ordinary startup or normal turns;
- one fresh provider and engine are created per delegated task;
- child registries omit every mutating tool and omit `subagent`;
- the main registry still exposes all existing tools plus `subagent`;
- the generated tool description says explicit user request is required;
- the main engine receives the aggregated text as a normal tool result and can complete the same turn.

### Validation

Run:

```text
gofumpt -w <changed Go files>
go test ./tool ./cmd/yaah ./agent
go test ./...
go test -shuffle=on ./...
go test -race ./tool ./agent
go vet ./...
staticcheck ./...
git diff --check
```

Manual OAuth check:

1. Start an interactive Yaah session.
2. Ask explicitly for two subagents with distinct review tasks.
3. Confirm one `subagent` tool start/end pair appears.
4. Confirm both findings appear in the main answer.
5. Ask a normal follow-up without requesting subagents and confirm no subagent tool call occurs.
6. Start a delegated turn and press Ctrl-C; confirm the turn interrupts without a late child result.

## Non-goals

The first version will not add:

- automatic delegation;
- child writes or shared-workspace mutation;
- nested subagents;
- background or detached children;
- child status, steering, resume, or persistence;
- separate models, effort levels, prompts, or per-child configuration;
- dynamic concurrency settings;
- retries or fallback routing;
- child streaming in the terminal;
- worktrees or merge logic;
- aggregate child usage accounting.

## Implementation order

1. Add the `subagent` tool and focused unit tests.
2. Add the child-run callback and read-only registry wiring in `cmd/yaah`.
3. Add engine/wiring tests proving ordinary tool-result feedback into the main generation.
4. Update prompt and README wording.
5. Run focused, full, shuffled, race, vet, staticcheck, diagnostics, and diff validation.
6. Perform one live explicit-use and cancellation check before committing the implementation.

## Acceptance criteria

The implementation is complete when an explicit user request can launch one to four fresh read-only child engines concurrently, Yaah waits for every child, their ordered outputs are returned through the existing tool-result continuation path, the main model synthesizes those outputs in the same turn, cancellation stops the group, and ordinary turns do not cause subagent invocation.
