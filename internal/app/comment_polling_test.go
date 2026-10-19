package app

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	githubbackend "github.com/nicobistolfi/vigilante/internal/backend/github"
	"github.com/nicobistolfi/vigilante/internal/environment"
	ghcli "github.com/nicobistolfi/vigilante/internal/github"
	"github.com/nicobistolfi/vigilante/internal/logging"
	"github.com/nicobistolfi/vigilante/internal/state"
	"github.com/nicobistolfi/vigilante/internal/testutil"
)

type pollingFixtureRunner struct {
	mu          sync.Mutex
	now         func() time.Time
	calls       map[string]int
	comments    string
	closedIssue bool
	closedPR    bool
	gone        bool   // issue reads return HTTP 410
	prGone      bool   // pull request reads return HTTP 410
	failPath    string // comment endpoints containing this text fail
}

func (r *pollingFixtureRunner) LookPath(s string) (string, error) { return s, nil }
func (r *pollingFixtureRunner) Run(_ context.Context, _ string, name string, args ...string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := name + " " + strings.Join(args, " ")
	r.calls[key]++
	if len(args) > 1 && args[0] == "api" {
		endpoint := args[1]
		if endpoint == "/rate_limit" {
			return fmt.Sprintf(`{"resources":{"core":{"limit":5000,"remaining":5000,"reset":%d}}}`, r.now().Add(time.Hour).Unix()), nil
		}
		if strings.Contains(endpoint, "/comments") {
			if r.failPath != "" && strings.Contains(endpoint, r.failPath) {
				return "", fmt.Errorf("temporary failure")
			}
			if r.comments != "" {
				return r.comments, nil
			}
			return "[]", nil
		}
		if r.gone {
			return "", fmt.Errorf("HTTP 410: Gone")
		}
		issueState := "open"
		if r.closedIssue {
			issueState = "closed"
		}
		return fmt.Sprintf(`{"state":%q,"labels":[],"assignees":[]}`, issueState), nil
	}
	if len(args) > 1 && args[0] == "pr" && args[1] == "view" {
		if r.prGone {
			return "", fmt.Errorf("HTTP 410: Gone")
		}
		prState := "OPEN"
		if r.closedPR {
			prState = "MERGED"
		}
		return fmt.Sprintf(`{"number":1,"state":%q}`, prState), nil
	}
	if len(args) > 1 && args[0] == "issue" && args[1] == "comment" {
		return "ok", nil
	}
	if len(args) > 1 && args[0] == "issue" && args[1] == "list" {
		return "[]", nil
	}
	return "", fmt.Errorf("unexpected command: %s", key)
}

func pollingTestApp(t *testing.T, now *time.Time) (*App, *pollingFixtureRunner) {
	t.Helper()
	t.Setenv("VIGILANTE_HOME", t.TempDir())
	store := state.NewStore()
	if err := store.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	base := &pollingFixtureRunner{now: func() time.Time { return *now }, calls: map[string]int{}}
	budget := &environment.GitHubBudgetRunner{Base: base, Path: filepath.Join(store.Root(), "github-budget.json"), Clock: base.now}
	env := &environment.Environment{Runner: environment.LoggingRunner{Base: budget, AccessLog: store.AppendAccessLogEntry}}
	gh := githubbackend.NewBackend(&env.Runner)
	return &App{state: store, clock: base.now, logger: logging.Discard(), stdout: testutil.IODiscard{}, stderr: testutil.IODiscard{}, env: env, issueTracker: gh, prManager: gh, labelManager: gh, rateLimiter: gh, githubBudget: budget}, base
}

