# Contributing

This repository is designed to be worked by both humans and coding-agent runs launched by Vigilante. Keep changes small, issue-scoped, and validated with the narrowest useful test command before broadening coverage.

## Local Development Notes

- Work from an isolated branch or issue worktree instead of editing the primary checkout directly.
- Preserve the existing git author, committer, and signing configuration for any commit or history rewrite.
- Prefer repository-native commands and targeted Go tests for the area you changed.

## Test Coverage

Coverage is measured by one command, used identically by contributors and CI:

```sh
task coverage        # or: ./scripts/coverage.sh
```

It prints per-package coverage worst-first, prints the total, and fails when the
total drops below the threshold committed in `scripts/coverage-threshold`. That
threshold is a ratchet: it only moves up. If a change raises the total, raising
the threshold in the same pull request is welcome.

The target is **80% per non-data package**, which is the standard [awesome-go][ag]
applies to listed projects. The repository is not there yet — the committed
threshold starts at the measured baseline and climbs. New and changed code is held
to 80% immediately through Codecov's patch status, so the way the number goes up is
by covering what you touch rather than by bulk backfill.

Coverage reports are published to [Codecov][cc]. `internal/testutil` and the
non-Go directories are excluded; see `codecov.yml`.

[ag]: https://github.com/avelino/awesome-go/blob/main/CONTRIBUTING.md
[cc]: https://app.codecov.io/gh/aliengiraffe/vigilante

## Documentation Comments

Every package carries a `// Package <name> ...` doc comment, and `golangci-lint`
enforces a Go-style doc comment on newly added exported identifiers via revive's
`exported` rule. Run it before pushing:

```sh
golangci-lint run
```

`.golangci.yml` carries a per-file baseline for exported symbols that predate the
linter. That list only shrinks — do not add to it. If you are already editing a
file on the list, documenting its exported symbols and removing its entry is a
welcome change.

## Fork-Based Workflow

Use fork mode when the authenticated GitHub identity should open pull requests from a fork rather than push branches directly to the upstream watched repository.

```sh
vigilante watch --fork ~/path/to/repo
```

Use an explicit owner when the fork should live under a bot or organization account:

```sh
vigilante watch --fork --fork-owner my-bot-org ~/path/to/repo
```

Fork mode behavior:

- Vigilante creates or reuses the configured GitHub fork before implementation starts.
- The local checkout keeps `origin` pointed at the upstream repository and configures a deterministic `fork` remote for pushes.
- Issue comments, issue selection, and pull request targeting continue to use the upstream repository as the source of truth.
- Implementation branches push to the `fork` remote, and pull requests are opened back to the upstream base branch.
