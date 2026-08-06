# Yaah: Minimal Go Coding-Agent Harness Plan

## Product definition

Build a **trusted-local, Unix-first coding agent** that borrows Pi's minimal core rather than pursuing feature parity:

- One Go executable
- Line-oriented terminal interface
- In-memory multi-turn conversation
- Four minimal Pi-style coding tools
- OpenAI Responses API
- Standard library only
- Compile-time provider pluggability
- No sandbox claims

"Zero dependency" initially means **no third-party Go modules**. The `bash` tool still requires Bash at runtime.

## 1. Minimal user interface

### Invocation

```text
yaah --model <model> [--cwd <directory>]
yaah --model <model> "one-shot prompt"
```

Configuration and authentication:

- `yaah login` uses browser authorization-code OAuth with PKCE; `yaah login --device-auth` supports headless environments, and `yaah logout` removes yaah's credential.
- OAuth credentials are stored only in yaah's private `auth.json` (`YAAH_HOME` or the OS user config directory), refreshed before expiry, and never imported from Pi or Codex.
- A nonblank `OPENAI_API_KEY` takes precedence and retains usage-based Platform API access.
- `OPENAI_MODEL` or `--model` is required; avoid a stale hard-coded default.
- The current directory is the default working directory.

ChatGPT OAuth uses the subscription-backed Codex endpoint. OpenAI documents ChatGPT sign-in for Codex, but the direct OAuth client, endpoint, header, and wire details used by independent clients are not a stable public third-party API contract. This compatibility path is therefore experimental and may need updates when Codex changes.

### Interactive mode

Use a plain line REPL based on `bufio.Reader`:

```text
yaah · openai/<model> · /path/to/project
> fix the failing tests
[tool] bash grep -R "TODO" . — exit 0
[tool] read app.go
[tool] bash go test ./... — exit 1
...
```

Commands:

- `/help`
- `/clear` — discard conversation and provider state
- `/exit`
- EOF exits cleanly
- Ctrl-C cancels or exits without corrupting terminal state

Assistant text goes to stdout. Tool status and errors go to stderr. Do not require raw terminal mode, Markdown rendering, autocomplete, history, spinners, colors, or ANSI support.

### Streaming

For the strictest MVP, begin with non-streaming OpenAI responses. Design the provider interface so streaming can be added without changing the agent loop. Streaming should be the first follow-up feature if Pi-like responsiveness is important.

## 2. Pi-style toolset

Support the minimal four Pi coding tools:

- `read`
- `bash`
- `edit`
- `write`

Discovery commands such as `grep`, `find`, and `ls` run through `bash` for the MVP. Dedicated focused discovery tools can be added later if stable schemas, normalized results, or fewer quoting problems prove valuable.

All relative paths resolve against the session's fixed working directory.

### Shared tool rules

- Tool arguments decode into tool-specific Go structs; each tool also supplies the matching provider-facing JSON Schema.
- Unknown fields and malformed arguments produce correlated tool-result errors rather than terminating the agent.
- Results are deterministically ordered and bounded before being sent to the model.
- Tool calls execute sequentially in model-provided order for the MVP.

### `read`

```text
read(path, offset?, limit?)
```

- Read regular UTF-8 text files initially; reject directories, special files, NUL bytes, and invalid UTF-8.
- Follow symlinks that resolve to regular files.
- Use one-based line offsets.
- Keep the first 2,000 lines or 50 KiB, whichever limit is reached first, without loading an unbounded file into memory.
- Explicitly report truncation and the next offset where possible.
- Defer images.

### `write`

```text
write(path, content)
```

- Create or directly overwrite a regular file and create parent directories.
- Follow symlinks to regular files; reject dangling symlinks, directories, and special files.
- Preserve the mode of an existing file. New parent directories use `0777` and new files use `0666`, both subject to the user's umask.
- Writes are intentionally non-transactional; cancellation can leave a changed or partial file.

### `edit`

Start with the minimal exact-replacement contract:

