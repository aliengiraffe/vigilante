package app

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	ghcli "github.com/nicobistolfi/vigilante/internal/github"
	"github.com/nicobistolfi/vigilante/internal/state"
)

const (
	defaultStaleRunningThreshold = 30 * time.Minute
	staleBlockedMultiplier       = 2
)

type sessionGroup struct {
	Label    string
	Sessions []state.Session
}

type watchedRepoStatus struct {
	Target       state.WatchTarget
	ActiveCount  int
	BlockedCount int
}

func groupSessions(sessions []state.Session, now time.Time, inactivityTimeout time.Duration) []sessionGroup {
	var active, prTracking, issueTracking, stale, completed, failed []state.Session

	staleBlockedThreshold := time.Duration(staleBlockedMultiplier) * inactivityTimeout

	for _, s := range sessions {
		if s.Status == state.SessionStatusClosed {
			continue
		}
		if isStale(s, now, staleBlockedThreshold) {
			stale = append(stale, s)
			continue
		}
		switch s.Status {
		case state.SessionStatusRunning, state.SessionStatusResuming:
			active = append(active, s)
		case state.SessionStatusBlocked:
			if isPRTracking(s) {
				prTracking = append(prTracking, s)
			} else {
				issueTracking = append(issueTracking, s)
			}
		case state.SessionStatusSuccess:
			completed = append(completed, s)
		case state.SessionStatusFailed:
			failed = append(failed, s)
		}
	}

	var groups []sessionGroup
	if len(active) > 0 {
		groups = append(groups, sessionGroup{Label: "Actively working", Sessions: active})
	}
	if len(prTracking) > 0 {
		groups = append(groups, sessionGroup{Label: "Paused, tracking PRs", Sessions: prTracking})
	}
	if len(issueTracking) > 0 {
		groups = append(groups, sessionGroup{Label: "Paused, tracking issues", Sessions: issueTracking})
	}
	if len(stale) > 0 {
		groups = append(groups, sessionGroup{Label: "Stale sessions", Sessions: stale})
	}
	if len(completed) > 0 || len(failed) > 0 {
		var summary []state.Session
		summary = append(summary, completed...)
		summary = append(summary, failed...)
		groups = append(groups, sessionGroup{Label: "Completed / failed", Sessions: summary})
	}
	return groups
}

func visibleStatusSessions(sessions []state.Session) []state.Session {
	visible := make([]state.Session, 0, len(sessions))
	for _, s := range sessions {
		if s.Status == state.SessionStatusClosed {
			continue
		}
		visible = append(visible, s)
	}
	return visible
}

func isPRTracking(s state.Session) bool {
	if s.PullRequestNumber > 0 && strings.EqualFold(s.PullRequestState, "OPEN") {
		return true
	}
	switch s.BlockedStage {
	case "pr_maintenance", "ci_remediation", "conflict_resolution":
		return true
	}
	return false
}

func isStale(s state.Session, now time.Time, staleBlockedThreshold time.Duration) bool {
	switch s.Status {
	case state.SessionStatusRunning:
		if s.LastHeartbeatAt != "" {
			if t, err := time.Parse(time.RFC3339, s.LastHeartbeatAt); err == nil {
				return now.Sub(t) > defaultStaleRunningThreshold
			}
		}
		if s.StartedAt != "" {
			if t, err := time.Parse(time.RFC3339, s.StartedAt); err == nil {
				return now.Sub(t) > defaultStaleRunningThreshold
			}
		}
		return false
	case state.SessionStatusBlocked:
		ref := latestTimestamp(s)
		if ref.IsZero() {
			return false
		}
		return now.Sub(ref) > staleBlockedThreshold
	default:
		return false
	}
}

func latestTimestamp(s state.Session) time.Time {
	var latest time.Time
	for _, raw := range []string{s.UpdatedAt, s.BlockedAt, s.LastMaintainedAt} {
		if raw == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			continue
		}
		if t.After(latest) {
			latest = t
		}
	}
	return latest
}

