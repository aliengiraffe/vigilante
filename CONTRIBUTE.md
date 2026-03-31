# Contributing

This repository is designed to be worked by both humans and coding-agent runs launched by Vigilante. Keep changes small, issue-scoped, and validated with the narrowest useful test command before broadening coverage.

## Local Development Notes

- Work from an isolated branch or issue worktree instead of editing the primary checkout directly.
- Use `vigilante commit` for any commit, amend, or history rewrite that records code changes. Do not use `git commit`, GitHub CLI commit flows, or other direct commit commands.
- Ensure `vigilante commit` preserves the existing git author, committer, and signing configuration so commits remain user-authored and signed according to the user's git configuration.
- Prefer repository-native commands and targeted Go tests for the area you changed.

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
