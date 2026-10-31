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

// commentCommandsEnabled controls whether the daemon reads issue and pull
// request comments to detect `@vigilanteai cleanup|recreate|resume|iterate`
// commands. It is off for now: no repository sweep or per-issue comment poll
// runs, so command detection costs no GitHub requests. Flip it back to true to
// restore comment-driven commands.
var commentCommandsEnabled = false

type pollingContextKey struct{}
type pollingSchedule struct {
	mu        sync.Mutex
	due       map[string]bool
	completed map[string]bool
	interval  time.Duration
	retention time.Duration
	sweeps    map[string]*repoSweepResult
}

func (a *App) prepareCommentPolling(ctx context.Context, sessions []state.Session) (context.Context, func([]state.Session)) {
	config, _ := a.state.LoadServiceConfig()
	interval, err := time.ParseDuration(config.GitHubInactivePollInterval)
	if err != nil || interval < 5*time.Minute {
		interval = 30 * time.Minute
	}
	retention := state.ParseRetention(config.GitHubFailedSessionPollRetention, state.DefaultFailedSessionPollRetention)
	schedule := &pollingSchedule{due: make(map[string]bool), completed: make(map[string]bool), interval: interval, retention: retention, sweeps: make(map[string]*repoSweepResult)}
	// Oldest due sessions go first. Sessions deferred by a budget retain their due
	// time and move ahead of serviced sessions on the next scan (also on restart).
	sort.SliceStable(sessions, func(i, j int) bool { return sessions[i].NextCommentPollAt < sessions[j].NextCommentPollAt })
	now := a.clock()
	for _, s := range sessions {
		due, _ := time.Parse(time.RFC3339, s.NextCommentPollAt)
		schedule.due[sessionKey(s.Repo, s.IssueNumber)] = s.Status != state.SessionStatusClosed && !sessionRetiredFromPolling(s, now, retention) && !now.Before(due)
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
			a.logger.Info("github background budget", "rest_used_units", budget.Used, "rest_limit_units", budget.Limit, "graphql_used_units", budget.GraphQLUsed, "graphql_limit_units", budget.GraphQLLimit, "window_end", budget.WindowEnd, "pause_until", budget.PauseUntil, "core_remaining_estimate", budget.CoreRemaining, "graphql_remaining_estimate", budget.GraphQLRemaining, "core_reset", budget.CoreReset)
		}
		fetches, hits, failures := cache.Counts()
		a.logger.Info("comment polling summary", "fetches", fetches, "cache_hits", hits, "failures", failures, "deferred_sessions", deferred, "swept_repos", len(schedule.sweeps))
	}
}

func commentPollDue(ctx context.Context, session state.Session) bool {
	if session.Status == state.SessionStatusClosed {
		return false
	}
	if session.IssueBackend != "" && session.IssueBackend != "github" {
		return true
	}
	schedule, _ := ctx.Value(pollingContextKey{}).(*pollingSchedule)
	return schedule == nil || schedule.due[sessionKey(session.Repo, session.IssueNumber)]
}

// reconcileFailedPolling refreshes remote state and cleans up closed issues.
// Closed issues stop automatic command monitoring; closed PRs on
// open issues stop PR polling while issue recreate commands remain available.
func (a *App) reconcileFailedPolling(ctx context.Context, sessions []state.Session, cache scanIssueDetailsCache) {
	now := a.clock()
	schedule, _ := ctx.Value(pollingContextKey{}).(*pollingSchedule)
	retention := state.DefaultFailedSessionPollRetention
	if schedule != nil {
		retention = schedule.retention
	}
	for i := range sessions {
		s := &sessions[i]
		if (s.IssueBackend != "" && s.IssueBackend != "github") || s.Status != state.SessionStatusFailed || !commentPollDue(ctx, *s) {
			continue
		}
		issue, err := a.loadSessionIssueDetails(ctx, cache, s)
		if err != nil {
			if ghcli.IsIssueUnavailableError(err) {
				a.closeIssueSession(ctx, s, nil, cache)
			}
			continue
		}
		if strings.EqualFold(issue.State, "closed") {
			a.closeIssueSession(ctx, s, issue, cache)
			continue
		}
		// The pull request state read is a GraphQL command per session; once per
		// retention window is enough to notice a PR that closed or merged.
		if last, err := time.Parse(time.RFC3339, strings.TrimSpace(s.FailedPollReconciledAt)); err == nil && retention > 0 && now.Sub(last) < retention {
			continue
		}
		s.FailedPollReconciledAt = now.UTC().Format(time.RFC3339)
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