```text
edit(path, oldText, newText)
```

- Operate on regular UTF-8 text files and follow final symlinks to their targets.
- `oldText` must be nonempty and occur exactly once.
- Zero or multiple matches leave the file unchanged.
- Commit a valid replacement through a same-directory temporary file and rename while preserving permission bits. This can change inode metadata and hard-link identity.
- Pi-style batched, non-overlapping edits can be added later without changing the other tools.

### `bash`

```text
bash(command, timeout?)
```

- Execute with `bash -c` in the fixed working directory.
- Do not attach child-process stdin.
- Return combined output and exit status.
- Keep the last 2,000 lines or 50 KiB.
- Enforce configurable timeout and cancellation.
- Document that killing the shell may not kill every descendant process.

This makes the initial target macOS/Linux with Bash installed. Windows needs a separate shell contract rather than silently mapping a tool named `bash` to PowerShell or `cmd.exe`.

### Trust model

Like Pi, the initial harness is an **unsandboxed trusted-local agent**:

- Tools run with the user's permissions.
- Absolute paths and shell escape are possible.
- Filesystem path checks must not be described as a security boundary.
- Containers, approval gates, and real confinement are future features.

## 3. Core architecture

```text
cmd/yaah/
    main.go                 CLI and dependency wiring

agent/
    engine.go               Conversation and tool-call loop
    events.go               Events consumed by the terminal
    prompt.go               Deterministic MVP system prompt
    provider.go             Provider interface and request/response types
    tools.go                Engine-facing tool interface and types

provider/openai/
    client.go               HTTP adapter
    request.go              Responses API mapping
    response.go             Response decoding
    sse.go                  Added when streaming is introduced

tool/
    registry.go
    limits.go               Shared line/byte/result bounding
    read.go
    write.go
    edit.go
    bash.go

terminal/
    repl.go
```

Avoid a dynamic plugin framework, generic event bus, provider capability negotiation, or interfaces around every helper.

### System prompt

Use a concrete `BuildSystemPrompt` function in `agent/prompt.go`; no interface or separate prompt subsystem is needed.

The MVP prompt contains only:

- A short coding-agent identity and behavioral guidance.
- Deterministically ordered names and concise summaries of active tools.
- Essential tool-use guidance that is not already clear from the tool schemas.

Full tool descriptions and JSON Schemas are sent separately through the provider tool definitions.

The MVP prompt deliberately excludes the working directory, `AGENTS.md`, `CLAUDE.md`, skills, and prompt replacement/append mechanisms. Those can be introduced later if a concrete need emerges.

### Provider seam

The agent engine consumes the provider, so the narrow `Provider` interface belongs in the `agent` package. The OpenAI adapter imports `agent` and satisfies that interface; `agent` never imports an implementation package.

```go
type Provider interface {
    Generate(
        context.Context,
        Request,
        func(Event) error,
    ) (Response, error)
}
```

The request and response types used by this contract live beside it in `agent/provider.go`:

```go
type Request struct {
    Model        string
    Instructions string
    Inputs       []Input
    Tools        []ToolSpec
    State        []byte // opaque provider-owned continuation state
}

type Response struct {
    Text      string
    ToolCalls []ToolCall
    State     []byte
    Usage     Usage
}
```

The event callback may receive one complete text event initially and true deltas after streaming is added.

The opaque state is important for OpenAI reasoning models: the Responses API requires carrying forward all returned output items, including reasoning items, rather than only function calls. The core stores this state but never interprets OpenAI wire types.

Provider pluggability means:

- `agent` never imports OpenAI.
- Fake providers can test the engine.
- A future provider is another adapter wired into the composition root.
- No dynamic Go plugins or provider registry are needed initially.

### Tool seam

The engine-facing tool collection interface belongs in `agent/tools.go`, where it is consumed:

```go
type Toolbox interface {
    Definitions() []ToolDefinition
    Execute(context.Context, ToolCall) (ToolResult, error)
}
```

