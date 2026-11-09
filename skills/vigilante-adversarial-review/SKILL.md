---
name: vigilante-adversarial-review
description: Run a solicited adversarial code review of a Vigilante-opened pull request with a chosen coding agent and post the findings back to the PR without modifying the branch.
---

# Vigilante Adversarial Review

## Overview
Use this skill when a human explicitly asks Vigilante for a critical second-pass review of an existing Vigilante-opened pull request, either through an `@vigilanteai review {provider}:{model}` PR comment or the `vigilante review` CLI command. The goal is to hunt for real defects in the PR diff — not to summarize it, restate its intent, or rubber-stamp an approval.

## Workflow
1. Fetch the PR metadata and full diff with `vigilante gh pr view <n>` and `vigilante gh pr diff <n>` before drawing any conclusions.
2. Read the surrounding code for every hunk in the diff; a diff line is only wrong or right relative to the invariants of the code it lands in.
3. Hunt adversarially for real defects introduced or exposed by the change: correctness bugs, security vulnerabilities, unhandled edge cases, race conditions, resource leaks, and broken invariants or contracts.
4. For each candidate finding, try to refute it before reporting it. Keep only findings you can defend with a concrete failure scenario (specific inputs or state leading to a wrong result, crash, or exploit).
5. For each surviving finding, cite the file and line, describe the failure scenario concretely, and rate severity (critical, major, minor).
6. Post all findings back to the pull request as a single review comment via `vigilante gh pr comment <n> --body <findings>`, most severe first, under a `## 🔍 Adversarial Review` header that names the reviewing agent and model.
7. If no real defects survive scrutiny, post that conclusion explicitly, including what you examined and why the risky-looking parts hold up.

## Guardrails
- This session is read-only with respect to the repository: do not modify files, do not commit, do not push, and do not change the PR branch in any way.
- Your only write is the review comment posted to the pull request.
- Do not apply or implement fixes for the findings; fixing is a separate, explicitly requested follow-up.
- Do not pad the review with style nitpicks or restatements of the diff; every finding must be a defensible defect.
- If the review cannot be completed (for example the PR or its diff is unavailable), post a short comment explaining the blocker and exit with a non-zero status.
