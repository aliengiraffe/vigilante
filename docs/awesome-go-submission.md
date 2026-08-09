# awesome-go submission

Everything needed to submit `vigilante` to [awesome-go](https://github.com/avelino/awesome-go),
ready to paste. Verified against awesome-go's automated checker
(`.github/scripts/check-quality/main.go`) on 2026-08-09.

Submitting is a human step. Read the preconditions first — an early submission
gets closed and resubmitting is not always welcome.

## Preconditions

- [ ] **Repository age.** awesome-go requires 5+ months of history since the first
      commit. First commit is 2026-03-10, so the repository qualifies from
      **2026-08-10**. Do not submit before that date.
- [x] **`CODECOV_TOKEN` is configured** as a repository secret (added 2026-08-09).
- [ ] **Codecov shows a report on `main`.** `https://app.codecov.io/gh/aliengiraffe/vigilante`
      loads, but the README badge reads "unknown" until a CI run on `main` uploads
      a report for that branch — the badge URL is branch-scoped. That happens on
      the first push to `main` after the Codecov integration merges. Confirm the
      badge renders a percentage before submitting; the coverage link is only a
      non-blocking check, but a dead link or an "unknown" badge invites a reviewer
      to look harder at everything else.
- [ ] **Coverage is defensible.** awesome-go's standard is ≥80% per non-data
      package and coverage is reviewed *manually*. The total is well under that
      (see `task coverage`), so decide deliberately whether to submit now or after
      the coverage backlog closes. Automated checks passing is not the same as the
      quality standard being met.
- [ ] **One item per PR.** The awesome-go PR must modify `README.md` only, adding
      exactly one line.
- [ ] **Alphabetical position confirmed.** Re-check against the live awesome-go
      README before opening the PR; the category changes often.

## Category and entry line

Category: **Artificial Intelligence**. That category already lists close
analogues — `hotplex` (agent runtime with sessions for Claude Code and OpenCode,
sandboxed) and `skillreaper` (CLI over agent session transcripts) — so it is the
right home for an agent orchestration CLI.

As of 2026-08-09, `vigilante` sorts between `trpc-agent-go` and
`web-researcher-mcp`. Insert exactly this line:

```
- [vigilante](https://github.com/aliengiraffe/vigilante) - Orchestration layer that runs coding agents in isolated git worktrees with scoped credentials and audit logs.
```

Why it is worded that way — each of these is a checked rule:

- Link text is the exact project name, lowercase, matching the repository name.
- The description is one sentence and ends with a period.
- It is non-promotional. Do **not** reuse the repository's GitHub "About" text
  ("so your agents can't burn down production"); the checker warns on superlative
  and marketing language, and a reviewer will read it as promotional.

## Pull request body

The checker parses these four lines out of the PR body with regexes, so the
`Label: URL` shape matters as much as the URLs. Paste this block verbatim:

```
Forge link: https://github.com/aliengiraffe/vigilante
pkg.go.dev: https://pkg.go.dev/github.com/nicobistolfi/vigilante
goreportcard.com: https://goreportcard.com/report/github.com/nicobistolfi/vigilante
Coverage: https://app.codecov.io/gh/aliengiraffe/vigilante
```

Then tick both boxes in awesome-go's PR template (Contribution Guidelines and
Quality Standards).

Two things about those URLs that look like mistakes and are not:

- **pkg.go.dev and goreportcard use `nicobistolfi/vigilante`, not
  `aliengiraffe/vigilante`.** That is the Go module path in `go.mod`. The
  repository moved to the `aliengiraffe` organization and GitHub's transfer
  redirect keeps the old path resolving, so pkg.go.dev serves it and the blocking
  check passes. Using the `aliengiraffe` path here would 404, because no module is
  published under it. Renaming the module would require a new tagged release for
  pkg.go.dev to index the new path.
- **The coverage link must be a Codecov or Coveralls URL.** The checker's regex
  accepts only `codecov.io`, `app.codecov.io`, and `coveralls.io`. A
  self-generated coverage badge or a CI artifact link will not satisfy it.

## What awesome-go checks, and where this repository stands

Blocking checks — a failure prevents merge:

| Check | Status |
| --- | --- |
| Repo accessible, not archived | Pass |
| `go.mod` at repository root | Pass |
| SemVer release `vX.Y.Z` | Pass |
| pkg.go.dev page reachable | Pass, via the module path above |
| Go Report Card grade A-/A/A+ | Pass — see the note below |
| PR body links present | Provided by the block above |
| Single item per PR, alphabetical, entry format, no duplicate link | Manual |
| Category has 3+ items | Pass |

Non-blocking checks:

| Check | Status |
| --- | --- |
| Open source license | Pass (Apache-2.0) |
| 5+ months of history | Pass from 2026-08-10 |
| GitHub Actions CI present | Pass |
| README present | Pass |
| Coverage link reachable | Pass once Codecov has run on `main` |

Reviewed manually by maintainers: correct category, general usefulness,
description accuracy, **real test coverage**, documentation quality, and that the
project works as documented.

### On Go Report Card

goreportcard.com was **sunset in 2026**. Report URLs still return HTTP 200 but the
page no longer publishes a grade. awesome-go's checker reads that as "reachable but
no grade found" and treats it as a pass, so the link is still worth including — but
there is no grade to improve, and no reason to spend effort chasing one.

The repository's static-analysis signal now comes from `golangci-lint`
(`.golangci.yml`) instead: `govet`, `ineffassign`, `misspell`, `gofmt`, and
revive's `exported` doc-comment rule — roughly what that badge used to cover.

## After acceptance

awesome-go expects listed projects to stay maintained: recent commits, issues and
PRs answered within about two weeks, and a release at least yearly. A project that
goes quiet gets removed.
