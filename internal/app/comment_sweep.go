package app

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/nicobistolfi/vigilante/internal/environment"
	ghcli "github.com/nicobistolfi/vigilante/internal/github"
	"github.com/nicobistolfi/vigilante/internal/state"
)

// repoSweepResult holds the comments one repository sweep routed to sessions.
// A repository with a result is "covered": its sessions read comments from here
// and never issue per-issue comment requests during the scan. A failed sweep
// leaves the repository uncovered so the legacy path still applies.
type repoSweepResult struct {
	comments map[string][]ghcli.IssueComment
}

// commentSweepOverlap is subtracted from the first `since` so a command posted
// moments before the daemon started is still observed.
const commentSweepOverlap = time.Minute

// runCommentSweeps performs one repository-wide comment sweep per watched
// GitHub repository that has sessions eligible for command polling, routes new
// comments to those sessions, and marks sessions with new comments due so the
// command handlers run for them this scan. Cursors are updated on targets and
// persisted when the scan saves the watch list.
func (a *App) runCommentSweeps(ctx context.Context, targets []state.WatchTarget, sessions []state.Session) {
	schedule, _ := ctx.Value(pollingContextKey{}).(*pollingSchedule)
	if schedule == nil {
		return
	}
	now := a.clock()
	eligible := make(map[string]map[int]int) // repo -> issue or PR number -> session index
	for i := range sessions {
		s := &sessions[i]
		if s.IssueBackend != "" && s.IssueBackend != "github" {
			continue
		}
		if s.Status == state.SessionStatusClosed || sessionRetiredFromPolling(*s, now, schedule.retention) {
			continue
		}
		numbers := eligible[s.Repo]
		if numbers == nil {
			numbers = make(map[int]int)
			eligible[s.Repo] = numbers
		}
		numbers[s.IssueNumber] = i
		if s.PullRequestNumber > 0 && s.PullRequestState == "OPEN" {
			if _, taken := numbers[s.PullRequestNumber]; !taken {
				numbers[s.PullRequestNumber] = i
			}
		}
	}
	for i := range targets {
		target := &targets[i]
		if target.EffectiveIssueBackend() != "github" || strings.TrimSpace(target.Repo) == "" {
			continue
		}
		numbers := eligible[target.Repo]
		if len(numbers) == 0 {
			continue
		}
		schedule.mu.Lock()
		_, done := schedule.sweeps[target.Repo]
		schedule.mu.Unlock()
		if done {
			continue
		}
		cursor := state.CommentSweepCursor{}
		if target.CommentSweep != nil {
			cursor = *target.CommentSweep
		}
		sweepCtx := a.withWatchTargetAccessLogContext(ctx, "scan", *target)
		sweep, err := ghcli.SweepRepoComments(sweepCtx, a.env.Runner, target.Repo, cursor, now.Add(-schedule.interval-commentSweepOverlap))
		result := &repoSweepResult{comments: make(map[string][]ghcli.IssueComment)}
		if err != nil {
			// Leave the repository uncovered: its due sessions fall back to the
			// scheduled, budgeted per-issue polling path for this scan.
			if !errors.Is(err, environment.ErrGitHubDeferred) {
				a.logger.Error("comment sweep failed", "repo", target.Repo, "err", err)
			} else {
				a.logger.Info("comment sweep deferred", "repo", target.Repo, "err", err)
			}
			continue
		}
		routed, untracked, fresh := 0, 0, 0
		schedule.mu.Lock()
		for _, comment := range sweep.Comments {
			index, ok := numbers[comment.Number]
			if !ok {
				untracked++
				continue
			}
			routed++
			if !a.markCommentSeen(target.Repo, comment.ID, comment.UpdatedAt) {
				continue
			}
			fresh++
			key := sessionKey(sessions[index].Repo, sessions[index].IssueNumber)
			result.comments[key] = append(result.comments[key], comment.IssueComment)
			schedule.due[key] = true
		}
		for key := range result.comments {
			sort.SliceStable(result.comments[key], func(i, j int) bool {
				return result.comments[key][i].CreatedAt.Before(result.comments[key][j].CreatedAt)
			})
		}
		schedule.sweeps[target.Repo] = result
		schedule.mu.Unlock()
		updated := sweep.Cursor
		updated.LastSweepAt = now.UTC().Format(time.RFC3339)
		target.CommentSweep = &updated
		a.logger.Info("comment sweep", "repo", target.Repo, "requests", sweep.Requests, "not_modified", sweep.NotModified, "comments", len(sweep.Comments), "routed", routed, "new", fresh, "untracked", untracked, "sessions", len(numbers), "since", updated.Since)
	}
}

