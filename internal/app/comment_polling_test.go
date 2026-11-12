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
	// Repository-wide sweep surfaces. repoComments is served for
	// repos/{repo}/issues/comments, repoReviewComments for repos/{repo}/pulls/comments.
	// Each surface carries an ETag derived from sweepVersion; a matching
	// If-None-Match answers 304 Not Modified with no body.
	repoComments       string
	repoReviewComments string
	sweepVersion       int
	sweepFailure       string // sweep endpoints fail with this text when set
	issueDetailsError  string // issue detail reads fail with this text when set
	issueListPerCall   []time.Time
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
			return fmt.Sprintf(`{"resources":{"core":{"limit":5000,"remaining":5000,"reset":%d},"graphql":{"limit":5000,"remaining":5000,"reset":%d}}}`, r.now().Add(time.Hour).Unix(), r.now().Add(time.Hour).Unix()), nil
		}
		if strings.Contains(endpoint, "/issues/comments?") || strings.Contains(endpoint, "/pulls/comments?") {
			return r.sweepResponse(endpoint, args)
		}
		if strings.Contains(endpoint, "reactions") {
			return `{"id":1}`, nil
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
		if r.issueDetailsError != "" {
			return "", fmt.Errorf("%s", r.issueDetailsError)
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
		r.issueListPerCall = append(r.issueListPerCall, r.now())
		return "[]", nil
	}
	return "", fmt.Errorf("unexpected command: %s", key)
}

func (r *pollingFixtureRunner) sweepResponse(endpoint string, args []string) (string, error) {
	if r.sweepFailure != "" {
		return "", fmt.Errorf("%s", r.sweepFailure)
	}
	surface, body := "issues", r.repoComments
	if strings.Contains(endpoint, "/pulls/comments?") {
		surface, body = "pulls", r.repoReviewComments
	}
	if body == "" {
		body = "[]"
	}
	etag := fmt.Sprintf(`W/"%s-%d"`, surface, r.sweepVersion)
	for i, a := range args {
		if a == "-H" && i+1 < len(args) && args[i+1] == "If-None-Match: "+etag {
			// gh exits 1 for any status above 299, 304 included.
			return "HTTP/1.1 304 Not Modified\r\nETag: " + etag + "\r\n\r\ngh: HTTP 304\n", fmt.Errorf("gh [api]: exit status 1")
		}
	}
	if !strings.Contains(strings.Join(args, " "), "--include") {
		return body, nil
	}
	return "HTTP/1.1 200 OK\r\nETag: " + etag + "\r\nX-RateLimit-Resource: core\r\nX-RateLimit-Remaining: 4900\r\n\r\n" + body, nil
}

// restRequests counts dispatched REST requests that GitHub meters: explicit
// `gh api` calls other than the quota probe, minus conditional sweeps answered 304.
func (r *pollingFixtureRunner) restRequests() (metered int, sweeps int, notModified int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for cmd, n := range r.calls {
		if !strings.HasPrefix(cmd, "gh api ") || strings.Contains(cmd, "/rate_limit") {
			continue
		}
		metered += n
		if strings.Contains(cmd, "/issues/comments?") || strings.Contains(cmd, "/pulls/comments?") {
			sweeps += n
			if strings.Contains(cmd, "If-None-Match") {
				notModified += n
				metered -= n
			}
		}
	}
	return metered, sweeps, notModified
}

