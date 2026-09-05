package app

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	ghcli "github.com/nicobistolfi/vigilante/internal/github"
	"github.com/nicobistolfi/vigilante/internal/state"
	"github.com/nicobistolfi/vigilante/internal/testutil"
)

func TestHasClosingReference(t *testing.T) {
	cases := []struct {
		name        string
		body        string
		issueNumber int
		want        bool
	}{
		{name: "present", body: "Summary\n\nCloses #1", issueNumber: 1, want: true},
		{name: "absent", body: "Summary with no reference at all", issueNumber: 1, want: false},
		{name: "wrong issue number", body: "Closes #12", issueNumber: 1, want: false},
		{name: "prefix of another number", body: "Closes #123", issueNumber: 12, want: false},
		{name: "close", body: "close #1", issueNumber: 1, want: true},
		{name: "closes", body: "closes #1", issueNumber: 1, want: true},
		{name: "closed", body: "closed #1", issueNumber: 1, want: true},
		{name: "fix", body: "fix #1", issueNumber: 1, want: true},
		{name: "fixes", body: "fixes #1", issueNumber: 1, want: true},
		{name: "fixed", body: "fixed #1", issueNumber: 1, want: true},
		{name: "resolve", body: "resolve #1", issueNumber: 1, want: true},
		{name: "resolves", body: "resolves #1", issueNumber: 1, want: true},
		{name: "resolved", body: "resolved #1", issueNumber: 1, want: true},
		{name: "mixed case", body: "ReSoLvEs #7", issueNumber: 7, want: true},
		{name: "colon separator", body: "Closes: #7", issueNumber: 7, want: true},
		{name: "keyword without hash", body: "Closes 1", issueNumber: 1, want: false},
		{name: "hash without keyword", body: "See #1 for context", issueNumber: 1, want: false},
		{name: "multiple references one matches", body: "Closes #9\nFixes #1\n", issueNumber: 1, want: true},
		{name: "cross repo reference", body: "Closes owner/repo#5", issueNumber: 5, want: false},
		{name: "unrelated word ending in fix", body: "hotfix #1", issueNumber: 1, want: false},
		{name: "zero issue number", body: "Closes #0", issueNumber: 0, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasClosingReference(tc.body, tc.issueNumber); got != tc.want {
				t.Fatalf("hasClosingReference(%q, %d) = %v, want %v", tc.body, tc.issueNumber, got, tc.want)
			}
		})
	}
}

func TestAppendClosingReference(t *testing.T) {
	cases := []struct {
		name        string
		body        string
		issueNumber int
		want        string
	}{
		{name: "empty body", body: "", issueNumber: 1, want: "Closes #1"},
		{name: "trailing newline", body: "Summary\n", issueNumber: 1, want: "Summary\n\nCloses #1"},
		{name: "no trailing newline", body: "Summary", issueNumber: 1, want: "Summary\n\nCloses #1"},
		{name: "unrelated closing reference", body: "Closes #9", issueNumber: 1, want: "Closes #9\n\nCloses #1"},
		{name: "preserves fenced content", body: "```\nCloses #2\n```", issueNumber: 1, want: "```\nCloses #2\n```\n\nCloses #1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := appendClosingReference(tc.body, tc.issueNumber)
			if got != tc.want {
				t.Fatalf("appendClosingReference(%q, %d) = %q, want %q", tc.body, tc.issueNumber, got, tc.want)
			}
			if !strings.HasPrefix(got, tc.body) {
				t.Fatalf("original body must be preserved as a prefix: %q", got)
			}
			if !hasClosingReference(got, tc.issueNumber) {
				t.Fatalf("appended body must contain a closing reference: %q", got)
			}
		})
	}
}

// prLinkRunner delegates to a FakeRunner while counting how many times each
// command was invoked, so tests can assert that a repair is not retried.
type prLinkRunner struct {
	testutil.FakeRunner
	calls map[string]int
}

func (r *prLinkRunner) Run(ctx context.Context, dir string, name string, args ...string) (string, error) {
	r.calls[testutil.Key(name, args...)]++
	return r.FakeRunner.Run(ctx, dir, name, args...)
}

func (r *prLinkRunner) RunWithStdin(ctx context.Context, stdin string, dir string, name string, args ...string) (string, error) {
	r.calls[testutil.Key(name, args...)]++
	return r.FakeRunner.RunWithStdin(ctx, stdin, dir, name, args...)
}

func newPullRequestLinkApp(t *testing.T, runner *prLinkRunner) *App {
	t.Helper()
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	t.Setenv("HOME", home)

	app := New()
	app.stdout = testutil.IODiscard{}
	app.stderr = testutil.IODiscard{}
	app.clock = func() time.Time { return time.Date(2026, 6, 1, 18, 0, 0, 0, time.UTC) }
	app.env.Runner = runner
	return app
}