// markCommentSeen records a comment version and reports whether it is new to
// this daemon process. GitHub's inclusive `since` and stable ETag URLs return
// the same comments repeatedly; this keeps handlers from re-running for them.
func (a *App) markCommentSeen(repo string, id int64, updatedAt time.Time) bool {
	a.commentSweepMu.Lock()
	defer a.commentSweepMu.Unlock()
	if a.commentSweepSeen == nil {
		a.commentSweepSeen = make(map[string]map[int64]time.Time)
	}
	seen := a.commentSweepSeen[repo]
	if seen == nil {
		seen = make(map[int64]time.Time)
		a.commentSweepSeen[repo] = seen
	}
	if previous, ok := seen[id]; ok && !updatedAt.After(previous) {
		return false
	}
	seen[id] = updatedAt
	return true
}

// sweptSessionComments returns the comments a repository sweep routed to the
// session. covered reports whether a sweep succeeded for the session's repository.
func sweptSessionComments(ctx context.Context, session state.Session) (comments []ghcli.IssueComment, covered bool) {
	schedule, _ := ctx.Value(pollingContextKey{}).(*pollingSchedule)
	if schedule == nil {
		return nil, false
	}
	schedule.mu.Lock()
	defer schedule.mu.Unlock()
	result, ok := schedule.sweeps[session.Repo]
	if !ok {
		return nil, false
	}
	return result.comments[sessionKey(session.Repo, session.IssueNumber)], true
}

// sessionRetiredFromPolling reports whether a failed session is old enough, or
// its pull request is finished, that background command polling stops for it.
// Retired sessions keep their history and remain visible in `vigilante list`.
func sessionRetiredFromPolling(session state.Session, now time.Time, retention time.Duration) bool {
	if session.Status != state.SessionStatusFailed {
		return false
	}
	if session.PullRequestNumber > 0 {
		switch strings.ToUpper(strings.TrimSpace(session.PullRequestState)) {
		case "CLOSED", "MERGED":
			return true
		}
	}
	if retention <= 0 {
		return false
	}
	for _, raw := range []string{session.EndedAt, session.UpdatedAt, session.StartedAt} {
		ended, err := time.Parse(time.RFC3339, strings.TrimSpace(raw))
		if err != nil {
			continue
		}
		return now.Sub(ended) > retention
	}
	return false
}

// errIssueLookupBackoff means the session's issue lookup is deliberately
// skipped this scan after repeated non-HTTP failures.
var errIssueLookupBackoff = errors.New("issue lookup backing off")

const issueLookupBackoffThreshold = 5
const issueLookupBackoffMax = 24 * time.Hour

var httpStatusPattern = regexp.MustCompile(`(?i)\bhttp[ /]\d{3}\b|\(\d{3}\)`)

// loadSessionIssueDetails loads issue details for a session with exponential
// backoff after repeated failures that carry no HTTP status (network errors,
// gh exiting 1 without a response). HTTP-classified errors and unavailable
// issues keep their existing handling and never trigger backoff.
func (a *App) loadSessionIssueDetails(ctx context.Context, cache scanIssueDetailsCache, session *state.Session) (*ghcli.IssueDetails, error) {
	now := a.clock()
	if until, err := time.Parse(time.RFC3339, strings.TrimSpace(session.IssueLookupBackoffUntil)); err == nil && now.Before(until) {
		return nil, errIssueLookupBackoff
	}
	details, err := a.loadIssueDetailsForScan(ctx, cache, session.Repo, session.IssueNumber)
	if err == nil {
		if session.IssueLookupFailures != 0 || session.IssueLookupBackoffUntil != "" {
			session.IssueLookupFailures = 0
			session.IssueLookupBackoffUntil = ""
		}
		return details, nil
	}
	if ghcli.IsIssueUnavailableError(err) || errors.Is(err, environment.ErrGitHubDeferred) || httpStatusPattern.MatchString(err.Error()) {
		return nil, err
	}
	session.IssueLookupFailures++
	if session.IssueLookupFailures >= issueLookupBackoffThreshold {
		exponent := session.IssueLookupFailures - issueLookupBackoffThreshold
		if exponent > 12 {
			exponent = 12
		}
		delay := defaultScanInterval * time.Duration(1<<uint(exponent))
		if delay > issueLookupBackoffMax {
			delay = issueLookupBackoffMax
		}
		session.IssueLookupBackoffUntil = now.Add(delay).UTC().Format(time.RFC3339)
		a.logger.Warn("issue lookup backing off", "repo", session.Repo, "issue", session.IssueNumber, "failures", session.IssueLookupFailures, "until", session.IssueLookupBackoffUntil, "err", err)
	}
	return nil, fmt.Errorf("%w (failure %d)", err, session.IssueLookupFailures)
}
