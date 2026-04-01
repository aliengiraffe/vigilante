---
name: vigilante-issue-implementation-on-go
description: Implement a GitHub issue end-to-end when Vigilante dispatches work for a Go repository with idiomatic tooling and security guidance.
---

# Vigilante Go Issue Implementation

## Focus
- Read the prompt for detected tech stacks, process hints, and Go security guidance before changing code.
- Follow idiomatic Go conventions from Effective Go for naming, formatting, error handling, and package structure.
- Keep changes scoped to the issue and do not broaden into unrelated style or lint fixes.

## Go Tooling Workflow
- **Formatting**: run `gofmt` or `go fmt` on all touched Go files. If the repository uses `goimports`, use that instead. Do not hand-format Go code.
- **Testing**: run targeted `go test ./path/to/package/...` for changed packages first, then broader `go test ./...` when changes cross package boundaries. Use `-race` when practical.
- **Vetting**: run `go vet ./...` on touched packages to catch common mistakes.
- **Vulnerability checking**: run `govulncheck ./...` when it is installed and the change touches dependencies or security-sensitive code. If `govulncheck` is not available, note its absence and continue with other validation — do not fabricate output.
- **Linting**: use the repository's established lint tooling. Prefer `golangci-lint` when configured (`.golangci.yml` or `.golangci.yaml`), or `staticcheck` when that is the project standard. If no project linter is configured, `go vet` is sufficient. Do not introduce a different linter unless the issue specifically requires it.
- **Dependencies**: run `go mod tidy` after any dependency changes. Prefer standard library packages when they cover the need.

## Effective Go Style
- Use MixedCaps for multi-word names, not underscores.
- Keep exported identifiers descriptive and unexported identifiers short.
- Return errors rather than panicking.
- Write doc comments on exported identifiers starting with the identifier name.
- Keep packages focused and APIs minimal.

## Security
- Use `crypto/rand` for security-sensitive random values, not `math/rand`.
- Prefer standard library crypto packages over third-party alternatives.
- Use `crypto/subtle.ConstantTimeCompare` for comparing secret values.
- Consider fuzz tests for parsers and input-handling logic (Go 1.18+ `func FuzzXxx`).
- Do not store secrets, tokens, or credentials in source files.

## Workflow
- Follow the base `vigilante-issue-implementation` workflow for issue comments, validation, push, and PR creation.
- Use `vigilante commit` for all commit-producing operations. Do not use `git commit` or GitHub CLI commit flows directly.
- Any commit or amend must preserve the user's existing git author, committer, and signing configuration. Commit on behalf of the user and do not overwrite `git config` with a coding-agent identity.
- Do not add `Co-authored by:` trailers or any other agent attribution for Codex, Claude, Gemini, or similar coding-agent identities.
- Repository-specific instructions (`AGENTS.md`, `README.md`, CI config) remain authoritative when they are more specific than the generic Go guidance in this skill.