func (r *pollingFixtureRunner) countCalls(substr string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	total := 0
	for cmd, n := range r.calls {
		if strings.Contains(cmd, substr) {
			total += n
		}
	}
	return total
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

// historicalSessionsFixture mirrors a real watch list: 7 repositories, 118
// non-closed failed sessions, 91 of them with an open pull request, all ended
// recently enough to stay within the polling retention window.
func historicalSessionsFixture(t *testing.T, now time.Time) ([]state.WatchTarget, []state.Session) {
	t.Helper()
	var sessions []state.Session
	var targets []state.WatchTarget
	for i := 0; i < 7; i++ {
		targets = append(targets, state.WatchTarget{Repo: "owner/repo" + strconv.Itoa(i), Path: t.TempDir(), Branch: "main", BranchMode: state.BranchModePinned, Assignee: "alice"})
	}
	for i := 0; i < 118; i++ {
		s := state.Session{Repo: targets[i%7].Repo, RepoPath: targets[i%7].Path, IssueNumber: i + 1, Status: state.SessionStatusFailed, StartedAt: now.Add(-3 * 24 * time.Hour).Format(time.RFC3339), EndedAt: now.Add(-2 * 24 * time.Hour).Format(time.RFC3339)}
		if i < 91 {
			s.PullRequestNumber = i + 1000
			s.PullRequestState = "OPEN"
		}
		sessions = append(sessions, s)
	}
	return targets, sessions
}

func TestCommentPollingSimulatedHourWithHistoricalSessions(t *testing.T) {
	now := time.Date(2026, 10, 16, 11, 0, 0, 0, time.UTC)
	app, base := pollingTestApp(t, &now)
	targets, sessions := historicalSessionsFixture(t, now)
	if err := app.state.SaveWatchTargets(targets); err != nil {
		t.Fatal(err)
	}
	if err := app.state.ReplaceSessions(sessions); err != nil {
		t.Fatal(err)
	}
	for minute := 0; minute < 60; minute++ {
		before := len(base.issueListPerCall)
		if err := app.ScanOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
		app.waitForSessions()
		if got := len(base.issueListPerCall) - before; got != len(targets) {
			t.Fatalf("minute %d: issue intake ran for %d of %d targets; a session backlog must never defer intake", minute, got, len(targets))
		}
		now = now.Add(time.Minute)
	}
	metered, sweeps, notModified := base.restRequests()
	if metered >= 500 {
		t.Fatalf("metered REST requests=%d, want fewer than 500 for the hour", metered)
	}
	if max := 2 * len(targets) * 60; sweeps > max {
		t.Fatalf("comment sweep requests=%d, want at most %d (two per target per scan)", sweeps, max)
	}
	if notModified < sweeps-2*len(targets) {
		t.Fatalf("unchanged repositories should answer 304: sweeps=%d not_modified=%d", sweeps, notModified)
	}
	if n := base.countCalls("/issues/1/comments"); n != 0 {
		t.Fatalf("per-issue comment polling still dispatched %d requests", n)
	}
	if n := base.countCalls("gh pr view"); n != 91 {
		t.Fatalf("pull request state reads=%d, want one per open-PR session per retention window", n)
	}
	status := app.githubBudget.Status()
	if status.Used >= 500 || status.GraphQLUsed > 2000 {
		t.Fatalf("unexpected budget consumption: %+v", status)
	}
	persisted, err := app.state.ListSessions()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range persisted {
		if s.NextCommentPollAt == "" {
			t.Fatalf("session %d starved in first sweep", s.IssueNumber)
		}
	}
	savedTargets, err := app.state.LoadWatchTargets()
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range savedTargets {
		if target.CommentSweep == nil || target.CommentSweep.Since == "" || target.CommentSweep.IssueCommentsETag == "" || target.CommentSweep.ReviewCommentsETag == "" {
			t.Fatalf("sweep cursor not persisted for %s: %+v", target.Repo, target.CommentSweep)
		}
	}
	t.Logf("simulated hour: metered REST=%d sweeps=%d not_modified=%d rest_units=%d graphql_units=%d", metered, sweeps, notModified, status.Used, status.GraphQLUsed)
}

func TestCommentSweepRoutesCommandsAcrossSurfacesAndIgnoresUntrackedIssues(t *testing.T) {
	now := time.Date(2026, 10, 16, 11, 0, 0, 0, time.UTC)
	app, base := pollingTestApp(t, &now)
	targets := []state.WatchTarget{{Repo: "owner/repo", Path: t.TempDir(), Branch: "main", BranchMode: state.BranchModePinned, Assignee: "alice"}}
	sessions := []state.Session{
		{Repo: "owner/repo", IssueNumber: 1, Status: state.SessionStatusFailed, EndedAt: now.Add(-time.Hour).Format(time.RFC3339)},
		{Repo: "owner/repo", IssueNumber: 2, Status: state.SessionStatusFailed, PullRequestNumber: 20, PullRequestState: "OPEN", EndedAt: now.Add(-time.Hour).Format(time.RFC3339)},
		{Repo: "owner/repo", IssueNumber: 3, Status: state.SessionStatusFailed, PullRequestNumber: 30, PullRequestState: "OPEN", EndedAt: now.Add(-time.Hour).Format(time.RFC3339)},
	}
	stamp := now.Add(-30 * time.Second).Format(time.RFC3339)
	base.repoComments = fmt.Sprintf(`[
		{"id":101,"body":"@vigilanteai cleanup","created_at":%[1]q,"updated_at":%[1]q,"user":{"login":"alice"},"issue_url":"https://api.github.com/repos/owner/repo/issues/1"},
		{"id":102,"body":"@vigilanteai cleanup","created_at":%[1]q,"updated_at":%[1]q,"user":{"login":"alice"},"issue_url":"https://api.github.com/repos/owner/repo/issues/20"},
		{"id":103,"body":"@vigilanteai recreate","created_at":%[1]q,"updated_at":%[1]q,"user":{"login":"mallory"},"issue_url":"https://api.github.com/repos/owner/repo/issues/999"}
	]`, stamp)
	base.repoReviewComments = fmt.Sprintf(`[
		{"id":104,"body":"@vigilanteai cleanup","created_at":%[1]q,"updated_at":%[1]q,"user":{"login":"alice"},"pull_request_url":"https://api.github.com/repos/owner/repo/pulls/30"}
	]`, stamp)
	ctx, finish := app.prepareCommentPolling(context.Background(), sessions)
	app.runCommentSweeps(ctx, targets, sessions)
	var err error
	sessions, err = app.processGitHubCleanupRequests(ctx, sessions)
	if err != nil {
		t.Fatal(err)
	}
	sessions, err = app.processGitHubRecreateRequests(ctx, sessions)
	if err != nil {
		t.Fatal(err)
	}
	finish(sessions)
	want := map[int]int64{1: 101, 2: 102, 3: 104}
	for _, s := range sessions {
		if s.LastCleanupCommentID != want[s.IssueNumber] {
			t.Fatalf("issue %d: cleanup comment %d, want %d", s.IssueNumber, s.LastCleanupCommentID, want[s.IssueNumber])
		}
		if s.LastRecreateCommentID != 0 {
			t.Fatalf("issue %d: recreate command from an untracked issue was applied", s.IssueNumber)
		}
	}
	if n := base.countCalls("reactions"); n != 3 {
		t.Fatalf("reactions=%d, want exactly one per routed command", n)
	}
	if n := base.countCalls("/issues/1/comments"); n != 0 {
		t.Fatal("covered repository still polled per issue")
	}
	if targets[0].CommentSweep == nil || targets[0].CommentSweep.Since != stamp {
		t.Fatalf("cursor should advance to the newest comment: %+v", targets[0].CommentSweep)
	}

	// Later scans with unchanged content must not replay commands. The scan
	// right after the cursor advanced fetches the new URL once (no ETag yet);
	// from then on both surfaces are conditional and answer 304.
	for i := 0; i < 2; i++ {
		now = now.Add(time.Minute)
		ctx, finish = app.prepareCommentPolling(context.Background(), sessions)
		app.runCommentSweeps(ctx, targets, sessions)
		sessions, err = app.processGitHubCleanupRequests(ctx, sessions)
		if err != nil {
			t.Fatal(err)
		}
		finish(sessions)
	}
	if n := base.countCalls("reactions"); n != 3 {
		t.Fatalf("commands replayed: reactions=%d", n)
	}
	if n := base.countCalls("If-None-Match"); n != 2 {
		t.Fatalf("third sweep should be conditional on both surfaces, got %d", n)
	}
}

func TestCommentSweepFailureFallsBackToScheduledPerIssuePolling(t *testing.T) {
	now := time.Date(2026, 10, 16, 11, 0, 0, 0, time.UTC)
	app, base := pollingTestApp(t, &now)
	base.sweepFailure = "boom"
	base.comments = `[{"id":7,"body":"@vigilanteai cleanup","created_at":"2026-10-16T10:59:00Z","user":{"login":"alice"}}]`
	targets := []state.WatchTarget{{Repo: "owner/repo", Path: t.TempDir(), Branch: "main", BranchMode: state.BranchModePinned, Assignee: "alice"}}
	sessions := []state.Session{{Repo: "owner/repo", IssueNumber: 1, Status: state.SessionStatusFailed, EndedAt: now.Add(-time.Hour).Format(time.RFC3339)}}
	ctx, finish := app.prepareCommentPolling(context.Background(), sessions)
	app.runCommentSweeps(ctx, targets, sessions)
	sessions, err := app.processGitHubCleanupRequests(ctx, sessions)
	if err != nil {
		t.Fatal(err)
	}
	finish(sessions)
	if sessions[0].LastCleanupCommentID != 7 {
		t.Fatalf("legacy polling did not run after sweep failure: %+v", sessions[0])
	}
	if targets[0].CommentSweep != nil {
		t.Fatal("failed sweep must not advance the cursor")
	}
}

func TestFailedSessionsRetireFromPollingByAgeAndPullRequestState(t *testing.T) {
	now := time.Date(2026, 10, 16, 11, 0, 0, 0, time.UTC)
	app, base := pollingTestApp(t, &now)
	sessions := []state.Session{
		{Repo: "owner/repo", IssueNumber: 1, Status: state.SessionStatusFailed, PullRequestNumber: 10, PullRequestState: "OPEN", EndedAt: now.Add(-15 * 24 * time.Hour).Format(time.RFC3339)},
		{Repo: "owner/repo", IssueNumber: 2, Status: state.SessionStatusFailed, PullRequestNumber: 20, PullRequestState: "MERGED", EndedAt: now.Add(-time.Hour).Format(time.RFC3339)},
		{Repo: "owner/repo", IssueNumber: 3, Status: state.SessionStatusFailed, PullRequestNumber: 30, PullRequestState: "CLOSED", EndedAt: now.Add(-time.Hour).Format(time.RFC3339)},
		{Repo: "owner/repo", IssueNumber: 4, Status: state.SessionStatusFailed, PullRequestNumber: 40, PullRequestState: "OPEN", EndedAt: now.Add(-time.Hour).Format(time.RFC3339)},
	}
	ctx, finish := app.prepareCommentPolling(context.Background(), sessions)
	for _, s := range sessions {
		if want := s.IssueNumber == 4; commentPollDue(ctx, s) != want {
			t.Fatalf("issue %d due=%v want %v", s.IssueNumber, commentPollDue(ctx, s), want)
		}
	}
	app.reconcileFailedPolling(ctx, sessions, make(scanIssueDetailsCache))
	if n := base.countCalls("gh pr view"); n != 1 {
		t.Fatalf("pr view calls=%d, want only the retained open-PR session", n)
	}
	finish(sessions)
	if sessions[0].NextCommentPollAt != "" || sessions[1].NextCommentPollAt != "" {
		t.Fatal("retired sessions were scheduled")
	}
	listed, err := app.state.ListSessions()
	if err != nil {
		t.Fatal(err)
	}
	_ = listed // retired sessions stay in the store; nothing deletes them.
	if len(sessions) != 4 {
		t.Fatal("sessions were removed")
	}

	// Explicit configuration shortens the retention window.
	config, _ := app.state.LoadServiceConfig()
	config.GitHubFailedSessionPollRetention = "30m"
	if err := app.state.SaveServiceConfig(config); err != nil {
		t.Fatal(err)
	}
	ctx, _ = app.prepareCommentPolling(context.Background(), sessions)
	if commentPollDue(ctx, sessions[3]) {
		t.Fatal("session older than the configured retention is still due")
	}
}

func TestReconcileFailedPollingReadsPullRequestStateOncePerRetentionWindow(t *testing.T) {
	now := time.Date(2026, 10, 16, 11, 0, 0, 0, time.UTC)
	app, base := pollingTestApp(t, &now)
	sessions := []state.Session{{Repo: "owner/repo", IssueNumber: 1, Status: state.SessionStatusFailed, PullRequestNumber: 10, PullRequestState: "OPEN", EndedAt: now.Add(-time.Hour).Format(time.RFC3339)}}
	for i := 0; i < 3; i++ {
		ctx, finish := app.prepareCommentPolling(context.Background(), sessions)
		app.reconcileFailedPolling(ctx, sessions, make(scanIssueDetailsCache))
		finish(sessions)
		now = now.Add(31 * time.Minute)
	}
	if n := base.countCalls("gh pr view"); n != 1 {
		t.Fatalf("pr view calls=%d, want 1 within the retention window", n)
	}
	if n := base.countCalls("gh api repos/owner/repo/issues/1"); n != 3 {
		t.Fatalf("issue state reads=%d, want one per due poll", n)
	}
}

func TestIssueLookupBacksOffAfterRepeatedNonHTTPFailures(t *testing.T) {
	now := time.Date(2026, 10, 16, 11, 0, 0, 0, time.UTC)
	app, base := pollingTestApp(t, &now)
	base.issueDetailsError = "error connecting to api.github.com"
	sessions := []state.Session{{Repo: "owner/repo", IssueNumber: 1, Status: state.SessionStatusFailed, EndedAt: now.Add(-time.Hour).Format(time.RFC3339)}}
	// Once the threshold is reached the delay doubles each failure (1m, 2m, 4m,
	// ...), so twelve one-minute scans perform far fewer than twelve lookups.
	scans := 12
	for i := 0; i < scans; i++ {
		app.reconcileFailedPolling(context.Background(), sessions, make(scanIssueDetailsCache))
		now = now.Add(time.Minute)
	}
	if n := base.countCalls("gh api repos/owner/repo/issues/1"); n < issueLookupBackoffThreshold || n > issueLookupBackoffThreshold+3 {
		t.Fatalf("lookups=%d over %d scans, want backoff after %d failures", n, scans, issueLookupBackoffThreshold)
	}
	if sessions[0].IssueLookupBackoffUntil == "" || sessions[0].IssueLookupFailures < issueLookupBackoffThreshold {
		t.Fatalf("backoff not recorded: %+v", sessions[0])
	}
	// HTTP-classified failures never trigger backoff.
	base.issueDetailsError = "HTTP 500: upstream"
	other := []state.Session{{Repo: "owner/repo", IssueNumber: 2, Status: state.SessionStatusFailed, EndedAt: now.Add(-time.Hour).Format(time.RFC3339)}}
	for i := 0; i < issueLookupBackoffThreshold+1; i++ {
		app.reconcileFailedPolling(context.Background(), other, make(scanIssueDetailsCache))
	}
	if other[0].IssueLookupFailures != 0 || other[0].IssueLookupBackoffUntil != "" {
		t.Fatalf("HTTP failure triggered backoff: %+v", other[0])
	}
	// A successful lookup clears the backoff state once the delay has elapsed.
	base.issueDetailsError = ""
	now = now.Add(issueLookupBackoffMax)
	app.reconcileFailedPolling(context.Background(), sessions, make(scanIssueDetailsCache))
	if sessions[0].IssueLookupFailures != 0 || sessions[0].IssueLookupBackoffUntil != "" {
		t.Fatalf("backoff not cleared after success: %+v", sessions[0])
	}
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