func TestCommentPollingSimulatedHourWithHistoricalSessions(t *testing.T) {
	now := time.Date(2026, 10, 16, 11, 0, 0, 0, time.UTC)
	app, base := pollingTestApp(t, &now)
	var sessions []state.Session
	var targets []state.WatchTarget
	for i := 0; i < 6; i++ {
		targets = append(targets, state.WatchTarget{Repo: "owner/repo" + strconv.Itoa(i), Path: t.TempDir(), Branch: "main", BranchMode: state.BranchModePinned, Assignee: "alice"})
	}
	for i := 0; i < 116; i++ {
		s := state.Session{Repo: targets[i%6].Repo, RepoPath: targets[i%6].Path, IssueNumber: i + 1, Status: state.SessionStatusFailed, StartedAt: now.Add(-60 * 24 * time.Hour).Format(time.RFC3339)}
		if i < 93 {
			s.PullRequestNumber = i + 1000
			s.PullRequestState = "OPEN"
		}
		sessions = append(sessions, s)
	}
	if err := app.state.SaveWatchTargets(targets); err != nil {
		t.Fatal(err)
	}
	if err := app.state.SaveSessions(sessions); err != nil {
		t.Fatal(err)
	}
	for minute := 0; minute < 60; minute++ {
		if err := app.ScanOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
		app.waitForSessions()
		now = now.Add(time.Minute)
	}
	status := app.githubBudget.Status()
	if status.Used > 2000 || status.Used < 1000 {
		t.Fatalf("unexpected budget consumption: %+v", status)
	}
	comments, commands := 0, 0
	for cmd, n := range base.calls {
		if !strings.Contains(cmd, "/rate_limit") {
			commands += n
		}
		if strings.Contains(cmd, "/comments") {
			comments += n
		}
	}
	if comments < 302 || comments >= 1000 {
		t.Fatalf("comment fetches=%d, want at least one full sweep and fewer than 1000", comments)
	}
	if commands > 2000 {
		t.Fatalf("background calls exceeded budget: %d", commands)
	}
	persisted, err := app.state.LoadSessions()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range persisted {
		if s.NextCommentPollAt == "" {
			t.Fatalf("session %d starved in first sweep", s.IssueNumber)
		}
	}
	t.Logf("simulated hour: %d background commands, %d comment pages, %d reserved units (limit %d)", commands, comments, status.Used, status.Limit)
}

func TestCommentPollingScheduleSurvivesRestartAndPrioritizesDeferred(t *testing.T) {
	now := time.Date(2026, 10, 16, 11, 0, 0, 0, time.UTC)
	app, _ := pollingTestApp(t, &now)
	sessions := []state.Session{{Repo: "owner/repo", IssueNumber: 1, Status: state.SessionStatusFailed}, {Repo: "owner/repo", IssueNumber: 2, Status: state.SessionStatusFailed}, {Repo: "owner/repo", IssueNumber: 3, Status: state.SessionStatusBlocked}}
	ctx, finish := app.prepareCommentPolling(context.Background(), sessions)
	for _, i := range []int{0, 2} {
		if _, err := app.collectSessionComments(ctx, sessions[i], "cleanup"); err != nil {
			t.Fatal(err)
		}
	}
	finish(sessions)
	data, _ := json.Marshal(sessions)
	var restarted []state.Session
	if err := json.Unmarshal(data, &restarted); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	ctx, finish = app.prepareCommentPolling(context.Background(), restarted)
	if restarted[0].IssueNumber != 2 {
		t.Fatal("deferred session was not prioritized")
	}
	for _, s := range restarted {
		want := s.IssueNumber != 1
		if commentPollDue(ctx, s) != want {
			t.Fatalf("issue %d due=%v", s.IssueNumber, commentPollDue(ctx, s))
		}
	}
	finish(restarted)
	now = now.Add(29 * time.Minute)
	ctx, _ = app.prepareCommentPolling(context.Background(), restarted)
	for _, s := range restarted {
		if !commentPollDue(ctx, s) {
			t.Fatalf("issue %d not due after inactive interval", s.IssueNumber)
		}
	}
}

func TestReconcileFailedPollingRetainsHistoryAndStopsStalePRReads(t *testing.T) {
	now := time.Now()
	app, base := pollingTestApp(t, &now)
	sessions := []state.Session{{Repo: "owner/repo", IssueNumber: 1, Status: state.SessionStatusFailed, PullRequestNumber: 2, PullRequestState: "OPEN", WorktreePath: "keep-this"}}
	base.closedPR = true
	app.reconcileFailedPolling(context.Background(), sessions, make(scanIssueDetailsCache))
	if sessions[0].PullRequestState != "MERGED" || sessions[0].Status != state.SessionStatusFailed {
		t.Fatal(sessions)
	}
	if _, err := app.collectSessionComments(context.Background(), sessions[0], "recreate"); err != nil {
		t.Fatal(err)
	}
	for cmd := range base.calls {
		if strings.Contains(cmd, "pulls/2/comments") {
			t.Fatal("polled merged PR")
		}
	}
	base.closedIssue = true
	app.reconcileFailedPolling(context.Background(), sessions, make(scanIssueDetailsCache))
	if sessions[0].Status != state.SessionStatusClosed || sessions[0].WorktreePath != "keep-this" {
		t.Fatal(sessions)
	}
}

