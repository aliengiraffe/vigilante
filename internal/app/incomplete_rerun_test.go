package app

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	issuerunner "github.com/nicobistolfi/vigilante/internal/runner"
	"github.com/nicobistolfi/vigilante/internal/state"
	"github.com/nicobistolfi/vigilante/internal/testutil"
)

type incompleteRerunFixture struct {
	app          *App
	repoPath     string
	worktreePath string
	now          time.Time
}

func newIncompleteRerunFixture(t *testing.T) incompleteRerunFixture {
	t.Helper()
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))

	repoPath := filepath.Join(home, "repo")
	worktreePath := filepath.Join(repoPath, ".worktrees", "vigilante", "issue-1")
	if err := os.MkdirAll(worktreePath, 0o755); err != nil {
		t.Fatal(err)
	}
	app := New()
	now := time.Date(2026, 3, 10, 15, 0, 0, 0, time.UTC)
	app.clock = func() time.Time { return now }
	app.stdout = &bytes.Buffer{}
	app.stderr = testutil.IODiscard{}
	if err := app.state.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	if err := app.state.SaveWatchTargets([]state.WatchTarget{{Path: repoPath, Repo: "owner/repo", Branch: "main"}}); err != nil {
		t.Fatal(err)
	}
	return incompleteRerunFixture{app: app, repoPath: repoPath, worktreePath: worktreePath, now: now}
}

func (f incompleteRerunFixture) session() state.Session {
	return state.Session{
		RepoPath:         f.repoPath,
		Repo:             "owner/repo",
		Provider:         "codex",
		IssueNumber:      1,
		IssueTitle:       "first",
		IssueURL:         "https://github.com/owner/repo/issues/1",
		BaseBranch:       "main",
		Branch:           "vigilante/issue-1",
		WorktreePath:     f.worktreePath,
		Status:           state.SessionStatusIncomplete,
		IncompleteReason: "no_durable_progress",
		StartedAt:        f.now.Add(-30 * time.Minute).Format(time.RFC3339),
		EndedAt:          f.now.Add(-20 * time.Minute).Format(time.RFC3339),
		UpdatedAt:        f.now.Add(-20 * time.Minute).Format(time.RFC3339),
	}
}

