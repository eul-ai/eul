# Automatic compaction plan

Status: discussion draft. This document proposes architecture and behavior; it does not describe implemented code.

## Goal

Add automatic conversation compaction without making the agent engine depend on OpenAI. The engine should decide when a conversation needs compaction and invoke an optional provider capability. The first and only implementation will use OpenAI's stateless `POST /responses/compact` endpoint through the existing ChatGPT/Codex OAuth transport.

The first version should:

- compact before the next model generation would approach the model context limit;
- keep provider continuation state opaque to the engine;
- replace, rather than append to, the old continuation state after compaction;
- preserve OpenAI's compacted output exactly as returned;
- work both between user turns and between tool rounds;
- preserve current reset and partial-tool-turn guarantees;
- do nothing for providers or models without declared compaction support.

## Research findings

OpenAI currently exposes two server-side mechanisms:

1. **Inline server-side compaction.** A normal `POST /responses` request can include:

   ```json
   {
     "context_management": [
       {"type": "compaction", "compact_threshold": 200000}
     ]
   }
   ```

   When the rendered context crosses the threshold, the response stream emits an opaque encrypted compaction item. A stateless client may then discard items before the newest compaction item.

2. **Standalone compaction.** `POST /responses/compact` accepts a full stateless context and returns a canonical replacement `output` array. OpenAI explicitly says not to prune or reinterpret this output. It can contain retained messages as well as an opaque item of type `compaction` with `encrypted_content`.

Both modes support stateless, `store:false`/ZDR-friendly conversations. The compact endpoint itself is unary JSON, not SSE. Its public response has object type `response.compaction`, an `output` array, and usage data.

The current Codex source confirms that the ChatGPT OAuth backend supports the standalone route at:

```text
https://chatgpt.com/backend-api/codex/responses/compact
```

Codex's compact request contains the model, complete input item history, instructions, tools, parallel-tool setting, reasoning controls, service tier, prompt-cache key, and text controls. The endpoint returns replacement response items. Codex also models a newer feature-gated `compaction_trigger` protocol, but retains the standalone endpoint as its V1 remote-compaction path.

Codex derives its automatic threshold as 90% of the model context window when the model does not declare a lower threshold. For the three currently known models, that is:

| Model | Context window | Proposed threshold |
|---|---:|---:|
| `gpt-5.6-sol` | 272,000 | 244,800 |
| `gpt-5.6-terra` | 272,000 | 244,800 |
| `gpt-5.6-luna` | 272,000 | 244,800 |

Codex checks this limit before a new user turn and during a turn when another model sample is required. That is the behavior this plan follows.

### Why start with the standalone endpoint

The standalone endpoint is the best first implementation because:

- Codex proves that the ChatGPT OAuth backend and route support it.
- It gives the engine an explicit provider-agnostic compaction boundary.
- Its output is already a complete replacement context, which fits the existing opaque `State` design.
- It avoids teaching the generic engine how to find and prune OpenAI-specific `compaction` items from an ordinary response stream.
- Codex does not currently use the public `context_management` field for its normal ChatGPT backend flow, so assuming that field is accepted there would be less well-supported.

Inline `context_management` and Codex's V2 trigger protocol are deferred.

## Provider-agnostic contract

Keep `Provider.Generate` unchanged. Add an optional capability interface in `agent`:

```go
type CompactResponse struct {
    State []byte
    Usage Usage
}

type Compactor interface {
    Compact(context.Context, Request) (CompactResponse, error)
}
```

`Engine.New` checks whether its provider also implements `Compactor`. Providers that do not implement it continue to work unchanged.

The `Request` passed to `Compact` has the same meaning as a request about to be passed to `Generate`:

- `State` is the provider's existing opaque continuation state;
- `Inputs` are pending user or tool-result items not yet represented by that state;
- model, instructions, and tools are the current generation settings.

A successful `Compact` must incorporate both `State` and `Inputs` into the returned canonical `State`. The engine must therefore clear `Inputs` after installing the compacted state so they are not replayed twice.

This contract leaves all provider-specific history decoding, wire items, and canonicalization inside the provider adapter. A future provider can implement the same interface with a local summary, a different endpoint, or its own opaque continuation format.

## Engine policy and state

Extend `Engine` with the latest active-context token count reported by a completed generation. This is a snapshot, not the sum of request usage across a run.

At the top of each generation-loop iteration:

1. Build the request that would normally be sent to `Generate`.
2. Look up the model context window in `modelContextWindows`.
3. Estimate the pending input text as `(bytes + 3) / 4` tokens, matching Codex's inexpensive approximation.
4. Compact when:

   ```text
   latest response total tokens + estimated pending input tokens >= 90% of context window
   ```

