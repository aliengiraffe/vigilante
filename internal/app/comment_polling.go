package app

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	ghcli "github.com/nicobistolfi/vigilante/internal/github"
	"github.com/nicobistolfi/vigilante/internal/state"
)

type pollingContextKey struct{}
type pollingSchedule struct {
	mu        sync.Mutex
	due       map[string]bool
	completed map[string]bool
	interval  time.Duration
}

func (a *App) prepareCommentPolling(ctx context.Context, sessions []state.Session) (context.Context, func([]state.Session)) {
	config, _ := a.state.LoadServiceConfig()
	interval, err := time.ParseDuration(config.GitHubInactivePollInterval)
	if err != nil || interval < 5*time.Minute {
		interval = 30 * time.Minute
	}
	schedule := &pollingSchedule{due: make(map[string]bool), completed: make(map[string]bool), interval: interval}
	// Oldest due sessions go first. Sessions deferred by a budget retain their due
	// time and move ahead of serviced sessions on the next scan (also on restart).
	sort.SliceStable(sessions, func(i, j int) bool { return sessions[i].NextCommentPollAt < sessions[j].NextCommentPollAt })
	for _, s := range sessions {
		due, _ := time.Parse(time.RFC3339, s.NextCommentPollAt)
		schedule.due[sessionKey(s.Repo, s.IssueNumber)] = s.Status != state.SessionStatusClosed && !a.clock().Before(due)
	}
	ctx = context.WithValue(ctx, pollingContextKey{}, schedule)
	ctx, cache := ghcli.WithCommentCache(ctx)
	return ctx, func(sessions []state.Session) {
		schedule.mu.Lock()
		defer schedule.mu.Unlock()
		deferred := 0
		for i := range sessions {
			s := &sessions[i]
			key := sessionKey(s.Repo, s.IssueNumber)
			if s.IssueBackend != "" && s.IssueBackend != "github" {
				continue
			}
			if !schedule.completed[key] {
				if schedule.due[key] {
					deferred++
				}
				continue
			}
			delay := interval
			if s.Status == state.SessionStatusRunning || s.Status == state.SessionStatusResuming || s.Status == state.SessionStatusBlocked {
				delay = time.Minute
			}
			s.NextCommentPollAt = a.clock().Add(delay).UTC().Format(time.RFC3339)
		}
		if a.githubBudget != nil {
			budget := a.githubBudget.Status()
			a.logger.Info("github background budget", "used_units", budget.Used, "limit_units", budget.Limit, "window_end", budget.WindowEnd, "pause_until", budget.PauseUntil, "core_remaining_estimate", budget.CoreRemaining, "core_reset", budget.CoreReset)
		}
		fetches, hits, failures := cache.Counts()
		a.logger.Info("comment polling summary", "fetches", fetches, "cache_hits", hits, "failures", failures, "deferred_sessions", deferred)
	}
}

func commentPollDue(ctx context.Context, session state.Session) bool {
	if session.IssueBackend != "" && session.IssueBackend != "github" {
		return true
	}
	schedule, _ := ctx.Value(pollingContextKey{}).(*pollingSchedule)
	return schedule == nil || schedule.due[sessionKey(session.Repo, session.IssueNumber)]
}

// reconcileFailedPolling refreshes remote state without deleting failed work or
// its history. Closed issues stop automatic command monitoring; closed PRs on
// open issues stop PR polling while issue recreate commands remain available.
func (a *App) reconcileFailedPolling(ctx context.Context, sessions []state.Session, cache scanIssueDetailsCache) {
	for i := range sessions {
		s := &sessions[i]
		if (s.IssueBackend != "" && s.IssueBackend != "github") || s.Status != state.SessionStatusFailed || !commentPollDue(ctx, *s) {
			continue
		}
		issue, err := a.loadIssueDetailsForScan(ctx, cache, s.Repo, s.IssueNumber)
		if err != nil {
			if ghcli.IsIssueUnavailableError(err) {
				s.Status = state.SessionStatusClosed
			}
			continue
		}
		if strings.EqualFold(issue.State, "closed") {
			s.Status = state.SessionStatusClosed
			continue
		}
		if s.PullRequestNumber > 0 && s.PullRequestState == "OPEN" {
			pr, err := a.prManagerForSession(*s).GetPullRequestDetails(ctx, s.Repo, s.PullRequestNumber)
			if err == nil && pr != nil {
				s.PullRequestState = pr.State
			} else if err != nil && ghcli.IsIssueUnavailableError(err) {
				s.PullRequestState = "CLOSED"
			}
		}
	}
}

// The delay starts after a scan completes; overdue ticks cannot accumulate.
func waitForNextScan(ctx context.Context, interval time.Duration) error {
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
