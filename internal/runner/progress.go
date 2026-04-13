package runner

import (
	"context"
	"strings"

	"github.com/nicobistolfi/vigilante/internal/environment"
	"github.com/nicobistolfi/vigilante/internal/state"
)

// ProgressSignal describes the deterministic progress classification of a
// session after the coding agent exits successfully.
type ProgressSignal struct {
	// HasPullRequest is true when the session already tracks a PR number.
	HasPullRequest bool
	// HasNewCommits is true when the issue branch has commits ahead of the base branch.
	HasNewCommits bool
	// HasWorktreeChanges is true when git status reports modified/staged files.
	HasWorktreeChanges bool
}

const (
	incompleteReasonNoDurableProgress = "no_durable_progress"
	incompleteReasonUncommittedOnly   = "uncommitted_changes"
	incompleteReasonCommitsWithoutPR  = "commits_without_pr"
)

// EvaluateSessionProgress inspects deterministic local and git signals to
// classify what durable progress a session made. The caller uses the result
// to decide between SessionStatusSuccess and SessionStatusIncomplete.
func EvaluateSessionProgress(ctx context.Context, runner environment.Runner, session state.Session) ProgressSignal {
	signal := ProgressSignal{
		HasPullRequest: session.PullRequestNumber > 0,
	}
	if signal.HasPullRequest {
		return signal
	}
	signal.HasNewCommits = detectNewCommits(ctx, runner, session)
	signal.HasWorktreeChanges = detectWorktreeChanges(ctx, runner, session)
	return signal
}

// ClassifyIncompleteReason returns the deterministic reason string for a
// session that exited successfully but did not create a PR.
func ClassifyIncompleteReason(signal ProgressSignal) string {
	switch {
	case signal.HasNewCommits:
		return incompleteReasonCommitsWithoutPR
	case signal.HasWorktreeChanges:
		return incompleteReasonUncommittedOnly
	default:
		return incompleteReasonNoDurableProgress
	}
}

// IsRerunEligible returns true when a session is incomplete but has partial
// progress that makes rerunning in the same worktree worthwhile.
func IsRerunEligible(session state.Session) bool {
	if session.Status != state.SessionStatusIncomplete {
		return false
	}
	return session.IncompleteReason == incompleteReasonCommitsWithoutPR ||
		session.IncompleteReason == incompleteReasonUncommittedOnly ||
		session.IncompleteReason == incompleteReasonNoDurableProgress
}

// detectNewCommits returns true if the issue branch has commits ahead of the
// base branch (or origin/base).
func detectNewCommits(ctx context.Context, runner environment.Runner, session state.Session) bool {
	if strings.TrimSpace(session.WorktreePath) == "" {
		return false
	}
	baseBranch := strings.TrimSpace(session.BaseBranch)
	if baseBranch == "" {
		baseBranch = "main"
	}
	output, err := runner.Run(ctx, session.WorktreePath, "git", "rev-list", "--count", "origin/"+baseBranch+"..HEAD")
	if err != nil {
		return false
	}
	return strings.TrimSpace(output) != "0" && strings.TrimSpace(output) != ""
}

// detectWorktreeChanges returns true when there are uncommitted modifications
// (staged or unstaged) in the worktree.
func detectWorktreeChanges(ctx context.Context, runner environment.Runner, session state.Session) bool {
	if strings.TrimSpace(session.WorktreePath) == "" {
		return false
	}
	output, err := runner.Run(ctx, session.WorktreePath, "git", "status", "--porcelain")
	if err != nil {
		return false
	}
	return strings.TrimSpace(output) != ""
}
