# Yaah

Yaah is a small, trusted-local coding agent written in Go. It provides one
line-oriented executable, an in-memory conversation, four coding tools, and an
OpenAI Responses API adapter.

## Scope

- Go implementation; Bash and gopls are currently required at runtime.
- macOS and Linux are the initial targets.
- Tools run unsandboxed with the user's permissions.
- Provider adapters are selected at compile time.
- Sessions live only in memory and `/clear` discards them.

Yaah deliberately excludes a full-screen TUI, Markdown rendering, dynamic
plugins, provider negotiation, model catalogs, telemetry, MCP, subagents,
project indexing, multimodal input, and session persistence.

## Usage

```text
yaah --model <model> [--effort <level>] [--cwd <directory>]
yaah --model <model> [--effort <level>] "one-shot prompt"
yaah login [--device-auth]
yaah logout
```

`OPENAI_MODEL` and `OPENAI_REASONING_EFFORT` provide flag defaults. Supported
reasoning efforts are `none`, `minimal`, `low`, `medium`, `high`, `xhigh`, and
`max`.

Yaah uses its own ChatGPT OAuth credential from `YAAH_HOME/auth.json` or the
operating system's user configuration directory. It does not read Pi or Codex
credential stores.

ChatGPT OAuth uses the subscription-backed Codex endpoint. OpenAI documents
ChatGPT sign-in for Codex, but the direct OAuth client, endpoint, headers, and
wire format used by independent clients are not a stable public third-party API
contract. This mode is experimental.

## Terminal

Interactive mode supports:

- `/help`
- `/clear`
- `/exit`
- EOF to exit
- Ctrl-C to cancel the active turn or exit while idle

Assistant text is streamed to stdout. Reasoning summaries, tool activity, and
errors go to stderr. The terminal intentionally has no raw mode, colors,
history, autocomplete, or ANSI rendering.

## Tools

All relative paths resolve against the session's fixed working directory.
Calls execute sequentially in model-provided order, and results are bounded
before returning to the model.

- `read(path, offset?, limit?)` reads regular UTF-8 text files, up to 2,000
  lines or 50 KiB.
- `write(path, content)` creates or directly overwrites a regular file and
  creates parent directories.
- `edit(path, oldText, newText)` replaces one uniquely matching fragment using
  a same-directory temporary file and rename.
- `bash(command, timeout?)` runs `bash -c` without subprocess stdin and keeps
  the last 2,000 lines or 50 KiB.
- `lsp_diagnostics(path)` returns current language-server diagnostics.
- `lsp_hover(path, line, character)` returns type and documentation details.
- `lsp_definition(path, line, character)` returns definition locations.
- `lsp_references(path, line, character, includeDeclaration?)` returns reference
  locations.
- `lsp_symbols(path)` returns document symbols.
- `lsp_rename(path, line, character, newName)` renames a symbol across the
  workspace.

LSP line numbers and UTF-16 character offsets are zero-based. Source-file
extensions select a language server; currently `.go` files use gopls.

Unknown tools, malformed arguments, and ordinary tool failures become
correlated tool-result errors. Cancellation stops the turn. Tools are not a
security boundary: absolute paths, shell escape, and side effects are possible.

## Architecture

```text
cmd/yaah/        CLI and dependency wiring
agent/           provider/tool contracts, prompt, and tool-call loop
auth/openai/     browser and device OAuth plus credential refresh
provider/openai/ Responses API requests, SSE decoding, and continuation state
tool/            read, write, edit, bash, LSP client, registry, and output limits
terminal/        line-oriented REPL and event rendering
```

The `agent` package owns the narrow provider and toolbox interfaces. The OpenAI
adapter owns all wire types and preserves opaque response output items for
stateless continuation. The agent stores that state without interpreting it.
The LSP client uses `go.lsp.dev/protocol` for protocol types and JSON-RPC.

The OpenAI adapter uses bounded SSE streams. Reasoning summaries, output text,
and refusals are delivered incrementally. Completed output items are retained
for tool calls and continuation replay. Requests use `store: false` and follow
the experimental Codex wire contract.

The engine permits at most 20 tool rounds per user turn. Once tool execution
has begun, cancellation or an incomplete continuation requires `/clear` before
another turn so provider state cannot be confused with external side effects.

## Authentication

Browser login uses authorization-code OAuth with PKCE and a loopback callback.
Device authorization is available for headless environments. Credentials are
stored privately and atomically, refreshed before expiry, and updated when the
server rotates refresh tokens. Logout removes only yaah's credential file.

Credentials are sent only in HTTP headers.

## Verification

Normal changes should pass:

```text
gofumpt -w <changed Go files>
go test ./...
go test -shuffle=on ./...
go vet ./...
git diff --check
```

Tests use fake providers and tools, `httptest.Server`, temporary directories,
and buffer-backed terminal sessions. Normal tests require no live OpenAI
credential.