5. Require non-empty provider state. There is nothing useful to compact before the first response in a conversation.
6. If the provider implements `Compactor`, call it with the complete pending request.
7. Replace the loop-local state with `CompactResponse.State`, clear pending inputs, add compact-call usage to the run result, and proceed immediately to `Generate`.

After every successful `Generate`, replace the active-context token snapshot with that response's `Usage.TotalTokens`. Continue summing all generation and compaction usage into `RunResult.Usage` as today.

The compact endpoint's `Usage.TotalTokens` measures the work performed by compaction; it is not the size of the resulting context. Do not use it as the new active-context snapshot. The immediately following generation supplies the next authoritative snapshot.

When a run ends normally, commit both continuation state and the latest context-token snapshot to the engine. `Reset` clears both.

### Placement in the tool loop

The top-of-loop check handles both relevant boundaries:

- **Next user turn:** the previous completed turn left a high token snapshot in the engine. The new user input is included in the compact request, the returned state becomes canonical, and generation proceeds without replaying the user input.
- **Tool continuation:** after tool calls execute, their outputs are pending inputs. Compaction receives the function calls from provider state together with the matching tool outputs, then generation continues from the compacted replacement state.

Including pending tool outputs avoids compacting a dangling function call separately from its result.

### Unknown models and missing usage

Automatic compaction is disabled when the model has no context-window metadata or the provider does not implement `Compactor`. This avoids guessing limits or provider capabilities.

OpenAI normally reports usage. If no positive active-context total has ever been reported, do not trigger automatic compaction from a byte-size guess alone.

## OpenAI implementation

Make `provider/openai.Client` implement `agent.Compactor`.

### Endpoint and authentication

Add a compact endpoint alongside the existing responses endpoint:

```text
<base URL>/codex/responses/compact
```

With the default base URL this resolves to:

```text
https://chatgpt.com/backend-api/codex/responses/compact
```

Use the same dynamically resolved OAuth credential and request headers as generation:

- `Authorization: Bearer ...`
- `chatgpt-account-id`
- `Content-Type: application/json`
- `originator: yaah`
- `User-Agent: yaah`
- `OpenAI-Beta: responses=experimental`

Use `Accept: application/json`. Keep credentials in headers only and retain the existing redirect policy, HTTP timeout, bounded bodies, cancellation behavior, and bounded error decoding.

### Request body

Build the compact input by decoding `Request.State`, appending encoded `Request.Inputs`, and reusing the existing tool conversion. Match the Codex payload relevant to yaah:

```json
{
  "model": "gpt-5.6-sol",
  "instructions": "...",
  "input": [],
  "tools": [],
  "parallel_tool_calls": true,
  "reasoning": {
    "effort": "high",
    "summary": "auto"
  },
  "text": {
    "verbosity": "low"
  }
}
```

Omit `reasoning` when no reasoning effort is configured, as generation does. Do not send generation-only fields such as `store`, `stream`, `include`, or `tool_choice`.

A small shared request/header helper is appropriate if it removes direct duplication between `Generate` and `Compact`; no broader HTTP abstraction is needed.

### Response and continuation state

Decode a bounded unary JSON response containing:

```json
{
  "object": "response.compaction",
  "output": [
    {"type": "message", "role": "user", "content": []},
    {"type": "compaction", "encrypted_content": "..."}
  ],
  "usage": {
    "input_tokens": 0,
    "output_tokens": 0,
    "total_tokens": 0
  }
}
```

Validate the JSON envelope and each output item with the same raw-object rules used for normal responses. Usage may be absent on the ChatGPT backend because current Codex code only requires `output`; normalize absent usage to zero.

Encode only the returned `output` array into continuation state. Do not append the pre-compaction history, pending inputs, or compact endpoint metadata. Do not filter retained messages or inspect/decrypt the compaction item. OpenAI defines the returned output as the canonical next context window.

Retain the existing continuation-state version and byte bound unless implementation evidence requires a wire-version change; the state envelope already stores arbitrary response items.

## Failure and atomicity rules

- Wrap compaction failures as `agent: compact context: ...`.
- Do not fall through to a likely-over-limit `Generate` call after compaction fails.
- Keep engine state transactional: work on loop-local state and commit it only after a final successful provider response, as current runs do.
- A failure before any tool executes leaves `resetRequired` unchanged.
- A failure after tool side effects preserves the current `resetRequired` behavior and requires `Reset` before another run.
- Cancellation should return the context error through the same precedence rules used by generation.
- Do not retry compaction separately in the first version; existing HTTP behavior and a user retry are sufficient.