func formatSessionRow(s state.Session) string {
	var b strings.Builder
	fmt.Fprintf(&b, "  Issue #%d in %s: %s", s.IssueNumber, s.Repo, string(s.Status))
	if s.PullRequestNumber > 0 {
		fmt.Fprintf(&b, ", PR #%d %s", s.PullRequestNumber, strings.ToUpper(s.PullRequestState))
	}
	if s.BlockedStage != "" {
		fmt.Fprintf(&b, ", stage %s", s.BlockedStage)
	}
	return b.String()
}

func writeSessionGroups(w io.Writer, groups []sessionGroup) {
	for i, g := range groups {
		if i > 0 {
			fmt.Fprintln(w)
		}
		fmt.Fprintf(w, "%s (%d)\n", g.Label, len(g.Sessions))
		for _, s := range g.Sessions {
			fmt.Fprintln(w, formatSessionRow(s))
		}
	}
}

func writeRateLimitSection(w io.Writer, snapshot ghcli.RateLimitSnapshot) {
	fmt.Fprintln(w)
	fmt.Fprintln(w, "GitHub rate limits")
	writeRateLimitResource(w, "  core", snapshot.Core)
	writeRateLimitResource(w, "  graphql", snapshot.GraphQL)
	writeRateLimitResource(w, "  search", snapshot.Search)
}

func writeRateLimitResource(w io.Writer, label string, r ghcli.RateLimitResource) {
	if r.Limit == 0 {
		return
	}
	resetLabel := "unknown"
	if !r.ResetAt.IsZero() {
		remaining := time.Until(r.ResetAt).Round(time.Second)
		if remaining < 0 {
			resetLabel = "now"
		} else {
			resetLabel = fmt.Sprintf("in %s", remaining)
		}
	}
	fmt.Fprintf(w, "%s: %d/%d remaining, resets %s\n", label, r.Remaining, r.Limit, resetLabel)
}

func writeRateLimitUnavailable(w io.Writer) {
	fmt.Fprintln(w)
	fmt.Fprintln(w, "GitHub rate limits: unavailable")
}

func watchedRepoStatuses(targets []state.WatchTarget, sessions []state.Session) []watchedRepoStatus {
	statuses := make([]watchedRepoStatus, 0, len(targets))
	indexByRepo := make(map[string]int, len(targets))
	for _, target := range targets {
		statuses = append(statuses, watchedRepoStatus{Target: target})
		indexByRepo[target.Repo] = len(statuses) - 1
	}
	for _, session := range sessions {
		index, ok := indexByRepo[session.Repo]
		if !ok {
			continue
		}
		switch session.Status {
		case state.SessionStatusRunning, state.SessionStatusResuming:
			statuses[index].ActiveCount++
		case state.SessionStatusBlocked:
			statuses[index].BlockedCount++
		}
	}
	sort.Slice(statuses, func(i, j int) bool {
		if statuses[i].Target.Repo != statuses[j].Target.Repo {
			return statuses[i].Target.Repo < statuses[j].Target.Repo
		}
		return statuses[i].Target.Path < statuses[j].Target.Path
	})
	return statuses
}

func formatWatchTargetRow(status watchedRepoStatus) string {
	target := status.Target
	fields := []string{
		fmt.Sprintf("branch %s (%s)", valueOrUnknown(target.Branch), target.EffectiveBranchMode()),
		fmt.Sprintf("provider %s", valueOrUnknown(target.Provider)),
	}
	if assignee := strings.TrimSpace(target.Assignee); assignee != "" {
		fields = append(fields, fmt.Sprintf("assignee %s", assignee))
	}
	if len(target.Labels) > 0 {
		fields = append(fields, fmt.Sprintf("labels %s", strings.Join(target.Labels, ",")))
	}
	if target.MaxParallel > 0 {
		fields = append(fields, fmt.Sprintf("max %d", target.MaxParallel))
	}
	fields = append(fields, formatWatchActivity(status))
	if scan := formatLastScan(target.LastScanAt); scan != "" {
		fields = append(fields, scan)
	}
	return fmt.Sprintf("  %s (%s)", valueOrUnknown(target.Repo), strings.Join(fields, ", "))
}

func formatWatchTargetDetail(status watchedRepoStatus) string {
	return fmt.Sprintf("    path: %s", valueOrUnknown(status.Target.Path))
}