func newPullRequestLinkRunner(outputs map[string]string, errs map[string]error) *prLinkRunner {
	return &prLinkRunner{
		FakeRunner: testutil.FakeRunner{
			Outputs:     outputs,
			Errors:      errs,
			StdinInputs: map[string]string{},
		},
		calls: map[string]int{},
	}
}

func pullRequestLinkSession() state.Session {
	return state.Session{
		Repo:        "owner/repo",
		IssueNumber: 1,
		Branch:      "vigilante/issue-1",
	}
}

const prEditCommand = "gh pr edit --repo owner/repo 31 --body-file -"

func TestEnsurePullRequestIssueLinkAppendsMissingReference(t *testing.T) {
	runner := newPullRequestLinkRunner(map[string]string{prEditCommand: ""}, nil)
	app := newPullRequestLinkApp(t, runner)
	session := pullRequestLinkSession()

	app.ensurePullRequestIssueLink(context.Background(), &session, ghcli.PullRequest{Number: 31, State: "OPEN", Body: "Adds a `--flag`\n"})

	if got, want := runner.calls[prEditCommand], 1; got != want {
		t.Fatalf("pr edit calls = %d, want %d", got, want)
	}
	if got, want := runner.StdinInputs[prEditCommand], "Adds a `--flag`\n\nCloses #1"; got != want {
		t.Fatalf("piped body = %q, want %q", got, want)
	}
	if session.PullRequestLinkAttemptedForPR != 31 {
		t.Fatalf("expected the repair to be recorded for PR 31: %#v", session)
	}
	if session.PullRequestLinkRepairedAt == "" {
		t.Fatalf("expected a repair timestamp: %#v", session)
	}
}

func TestEnsurePullRequestIssueLinkSkipsUpdateWhenAlreadyLinked(t *testing.T) {
	runner := newPullRequestLinkRunner(map[string]string{prEditCommand: ""}, nil)
	app := newPullRequestLinkApp(t, runner)
	session := pullRequestLinkSession()

	app.ensurePullRequestIssueLink(context.Background(), &session, ghcli.PullRequest{Number: 31, State: "OPEN", Body: "Summary\n\nCloses #1"})

	if got := runner.calls[prEditCommand]; got != 0 {
		t.Fatalf("expected no pr edit for an already-linked PR, got %d calls", got)
	}
	if session.PullRequestLinkAttemptedForPR != 31 {
		t.Fatalf("expected the check to be recorded for PR 31: %#v", session)
	}
	if session.PullRequestLinkRepairedAt != "" {
		t.Fatalf("expected no repair timestamp for an already-linked PR: %#v", session)
	}
}

func TestEnsurePullRequestIssueLinkDoesNotRetryAfterFailedUpdate(t *testing.T) {
	runner := newPullRequestLinkRunner(nil, map[string]error{prEditCommand: errors.New("forbidden")})
	app := newPullRequestLinkApp(t, runner)
	session := pullRequestLinkSession()
	session.Status = state.SessionStatusSuccess
	pr := ghcli.PullRequest{Number: 31, State: "OPEN", Body: "Summary"}

	app.ensurePullRequestIssueLink(context.Background(), &session, pr)
	app.ensurePullRequestIssueLink(context.Background(), &session, pr)

	if got, want := runner.calls[prEditCommand], 1; got != want {
		t.Fatalf("pr edit calls = %d, want %d", got, want)
	}
	if session.PullRequestLinkAttemptedForPR != 31 {
		t.Fatalf("expected the failed attempt to be recorded: %#v", session)
	}
	if session.PullRequestLinkRepairedAt != "" {
		t.Fatalf("expected no repair timestamp after a failed update: %#v", session)
	}
	if session.Status != state.SessionStatusSuccess {
		t.Fatalf("expected session status to be unaffected by a failed repair: %#v", session)
	}
}

func TestEnsurePullRequestIssueLinkFetchesBodyWhenLookupOmitsIt(t *testing.T) {
	const viewCommand = "gh pr view --repo owner/repo 31 --json number,title,body,url,state,mergedAt,labels,isDraft,mergeable,mergeStateStatus,reviewDecision,statusCheckRollup,baseRefName,headRefOid"
	runner := newPullRequestLinkRunner(map[string]string{
		viewCommand:   `{"number":31,"title":"PR","body":"Summary\n\nCloses #1","url":"https://github.com/owner/repo/pull/31","state":"OPEN","mergedAt":null,"baseRefName":"main"}`,
		prEditCommand: "",
	}, nil)
	app := newPullRequestLinkApp(t, runner)
	session := pullRequestLinkSession()

	app.ensurePullRequestIssueLink(context.Background(), &session, ghcli.PullRequest{Number: 31, State: "OPEN"})

	if got, want := runner.calls[viewCommand], 1; got != want {
		t.Fatalf("pr view calls = %d, want %d", got, want)
	}
	if got := runner.calls[prEditCommand]; got != 0 {
		t.Fatalf("expected no pr edit for an already-linked PR, got %d calls", got)
	}
}