The `tool` package provides a registry that satisfies this interface. Any smaller interface used only to register individual concrete tools belongs in the `tool` package itself.

Each tool defines its arguments as a Go struct and decodes model-supplied JSON into that struct. The tool also supplies the corresponding provider-facing JSON Schema as Go values, which the OpenAI adapter serializes. The standard library does not derive JSON Schema from Go structs, so the two representations are kept small and covered by consistency tests. OpenAI strict schemas use `additionalProperties: false`.

The registry:

- Rejects duplicate names.
- Returns definitions in deterministic order.
- Resolves calls by exact name.
- Separates recoverable tool-result failures from infrastructure cancellation.

## 4. Agent loop

For every user prompt:

1. Append the user input to the normalized transcript.
2. Call the provider with new inputs, tool definitions, and prior provider state.
3. Commit the completed provider response and returned state.
4. If there are no tool calls, finish.
5. For each tool call, in response order:
   - Preserve its call ID.
   - Resolve the tool.
   - Decode and validate arguments.
   - Execute it sequentially.
   - Convert unknown tools, malformed arguments, and execution failures into correlated tool-result errors.
6. Send all tool results back to the provider.
7. Repeat until final text or a fixed round limit.

Important rules:

- Never let the OpenAI adapter execute tools.
- Do not retry ambiguous turns after side effects.
- Preserve assistant text that accompanies tool calls.
- Cancellation is fatal to the current run; ordinary tool failures are not.
- Use a maximum of roughly 20 tool rounds.
- Without tokenizer dependencies, cap transcript/provider-state bytes and tell the user to `/clear` on context overflow.

## 5. OpenAI adapter

Use the **Responses API**, with:

- API-key mode: `POST https://api.openai.com/v1/responses` with a non-streaming JSON response.
- OAuth mode: bounded SSE from `POST https://chatgpt.com/backend-api/codex/responses`, with request-time token refresh and ChatGPT account routing headers.
- `store: false`
- Explicit `model`
- Instructions sent on every request
- Strict function tools
- Local replay rather than `previous_response_id`
- All `response.output` items retained
- Matching `call_id` on each `function_call_output`
- Encrypted reasoning content retained when required for stateless continuation

The adapter owns all OpenAI DTOs and HTTP details. Inject `http.Client` and base URL for `httptest.Server`; production tests should never require credentials.

Handle:

- Browser PKCE and device-code login, private atomic credential storage, refresh-token rotation, and logout
- Bounded Codex SSE completion responses while retaining the terminal's completed-text behavior
- Bounded non-2xx response bodies
- Malformed JSON
- Missing call IDs
- Multiple function calls
- Context cancellation and HTTP timeouts
- Secret redaction

When streaming is added, parse SSE correctly rather than assuming one JSON object per network read.

## 6. Milestones

### Milestone 0 — Record decisions

Document:

- Meaning of zero dependency
- Supported platforms
- Unsandboxed trust model
- OpenAI Responses API choice
- Tool schemas and limits
- Explicit non-goals

### Milestone 1 — Agent engine against fakes

Define the small, consumer-owned provider and tool contracts in `agent`, add the deterministic MVP system-prompt builder, then implement the engine against fakes.

Exit criteria:

- A fake provider demonstrates user input → tool call → matching result → final text.
- Multiple calls preserve order and IDs.
- Unknown tools and malformed arguments are recoverable.
- Round limits and cancellation work.
- The system prompt is deterministic and contains only the active tool guidance.

### Milestone 2 — Tool registry and shared limits

Implement deterministic registration, strict argument decoding, and shared output truncation.

Exit criteria:

- Duplicate tool names are rejected.
- Tool definitions and results have deterministic ordering.
- Head, tail, line, byte, and item limits are independently tested.

### Milestone 3 — Four core tools

Implement and test the four core tools with `t.TempDir` and fake command environments.

Exit criteria:

- `read`, `write`, and `edit` have deterministic failure behavior.
- Failed edits never modify the file.
- `bash` reports timeout, cancellation, output, and exit status.
- Common discovery workflows remain available through `bash`.
- Tool implementations can be replaced without changing the agent or provider layers.
- Every tool result is bounded.

### Milestone 4 — OpenAI adapter

Use non-streaming Responses first.

Exit criteria:

- `httptest.Server` verifies request shape and authentication.
- Text and function calls decode correctly.
- Every output item, including reasoning, is replayed.
- No OpenAI DTO escapes the adapter.

### Milestone 5 — Terminal application

Wire the REPL, configuration, commands, signals, four tools, and provider.

Exit criteria:

- One-shot and interactive workflows function.
- `/clear`, `/exit`, EOF, and interrupt are clean.
- Tool activity is concise and understandable.
- API keys never appear in output.

### Milestone 6 — Hardening

- Add incremental terminal streaming; OAuth currently collects and bounds Codex SSE before delivering completed text.
- Review resource cleanup and process cancellation.
- Add an optional credential-gated live smoke test.

Release checks:

```text
gofmt
go test ./...
go vet ./...
go list -m all   # only the main module
```

## 7. Testing strategy

- Scripted fake provider for the complete agent loop.
- Fake tools for call ordering, errors, and cancellation.
- `httptest.Server` for OpenAI requests and responses.
- `t.TempDir` for filesystem tools.
- Buffer-backed terminal tests requiring no TTY.
- No live OpenAI credential in normal test runs.

## 8. Explicit non-goals

- Pi feature parity
- Full-screen TUI, raw terminal editing, or rich Markdown rendering
- Project context discovery through `AGENTS.md` or `CLAUDE.md`
- System-prompt replacement, append files, or per-turn prompt hooks
- Session persistence, replay, branching, compaction, or tokenizer-based accounting
- Providers other than OpenAI in the initial release
- Runtime provider plugins or capability negotiation
- MCP, subagents, background tools, or parallel tool execution
- Images and other multimodal content
- Dedicated `grep`, `find`, or `ls` tools; use `bash` for MVP discovery
- Built-in Git workflows, LSP integration, or repository indexing
- Interactive subprocesses
- Automatic retries after partially visible output or side effects
- Sandbox, container, or comprehensive permission system
- Model catalogs, telemetry, pricing, or usage dashboards

## 9. Future work

- **Focused discovery tools** — Add bounded, structured `grep`, `find`, and `ls` tools if Bash-based discovery proves too error-prone.
- **Sub-agents** — Child agent sessions with explicit tool/model limits, parent-child coordination, bounded concurrency, cancellation, and isolated or deliberately shared working contexts.
- **Themes** — Configurable colors and presentation once the terminal layer has a stable rendering abstraction; plain text remains the universal fallback.
- **LSP** — Optional tools for diagnostics, hover, definitions, references, and symbols by speaking JSON-RPC to externally installed language servers.
- **Web access** — Bounded fetch and search tools with timeouts, response-size limits, content extraction, source metadata, and an explicit network trust policy.

## 10. Decisions to confirm before implementation

| Decision | Recommendation |
|---|---|
| Zero dependency | No third-party Go modules; Bash runtime allowed |
| Platform | macOS/Linux first |
| OpenAI endpoint | Platform Responses API for API keys; experimental ChatGPT Codex Responses endpoint for OAuth |
| Streaming | Follow-up milestone; interface ready from day one |
| Model | Require `--model` or `OPENAI_MODEL` |
| Safety | Trusted and unsandboxed |
| Provider plugins | Compile-time adapters only |
| Sessions | In-memory only; `/clear` resets |
| Public API | Binary-first; export only deliberate extension points |
| Authentication | `OPENAI_API_KEY` precedence plus yaah-owned ChatGPT OAuth login/refresh/logout |

This produces a defensible small core without prematurely reproducing Pi's sessions, extensions, TUI, model catalog, compaction, skills, or subagent ecosystem.
