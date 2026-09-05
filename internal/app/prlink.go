package app

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/nicobistolfi/vigilante/internal/backend"
	ghcli "github.com/nicobistolfi/vigilante/internal/github"
	"github.com/nicobistolfi/vigilante/internal/state"
)

// closingReferencePattern matches a GitHub closing keyword followed by a bare
// `#<number>` reference. Owner-qualified references such as `owner/repo#5` do
// not match because the keyword must be followed directly by the `#`.
var closingReferencePattern = regexp.MustCompile(`(?i)\b(?:close[sd]?|fix(?:e[sd])?|resolve[sd]?)\b[ \t]*:?[ \t]+#(\d+)\b`)

// hasClosingReference reports whether body contains a GitHub closing keyword
// that references issueNumber in the same repository.
func hasClosingReference(body string, issueNumber int) bool {
	if issueNumber <= 0 {
		return false
	}
	for _, match := range closingReferencePattern.FindAllStringSubmatch(body, -1) {
		if number, err := strconv.Atoi(match[1]); err == nil && number == issueNumber {
			return true
		}
	}
	return false
}

// appendClosingReference returns body with a `Closes #<issueNumber>` line
// appended on its own line. The original body is preserved byte-for-byte as a
// prefix of the result.
func appendClosingReference(body string, issueNumber int) string {
	reference := fmt.Sprintf("Closes #%d", issueNumber)
	switch {
	case body == "":
		return reference
	case strings.HasSuffix(body, "\n"):
		return body + "\n" + reference
	default:
		return body + "\n\n" + reference
	}
}

// ensurePullRequestIssueLink verifies that pr closes the session issue and
// appends a closing reference when it does not. The coding agent is instructed
// to write `Closes #<n>` itself; this is the deterministic safety net behind
// that instruction.
//
// The repair is attempted at most once per PR number per session, tracked on
// session.PullRequestLinkAttemptedForPR, so a persistent failure never re-edits
// the pull request on every maintenance pass. Failures are logged and swallowed:
// PR maintenance must not fail because the link could not be repaired.
func (a *App) ensurePullRequestIssueLink(ctx context.Context, session *state.Session, pr ghcli.PullRequest) {
	if session == nil || pr.Number <= 0 || session.IssueNumber <= 0 {
		return
	}
	if session.PullRequestLinkAttemptedForPR == pr.Number {
		return
	}
	// Only open pull requests are worth repairing: a merged or closed PR has
	// already had whatever effect the link would have had, and its session is
	// about to be cleaned up.
	if pr.MergedAt != nil || !strings.EqualFold(strings.TrimSpace(pr.State), "OPEN") {
		return
	}
	repo := strings.TrimSpace(session.Repo)
	if repo == "" {
		return
	}

	target := a.fallbackWatchTargetForSession(*session)
	if target.EffectiveIssueBackend() != string(backend.BackendGitHub) {
		return
	}
	if target.EffectivePRBackend() != string(backend.BackendGitHub) {
		return
	}
	// A fork session's PR body is copied verbatim into a pull request on the
	// upstream repository by tryCreateUpstreamPR, where a bare `Closes #<n>`
	// would resolve to an unrelated issue. Leave those bodies alone.
	if upstream := strings.TrimSpace(session.UpstreamRepo); upstream != "" && !strings.EqualFold(upstream, repo) {
		return
	}

	prManager := a.prManagerForSession(*session)
	if prManager == nil {
		return
	}

	body := pr.Body
	if strings.TrimSpace(body) == "" {
		details, err := prManager.GetPullRequestDetails(ctx, repo, pr.Number)
		if err != nil {
			a.logger.Error("pr issue link lookup failed", "repo", repo, "issue", session.IssueNumber, "pr", pr.Number, "err", err)
			return
		}
		if details == nil {
			return
		}
		body = details.Body
	}

	session.PullRequestLinkAttemptedForPR = pr.Number
	if hasClosingReference(body, session.IssueNumber) {
		a.logger.Info("pr issue link verified", "repo", repo, "issue", session.IssueNumber, "pr", pr.Number, "linked", true)
		return
	}

	if err := prManager.UpdatePullRequestBody(ctx, repo, pr.Number, appendClosingReference(body, session.IssueNumber)); err != nil {
		a.logger.Error("pr issue link repair failed", "repo", repo, "issue", session.IssueNumber, "pr", pr.Number, "err", err)
		return
	}
	session.PullRequestLinkRepairedAt = a.clock().Format(time.RFC3339)
	a.logger.Info("pr issue link repaired", "repo", repo, "issue", session.IssueNumber, "pr", pr.Number, "linked", false)
}