func TestEnsurePullRequestIssueLinkSkipsNonGitHubIssueBackend(t *testing.T) {
	runner := newPullRequestLinkRunner(map[string]string{prEditCommand: ""}, nil)
	app := newPullRequestLinkApp(t, runner)
	if err := app.state.SaveWatchTargets([]state.WatchTarget{{
		Path:         t.TempDir(),
		Repo:         "owner/repo",
		Branch:       "main",
		IssueBackend: "linear",
		IssueStage:   "In Progress",
	}}); err != nil {
		t.Fatal(err)
	}
	session := pullRequestLinkSession()

	app.ensurePullRequestIssueLink(context.Background(), &session, ghcli.PullRequest{Number: 31, State: "OPEN", Body: "Summary"})

	if got := runner.calls[prEditCommand]; got != 0 {
		t.Fatalf("expected no pr edit for a non-GitHub issue backend, got %d calls", got)
	}
	if session.PullRequestLinkAttemptedForPR != 0 {
		t.Fatalf("expected no recorded attempt for a non-GitHub issue backend: %#v", session)
	}
}

func TestEnsurePullRequestIssueLinkSkipsForkSessionWithUpstreamRepo(t *testing.T) {
	runner := newPullRequestLinkRunner(map[string]string{prEditCommand: ""}, nil)
	app := newPullRequestLinkApp(t, runner)
	session := pullRequestLinkSession()
	session.ForkMode = true
	session.ForkOwner = "contributor"
	session.UpstreamRepo = "upstream/repo"

	app.ensurePullRequestIssueLink(context.Background(), &session, ghcli.PullRequest{Number: 31, State: "OPEN", Body: "Summary"})

	if got := runner.calls[prEditCommand]; got != 0 {
		t.Fatalf("expected no pr edit for a fork session with an upstream repo, got %d calls", got)
	}
	if session.PullRequestLinkAttemptedForPR != 0 {
		t.Fatalf("expected no recorded attempt for a fork session: %#v", session)
	}
}

func TestEnsurePullRequestIssueLinkSkipsPullRequestsThatAreNotOpen(t *testing.T) {
	merged := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		pr   ghcli.PullRequest
	}{
		{name: "closed", pr: ghcli.PullRequest{Number: 31, State: "CLOSED", Body: "Summary"}},
		{name: "merged", pr: ghcli.PullRequest{Number: 31, State: "MERGED", Body: "Summary", MergedAt: &merged}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runner := newPullRequestLinkRunner(map[string]string{prEditCommand: ""}, nil)
			app := newPullRequestLinkApp(t, runner)
			session := pullRequestLinkSession()

			app.ensurePullRequestIssueLink(context.Background(), &session, tc.pr)

			if got := runner.calls[prEditCommand]; got != 0 {
				t.Fatalf("expected no pr edit for a %s PR, got %d calls", tc.name, got)
			}
		})
	}
}

func TestRunPullRequestMaintenanceRepairsMissingIssueLink(t *testing.T) {
	const listCommand = "gh pr list --repo owner/repo --head vigilante/issue-1 --state all --json number,url,state,mergedAt"
	const viewCommand = "gh pr view --repo owner/repo 31 --json number,title,body,url,state,mergedAt,labels,isDraft,mergeable,mergeStateStatus,reviewDecision,statusCheckRollup,baseRefName,headRefOid"
	runner := newPullRequestLinkRunner(
		map[string]string{
			listCommand:   `[{"number":31,"url":"https://github.com/owner/repo/pull/31","state":"OPEN","mergedAt":null}]`,
			viewCommand:   `{"number":31,"title":"PR","body":"","url":"https://github.com/owner/repo/pull/31","state":"OPEN","mergedAt":null,"baseRefName":"main"}`,
			prEditCommand: "",
		},
		map[string]error{"git fetch origin main": errors.New("fetch failed")},
	)
	app := newPullRequestLinkApp(t, runner)

	repoPath := t.TempDir()
	session := pullRequestLinkSession()
	session.RepoPath = repoPath
	session.WorktreePath = filepath.Join(repoPath, ".worktrees", "vigilante", "issue-1")
	session.BaseBranch = "main"
	session.Status = state.SessionStatusSuccess
	session.ResumeHint = "vigilante resume --repo owner/repo --issue 1"

	if _, _, err := app.runPullRequestMaintenance(context.Background(), &session, nil); err == nil {
		t.Fatal("expected maintenance failure from the stubbed git fetch")
	}
	if got, want := runner.calls[prEditCommand], 1; got != want {
		t.Fatalf("pr edit calls = %d, want %d", got, want)
	}
	if got, want := runner.StdinInputs[prEditCommand], "Closes #1"; got != want {
		t.Fatalf("piped body = %q, want %q", got, want)
	}
	if session.PullRequestLinkAttemptedForPR != 31 {
		t.Fatalf("expected the repair to be recorded for PR 31: %#v", session)
	}
}
