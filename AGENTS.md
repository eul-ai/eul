# AGENTS.md

## Scope

- Implement only the requested behavior and the minimum code needed to support it.
- Do not add speculative hardening, compatibility layers, abstractions, validation, configuration, or tests for unrequested scenarios.
- Prefer the standard library and existing project patterns over custom infrastructure.
- Keep functions small and focused, but refactor only when it clearly improves readability or removes duplication.
- Preserve existing behavior unless the request requires changing it.
- If a materially larger design choice is unclear, ask before implementing it.
- After completing and validating the requested work, commit the changes.

## Go

- Prefer early returns and keep the happy path left-aligned.
- Prefer `switch` statements over chained `if`/`else` branches.
- Use blank lines to separate distinct logical stages within a function, especially parsing and validation from setup and execution.
- Avoid comments unless they are absolutely necessary; refactor for clarity instead.
- Write necessary comments so they are understandable without project-wide knowledge or conversational context.
- Do not export symbols unless they are used outside their package.
- After working on a Go file, run `gofumpt -w <path>` for each changed Go file.
- Run focused tests for the changed packages, then `go test ./...`.