func formatWatchActivity(status watchedRepoStatus) string {
	parts := make([]string, 0, 3)
	if status.ActiveCount > 0 {
		parts = append(parts, fmt.Sprintf("%d active", status.ActiveCount))
	}
	if status.BlockedCount > 0 {
		parts = append(parts, fmt.Sprintf("%d blocked", status.BlockedCount))
	}
	if len(parts) == 0 {
		return "idle"
	}
	return strings.Join(parts, ", ")
}

func formatLastScan(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "last scan never"
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return "last scan unknown"
	}
	return fmt.Sprintf("last scan %s", t.UTC().Format("2006-01-02 15:04 UTC"))
}

func valueOrUnknown(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "unknown"
	}
	return value
}

func writeWatchedReposSection(w io.Writer, statuses []watchedRepoStatus) {
	fmt.Fprintf(w, "Watched repositories (%d)\n", len(statuses))
	if len(statuses) == 0 {
		fmt.Fprintln(w, "  none configured")
		return
	}
	for _, status := range statuses {
		fmt.Fprintln(w, formatWatchTargetRow(status))
		fmt.Fprintln(w, formatWatchTargetDetail(status))
	}
}

func (a *App) statusExpanded(ctx context.Context) error {
	status, err := a.statusServiceSection(ctx)
	if err != nil {
		return err
	}

	fmt.Fprintln(a.stdout)
	writeStatusServiceSection(a.stdout, status)

	targets, err := a.state.LoadWatchTargets()
	if err != nil {
		targets = nil
	}
	sessions, err := a.state.LoadSessions()
	if err != nil {
		sessions = nil
	}

	visibleSessions := visibleStatusSessions(sessions)

	fmt.Fprintln(a.stdout)
	writeWatchedReposSection(a.stdout, watchedRepoStatuses(targets, sessions))

	fmt.Fprintln(a.stdout)
	fmt.Fprintf(a.stdout, "Sessions: %d total\n", len(visibleSessions))

	if len(visibleSessions) > 0 {
		cfg, cfgErr := a.state.LoadServiceConfig()
		inactivityTimeout := state.DefaultBlockedSessionInactivityTimeout
		if cfgErr == nil {
			if parsed, parseErr := time.ParseDuration(cfg.BlockedSessionInactivityTimeout); parseErr == nil && parsed > 0 {
				inactivityTimeout = parsed
			}
		}

		groups := groupSessions(visibleSessions, a.clock(), inactivityTimeout)
		if len(groups) > 0 {
			fmt.Fprintln(a.stdout)
			writeSessionGroups(a.stdout, groups)
		}
	}

	snapshot, rlErr := ghcli.GetRateLimitSnapshot(ctx, a.env.Runner)
	if rlErr != nil {
		writeRateLimitUnavailable(a.stdout)
	} else {
		writeRateLimitSection(a.stdout, snapshot)
	}

	return nil
}

func writeStatusServiceSection(w io.Writer, s serviceStatusInfo) {
	fmt.Fprintf(w, "Service\n")
	fmt.Fprintf(w, "  state: %s\n", s.State)
	fmt.Fprintf(w, "  manager: %s\n", s.Manager)
	fmt.Fprintf(w, "  service: %s\n", s.Service)
	fmt.Fprintf(w, "  path: %s\n", s.FilePath)
	if s.Installed {
		fmt.Fprintln(w, "  installed: yes")
	} else {
		fmt.Fprintln(w, "  installed: no")
	}
	if s.Running {
		fmt.Fprintln(w, "  running: yes")
	} else {
		fmt.Fprintln(w, "  running: no")
	}
	if s.Installed {
		switch {
		case s.DaemonVersion != "" && s.Running:
			fmt.Fprintf(w, "  daemon version: %s\n", s.DaemonVersion)
		case s.DaemonVersion != "":
			fmt.Fprintf(w, "  daemon version: %s (configured binary; service not running)\n", s.DaemonVersion)
		case s.Running:
			fmt.Fprintln(w, "  daemon version: unavailable")
		default:
			fmt.Fprintln(w, "  daemon version: unavailable (service not running)")
		}
	}
}

type serviceStatusInfo struct {
	State         string
	Manager       string
	Service       string
	FilePath      string
	Installed     bool
	Running       bool
	DaemonVersion string
}
