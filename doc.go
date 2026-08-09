// Package skillassets embeds the runtime assets that ship inside the vigilante
// binary: the built-in coding-agent skills under skills/ and the canonical
// repository label manifest.
//
// # Vigilante
//
// This is the module root of vigilante, a sandbox-first orchestration layer for
// coding agents. Vigilante turns GitHub issues into a guarded issue-to-PR
// pipeline: one git worktree per task, deterministic lifecycle management,
// scoped credentials, and a durable operator trail through issue comments,
// session state, and pull requests.
//
// Vigilante is a command-line program, not a library. The orchestration logic
// lives in internal packages and is intentionally not part of the public API, so
// there is nothing here to import beyond the embedded assets above. Install and
// usage documentation lives in README.md and DOCS.md; the sandbox threat model
// is described in SANDBOX.md.
//
// The command entry points are:
//
//	cmd/vigilante   the vigilante CLI and daemon
//	cmd/gh-sandbox  the sandboxed gh wrapper used inside containers
package skillassets