## Tests

### Agent tests

Add focused tests for:

- a provider without `Compactor` remaining unchanged;
- no compaction below the 90% threshold;
- compaction triggered by the previous token snapshot plus a pending user input estimate;
- compaction before a tool-continuation generation;
- compacted state replacing old state and pending inputs being consumed exactly once;
- final high usage causing compaction at the beginning of the next `Run`;
- compact-call usage included in `RunResult.Usage` but not used as active-context size;
- unknown model and missing usage disabling automatic compaction;
- `Reset` clearing the token snapshot;
- compaction error and cancellation atomicity before and after tool execution.

### OpenAI adapter tests

Add HTTP tests for:

- exact compact endpoint, OAuth headers, account header, beta header, and JSON accept type;
- exact request body, including history, pending inputs, instructions, tools, reasoning, and text controls;
- absence of `store`, `stream`, `include`, and `tool_choice`;
- canonical output replacing rather than extending continuation state;
- opaque `compaction` items surviving a compact-then-generate round trip;
- optional usage normalization;
- malformed/trailing JSON, invalid output items, body bounds, state bounds, HTTP errors, redirects, cancellation, and timeout behavior.

Run `gofumpt`, focused package tests, `go test ./...`, shuffled tests, vet, staticcheck, LSP diagnostics, and `git diff --check`.

## Implementation sequence

1. Consume `modelContextWindows` in an unexported context-limit helper and remove its reserved-code lint suppression.
2. Add `agent.Compactor` and `agent.CompactResponse`.
3. Add the engine token snapshot, pending-input estimate, top-of-loop compaction check, transactional state handling, and tests.
4. Add the OpenAI compact request/response types and canonical-state encoding.
5. Add `Client.Compact`, sharing only the small auth/header plumbing that is truly duplicated.
6. Add OpenAI wire and continuation tests.
7. Update `README.md` with automatic threshold behavior and the current OpenAI-only implementation.

## Deferred work

- OpenAI inline `context_management` compaction.
- Codex's feature-gated V2 `compaction_trigger` flow.
- Manual `/compact` commands or CLI flags.
- Local summarization fallback for providers without a compact endpoint.
- Tokenizer-specific counting.
- Context metadata discovery from the remote model catalog.
- Compaction compatibility hashes for model switching.
- Token-aware truncation of a single oversized pending user or tool item.
- Conversation persistence across process restarts.

## Questions for discussion

1. **Visibility:** the minimal version can remain silent unless compaction fails. We could instead add a provider-neutral `EventContextCompacted` rendered on stderr.
2. **Threshold:** this plan uses Codex's 90% default. A lower fixed threshold would leave more room for unusually large tool outputs but diverge from the known client.
3. **Unknown models:** this plan disables automatic compaction. An alternative is a CLI context-window override, but that adds configuration not otherwise needed.
4. **Oversized pending input:** the 90% trigger plus input estimate catches normal growth, but the compact request itself must still fit the model window. The first version intentionally does not truncate one exceptionally large input.

## Sources

- OpenAI, [Compaction guide](https://developers.openai.com/api/docs/guides/compaction)
- OpenAI, [`POST /responses/compact` API reference](https://developers.openai.com/api/docs/api-reference/responses/compact)
- OpenAI, [Conversation state guide](https://developers.openai.com/api/docs/guides/conversation-state)
- OpenAI Codex source, commit [`57f42a8`](https://github.com/openai/codex/tree/57f42a81131ccf5933e7ec5dc659c381eeb5d72b):
  - [`CompactionInput`](https://github.com/openai/codex/blob/57f42a81131ccf5933e7ec5dc659c381eeb5d72b/codex-rs/codex-api/src/common.rs)
  - [compact endpoint client](https://github.com/openai/codex/blob/57f42a81131ccf5933e7ec5dc659c381eeb5d72b/codex-rs/codex-api/src/endpoint/compact.rs)
  - [compact request construction](https://github.com/openai/codex/blob/57f42a81131ccf5933e7ec5dc659c381eeb5d72b/codex-rs/core/src/client.rs)
  - [remote compaction orchestration](https://github.com/openai/codex/blob/57f42a81131ccf5933e7ec5dc659c381eeb5d72b/codex-rs/core/src/compact_remote.rs)
  - [90% automatic threshold](https://github.com/openai/codex/blob/57f42a81131ccf5933e7ec5dc659c381eeb5d72b/codex-rs/protocol/src/openai_models.rs)