func TestScanDelayStartsAfterWorkAndCanBeCanceled(t *testing.T) {
	interval := 20 * time.Millisecond
	// The work lasted longer than an interval; no overdue tick is available.
	time.Sleep(2 * interval)
	completed := time.Now()
	if err := waitForNextScan(context.Background(), interval); err != nil {
		t.Fatal(err)
	}
	if time.Since(completed) < interval {
		t.Fatal("started immediately after long scan")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitForNextScan(ctx, time.Hour); err != context.Canceled {
		t.Fatal(err)
	}
}

func TestQuotaDeferralDoesNotBlockSessionOrPostDelayNotice(t *testing.T) {
	now := time.Now()
	app, base := pollingTestApp(t, &now)
	session := state.Session{Repo: "owner/repo", IssueNumber: 1, Status: state.SessionStatusRunning}
	app.recordSessionFailure(&session, "issue_execution", "poll", environment.ErrGitHubDeferred)
	if session.Status != state.SessionStatusRunning || session.ResumeRequired {
		t.Fatal(session)
	}
	snapshot := ghcli.RateLimitSnapshot{Core: ghcli.RateLimitResource{Limit: 5000, Remaining: 0, ResetAt: now.Add(time.Hour)}}
	sessions := []state.Session{session}
	for i := 0; i < 5; i++ {
		sessions = app.notifyGitHubLowQuotaSessions(context.Background(), sessions, snapshot)
	}
	if len(base.calls) != 0 {
		t.Fatal("attempted notifications while quota exhausted", base.calls)
	}
	if sessions[0].ResumeAfter == "" {
		t.Fatal("missing recovery time")
	}
}

func TestInactiveSessionProcessesNewCommandWhenDueWithoutReplay(t *testing.T) {
	now := time.Date(2026, 10, 16, 11, 0, 0, 0, time.UTC)
	app, base := pollingTestApp(t, &now)
	sessions := []state.Session{{Repo: "owner/repo", IssueNumber: 1, Status: state.SessionStatusFailed}}
	poll := func() {
		ctx, finish := app.prepareCommentPolling(context.Background(), sessions)
		var err error
		sessions, err = app.processGitHubCleanupRequests(ctx, sessions)
		if err != nil {
			t.Fatal(err)
		}
		finish(sessions)
	}
	poll()
	base.comments = `[{"id":101,"body":"@vigilanteai cleanup","created_at":"2026-10-16T11:01:00Z","user":{"login":"alice"}}]`
	now = now.Add(time.Minute)
	poll()
	if sessions[0].LastCleanupCommentID != 0 {
		t.Fatal("polled inactive session before due time")
	}
	now = now.Add(29 * time.Minute)
	poll()
	if sessions[0].LastCleanupCommentID != 101 {
		t.Fatal("missed newly arriving cleanup command", sessions)
	}
	now = now.Add(30 * time.Minute)
	poll()
	writes := 0
	for cmd, n := range base.calls {
		if strings.HasPrefix(cmd, "gh issue comment ") {
			writes += n
		}
	}
	if writes != 1 {
		t.Fatalf("command replayed: %d replies", writes)
	}
}

func TestCommentPollingNonGitHubBackendsAreAlwaysDueAndNeverRescheduled(t *testing.T) {
	now := time.Date(2026, 10, 16, 11, 0, 0, 0, time.UTC)
	app, _ := pollingTestApp(t, &now)
	sessions := []state.Session{
		{Repo: "owner/repo", IssueNumber: 1, IssueBackend: "linear", Status: state.SessionStatusFailed},
		{Repo: "owner/repo", IssueNumber: 2, Status: state.SessionStatusFailed},
	}
	ctx, finish := app.prepareCommentPolling(context.Background(), sessions)
	if !commentPollDue(ctx, sessions[0]) {
		t.Fatal("non-GitHub session must not be gated by the GitHub schedule")
	}
	finish(sessions)
	for _, s := range sessions {
		if s.IssueBackend == "linear" && s.NextCommentPollAt != "" {
			t.Fatal("non-GitHub session was scheduled")
		}
	}
	// reconcile must leave them alone and make no GitHub calls.
	sessions[0].PullRequestNumber, sessions[0].PullRequestState = 3, "OPEN"
	app, base := pollingTestApp(t, &now)
	app.reconcileFailedPolling(context.Background(), sessions[:1], make(scanIssueDetailsCache))
	if len(base.calls) != 0 || sessions[0].PullRequestState != "OPEN" {
		t.Fatal(base.calls, sessions[0])
	}
}

func TestReconcileFailedPollingClosesUnavailableIssuesAndPullRequests(t *testing.T) {
	now := time.Now()
	app, base := pollingTestApp(t, &now)
	base.gone = true
	sessions := []state.Session{{Repo: "owner/repo", IssueNumber: 1, Status: state.SessionStatusFailed, WorktreePath: "keep-this"}}
	app.reconcileFailedPolling(context.Background(), sessions, make(scanIssueDetailsCache))
	if sessions[0].Status != state.SessionStatusClosed || sessions[0].WorktreePath != "keep-this" {
		t.Fatal(sessions)
	}

	base.gone, base.prGone = false, true
	sessions = []state.Session{{Repo: "owner/repo", IssueNumber: 1, Status: state.SessionStatusFailed, PullRequestNumber: 2, PullRequestState: "OPEN"}}
	app.reconcileFailedPolling(context.Background(), sessions, make(scanIssueDetailsCache))
	if sessions[0].PullRequestState != "CLOSED" || sessions[0].Status != state.SessionStatusFailed {
		t.Fatal(sessions)
	}
}

func TestCollectSessionCommentsDegradesWhenPullRequestSurfacesFail(t *testing.T) {
	now := time.Date(2026, 10, 16, 11, 0, 0, 0, time.UTC)
	for _, failPath := range []string{"pulls/2/comments", "issues/2/comments"} {
		t.Run(failPath, func(t *testing.T) {
			app, base := pollingTestApp(t, &now)
			base.failPath = failPath
			session := state.Session{Repo: "owner/repo", IssueNumber: 1, Status: state.SessionStatusBlocked, PullRequestNumber: 2, PullRequestState: "OPEN"}
			sessions := []state.Session{session}
			ctx, finish := app.prepareCommentPolling(context.Background(), sessions)
			if _, err := app.collectSessionComments(ctx, session, "iteration"); err != nil {
				t.Fatalf("partial PR failure should keep issue comments: %v", err)
			}
			finish(sessions)
			if sessions[0].NextCommentPollAt != "" {
				t.Fatal("incomplete poll must stay due for the next scan")
			}
		})
	}
}

func TestCommentLookupFailuresAreRecordedAndStaySchedulable(t *testing.T) {
	now := time.Date(2026, 10, 16, 11, 0, 0, 0, time.UTC)
	process := map[string]func(*App, context.Context, []state.Session) ([]state.Session, error){
		"cleanup":  (*App).processGitHubCleanupRequests,
		"recreate": (*App).processGitHubRecreateRequests,
		"resume": func(a *App, ctx context.Context, s []state.Session) ([]state.Session, error) {
			return a.processGitHubResumeRequests(ctx, s, make(scanIssueDetailsCache))
		},
	}
	for name, run := range process {
		t.Run(name, func(t *testing.T) {
			app, base := pollingTestApp(t, &now)
			base.failPath = "issues/1/comments"
			sessions := []state.Session{{Repo: "owner/repo", IssueNumber: 1, Status: state.SessionStatusFailed}}
			ctx, finish := app.prepareCommentPolling(context.Background(), sessions)
			out, err := run(app, ctx, sessions)
			if err != nil {
				t.Fatal(err)
			}
			finish(out)
			if out[0].NextCommentPollAt != "" {
				t.Fatalf("failed lookup was marked complete: %+v", out[0])
			}
		})
	}
}