// scan mirrors the scan loop's rerun phase: snapshot, rerun, commit, wait.
func (f incompleteRerunFixture) scan(t *testing.T) []state.Session {
	t.Helper()
	snap, loaded, err := f.app.state.LoadSessionSnapshot(false)
	if err != nil {
		t.Fatal(err)
	}
	sessions, err := f.app.rerunIncompleteSessions(context.Background(), loaded, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.app.commitSessions(snap, sessions, "test"); err != nil {
		t.Fatal(err)
	}
	f.app.waitForSessions()
	stored, err := f.app.state.ListSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 1 {
		t.Fatalf("expected one session, got %#v", stored)
	}
	return stored
}

func countMatching(r *countingRunner, substr string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	total := 0
	for key, count := range r.counts {
		if strings.Contains(key, substr) {
			total += count
		}
	}
	return total
}

// Reproduces rhizome#136: an agent on a stacked-base issue keeps exiting 0
// with no PR and no changes. The first zero-progress rerun must end blocked,
// and later scans must not relaunch it.
func TestRerunIncompleteSessionsStopsZeroProgressLoop(t *testing.T) {
	f := newIncompleteRerunFixture(t)
	previous := f.session()
	// The previous attempt already ended on this exact commit and a clean tree.
	previous.LastAttemptFingerprint = "abc123:e3b0c44298fc1c14"

	expected := previous
	expected.Status = state.SessionStatusRunning
	expected.StartedAt = f.now.Format(time.RFC3339)
	expected.EndedAt = ""
	expected.LastHeartbeatAt = f.now.Format(time.RFC3339)
	expected.UpdatedAt = f.now.Format(time.RFC3339)
	expected.ProcessID = os.Getpid()
	expected.ReusedRemoteBranch = previous.Branch
	expected.StackedBaseBranch = "stack/base"
	expected.IncompleteRerunCount = 1
	expected.BranchDiffSummary = "No diff detected between `stack/base` and `" + previous.Branch + "`."
	expected.ContinuationSource = "incomplete_rerun"
	resumedComment := sessionStartCommentCommand("owner/repo", 1, f.worktreePath, expected)
	if !strings.Contains(resumedComment, "Diff summary against `stack/base`") {
		t.Fatalf("resume comment must compare against the stacked base: %q", resumedComment)
	}
	issuePrompt := issuePromptCommandForSession(f.worktreePath, "owner/repo", f.repoPath, 1, "first", "https://github.com/owner/repo/issues/1", expected)

	runner := &countingRunner{allowUnexpectedGH: true, base: testutil.FakeRunner{
		LookPaths: map[string]string{"git": "/usr/bin/git", "gh": "/usr/bin/gh", "codex": "/usr/bin/codex"},
		Outputs: map[string]string{
			"codex --version": "codex 1.0.0",
			"gh pr list --repo owner/repo --head " + previous.Branch + " --state all --json number,url,state,mergedAt": `[]`,
			"gh api repos/owner/repo/issues/1":                `{"title":"first","body":"Base branch: stack/base\n\nNeeds PR #12 first.","labels":[{"name":"vigilante:queued"}]}`,
			"git diff --stat stack/base..." + previous.Branch: "",
			resumedComment: "ok",
			preflightPromptCommandForSession(f.worktreePath, "owner/repo", f.repoPath, 1, "first", "https://github.com/owner/repo/issues/1", expected): "baseline ok",
			issuePrompt: "blocked on PR #12",
			"git rev-list --count origin/stack/base..HEAD": "0",
			"git rev-list --count origin/main..HEAD":       "5",
			"git status --porcelain":                       "",
			"git rev-parse HEAD":                           "abc123",
		},
	}}
	f.app.env.Runner = runner
	if err := f.app.state.ReplaceSessions([]state.Session{previous}); err != nil {
		t.Fatal(err)
	}

	stored := f.scan(t)
	if stored[0].Status != state.SessionStatusBlocked || stored[0].BlockedReason.Kind != issuerunner.BlockedKindNoProgressRepeated {
		t.Fatalf("expected the zero-progress rerun to block, got status=%s reason=%#v", stored[0].Status, stored[0].BlockedReason)
	}
	if stored[0].BlockedStage != "issue_execution" || !stored[0].ResumeRequired {
		t.Fatalf("expected an issue_execution block waiting for resume: %#v", stored[0])
	}

	for i := 0; i < 3; i++ {
		f.scan(t)
	}
	if got := countMatching(runner, issuePrompt); got != 1 {
		t.Fatalf("expected exactly one relaunch across scans, got %d", got)
	}
	if got := countMatching(runner, "--body ## ⚠️ Incomplete"); got != 0 {
		t.Fatalf("a blocked run must not post an Incomplete comment, got %d", got)
	}
	if got := countMatching(runner, "--body ## ⛔ Blocked"); got != 1 {
		t.Fatalf("expected one blocked comment, got %d", got)
	}
}

func TestRerunIncompleteSessionsBlocksAtRerunCap(t *testing.T) {
	f := newIncompleteRerunFixture(t)
	previous := f.session()
	previous.IncompleteReason = "commits_without_pr"
	previous.IncompleteRerunCount = state.DefaultIncompleteRerunLimit
	previous.EndedAt = f.now.Add(-3 * time.Hour).Format(time.RFC3339)
	runner := &countingRunner{allowUnexpectedGH: true, base: testutil.FakeRunner{Outputs: map[string]string{
		"gh pr list --repo owner/repo --head " + previous.Branch + " --state all --json number,url,state,mergedAt": `[]`,
		"gh api repos/owner/repo/issues/1": `{"title":"first","body":"","labels":[{"name":"vigilante:queued"}]}`,
	}}}
	f.app.env.Runner = runner
	if err := f.app.state.ReplaceSessions([]state.Session{previous}); err != nil {
		t.Fatal(err)
	}

	stored := f.scan(t)
	if stored[0].Status != state.SessionStatusBlocked || stored[0].BlockedReason.Kind != issuerunner.BlockedKindRerunLimit {
		t.Fatalf("expected the cap to block, got status=%s reason=%#v", stored[0].Status, stored[0].BlockedReason)
	}
	if got := countMatching(runner, "stopped after 3 automatic reruns"); got != 1 {
		t.Fatalf("expected one blocked comment explaining the cap, got %d", got)
	}
	if got := countMatching(runner, "codex exec"); got != 0 {
		t.Fatalf("a capped session must not relaunch, got %d", got)
	}
}

func TestRerunIncompleteSessionsHonorsBlockedLabel(t *testing.T) {
	f := newIncompleteRerunFixture(t)
	previous := f.session()
	runner := &countingRunner{allowUnexpectedGH: true, base: testutil.FakeRunner{Outputs: map[string]string{
		"gh pr list --repo owner/repo --head " + previous.Branch + " --state all --json number,url,state,mergedAt": `[]`,
		"gh api repos/owner/repo/issues/1": `{"title":"first","body":"","labels":[{"name":"vigilante:blocked"}]}`,
	}}}
	f.app.env.Runner = runner
	if err := f.app.state.ReplaceSessions([]state.Session{previous}); err != nil {
		t.Fatal(err)
	}

	stored := f.scan(t)
	if stored[0].Status != state.SessionStatusBlocked || stored[0].BlockedReason.Kind != issuerunner.BlockedKindIssueLabel {
		t.Fatalf("expected the label to block, got status=%s reason=%#v", stored[0].Status, stored[0].BlockedReason)
	}
	if got := countMatching(runner, "codex exec"); got != 0 {
		t.Fatalf("a labeled session must not relaunch, got %d", got)
	}
}

func TestRerunIncompleteSessionsWaitsForBackoff(t *testing.T) {
	f := newIncompleteRerunFixture(t)
	previous := f.session()
	previous.EndedAt = f.now.Add(-time.Minute).Format(time.RFC3339)
	runner := &countingRunner{base: testutil.FakeRunner{Outputs: map[string]string{
		"gh pr list --repo owner/repo --head " + previous.Branch + " --state all --json number,url,state,mergedAt": `[]`,
	}}}
	f.app.env.Runner = runner
	if err := f.app.state.ReplaceSessions([]state.Session{previous}); err != nil {
		t.Fatal(err)
	}

	stored := f.scan(t)
	if stored[0].Status != state.SessionStatusIncomplete || stored[0].IncompleteRerunCount != 0 {
		t.Fatalf("expected the session to wait out its backoff: %#v", stored[0])
	}
	if got := countMatching(runner, "gh api repos/owner/repo/issues/1"); got != 0 {
		t.Fatalf("a waiting session should not even look up the issue, got %d", got)
	}
}

// When the agent writes the blocked signal, the rerun ends blocked with the
// agent's reason, posts no Incomplete comment, and is not rerun.
func TestRerunIncompleteSessionsHonorsAgentBlockedSignal(t *testing.T) {
	f := newIncompleteRerunFixture(t)
	previous := f.session()
	previous.IncompleteReason = "commits_without_pr"

	expected := previous
	expected.Status = state.SessionStatusRunning
	expected.StartedAt = f.now.Format(time.RFC3339)
	expected.EndedAt = ""
	expected.LastHeartbeatAt = f.now.Format(time.RFC3339)
	expected.UpdatedAt = f.now.Format(time.RFC3339)
	expected.ProcessID = os.Getpid()
	expected.ReusedRemoteBranch = previous.Branch
	expected.IncompleteRerunCount = 1
	expected.BranchDiffSummary = "No diff detected between `main` and `" + previous.Branch + "`."
	expected.ContinuationSource = "incomplete_rerun"
	issuePrompt := issuePromptCommandForSession(f.worktreePath, "owner/repo", f.repoPath, 1, "first", "https://github.com/owner/repo/issues/1", expected)

	runner := &countingRunner{allowUnexpectedGH: true, base: testutil.FakeRunner{
		LookPaths: map[string]string{"git": "/usr/bin/git", "gh": "/usr/bin/gh", "codex": "/usr/bin/codex"},
		Outputs: map[string]string{
			"codex --version": "codex 1.0.0",
			"gh pr list --repo owner/repo --head " + previous.Branch + " --state all --json number,url,state,mergedAt": `[]`,
			"gh api repos/owner/repo/issues/1":                                    `{"title":"first","body":"","labels":[]}`,
			"git diff --stat main..." + previous.Branch:                           "",
			sessionStartCommentCommand("owner/repo", 1, f.worktreePath, expected): "ok",
			preflightPromptCommandForSession(f.worktreePath, "owner/repo", f.repoPath, 1, "first", "https://github.com/owner/repo/issues/1", expected): "baseline ok",
			issuePrompt:                              "blocked",
			"git rev-list --count origin/main..HEAD": "2",
			"git status --porcelain":                 "",
			"git rev-parse HEAD":                     "def456",
		},
	}}
	f.app.env.Runner = runner
	// Stands in for the agent writing the signal during its run.
	signalPath := filepath.Join(f.worktreePath, filepath.FromSlash(issuerunner.AgentBlockedSignalPath))
	if err := os.MkdirAll(filepath.Dir(signalPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(signalPath, []byte(`{"kind":"dependency_missing","summary":"PR #12 must merge into stack/base first"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := f.app.state.ReplaceSessions([]state.Session{previous}); err != nil {
		t.Fatal(err)
	}

	stored := f.scan(t)
	reason := stored[0].BlockedReason
	if stored[0].Status != state.SessionStatusBlocked || reason.Kind != issuerunner.BlockedKindDependencyMissing || !strings.Contains(reason.Summary, "PR #12") {
		t.Fatalf("expected the agent's blocker, got status=%s reason=%#v", stored[0].Status, reason)
	}
	if _, err := os.Stat(signalPath); !os.IsNotExist(err) {
		t.Fatalf("expected the signal file to be consumed, stat err = %v", err)
	}
	f.scan(t)
	if got := countMatching(runner, issuePrompt); got != 1 {
		t.Fatalf("expected no rerun after the agent blocked, got %d launches", got)
	}
	if got := countMatching(runner, "--body ## ⚠️ Incomplete"); got != 0 {
		t.Fatalf("expected no Incomplete comment, got %d", got)
	}
	if got := countMatching(runner, "PR #12 must merge"); got != 1 {
		t.Fatalf("expected one blocked comment with the agent's summary, got %d", got)
	}
}
