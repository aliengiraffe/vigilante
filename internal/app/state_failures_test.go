package app

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nicobistolfi/vigilante/internal/environment"
	ghcli "github.com/nicobistolfi/vigilante/internal/github"
	"github.com/nicobistolfi/vigilante/internal/state"
	"github.com/nicobistolfi/vigilante/internal/testutil"
)

// execStateSQL runs one statement against the app's state database through a
// second connection, the way another process would, so a test can break or
// change the schema under an app that already holds the database open. The
// "sqlite" driver is registered by the state package.
func execStateSQL(t *testing.T, app *App, stmt string) {
	t.Helper()
	conn, err := sql.Open("sqlite", app.state.DatabasePath())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Exec(stmt); err != nil {
		t.Fatalf("%s: %v", stmt, err)
	}
}

func newStateTestApp(t *testing.T) *App {
	t.Helper()
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	t.Setenv("HOME", home)
	app := New()
	app.stdout = &bytes.Buffer{}
	app.stderr = testutil.IODiscard{}
	app.env.Runner = testutil.FakeRunner{}
	if err := app.state.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	return app
}

func TestStateCommandRejectsBadInvocations(t *testing.T) {
	app := newStateTestApp(t)
	stdout := app.stdout.(*bytes.Buffer)

	if code := app.Run(context.Background(), []string{"state"}); code == 0 || !strings.Contains(stdout.String(), "usage: vigilante state export") {
		t.Fatalf("bare state must print usage and fail: code=%d out=%s", code, stdout.String())
	}
	stdout.Reset()
	if code := app.Run(context.Background(), []string{"state", "--help"}); code != 0 || !strings.Contains(stdout.String(), "usage: vigilante state export") {
		t.Fatalf("state --help: code=%d out=%s", code, stdout.String())
	}
	if code := app.Run(context.Background(), []string{"state", "export", "--bogus"}); code == 0 {
		t.Fatal("unknown flag must fail")
	}
	if code := app.Run(context.Background(), []string{"state", "export", "extra"}); code == 0 {
		t.Fatal("positional argument must fail")
	}

	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := app.Run(context.Background(), []string{"state", "export", "--dir", file}); code == 0 {
		t.Fatal("export into a file path must fail")
	}

	execStateSQL(t, app, `DROP TABLE sessions`)
	if code := app.Run(context.Background(), []string{"state", "export"}); code == 0 {
		t.Fatal("export must fail when the database cannot be read")
	}
}

func TestStateCommandFailsWhenStateRootIsAFile(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, ".vigilante")
	if err := os.WriteFile(root, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VIGILANTE_HOME", root)
	t.Setenv("HOME", home)
	app := New()
	app.stdout = &bytes.Buffer{}
	app.stderr = testutil.IODiscard{}
	if code := app.Run(context.Background(), []string{"state", "export"}); code == 0 {
		t.Fatal("export must fail when the state layout cannot be created")
	}
}

func TestCommitSessionHelpersSurfaceStoreErrors(t *testing.T) {
	app := newStateTestApp(t)
	session := state.Session{Repo: "owner/repo", IssueNumber: 1, Provider: "codex", Status: state.SessionStatusRunning}
	if err := app.state.PutSession(session); err != nil {
		t.Fatal(err)
	}
	snap, sessions, err := app.state.LoadSessionSnapshot(false)
	if err != nil {
		t.Fatal(err)
	}

	// A concurrent change is a logged skip for the scan and an error for the CLI.
	if err := state.NewStore().UpdateSession("owner/repo", 1, func(s *state.Session) error { s.Status = state.SessionStatusBlocked; return nil }); err != nil {
		t.Fatal(err)
	}
	sessions[0].IssueTitle = "changed"
	committed, err := app.commitSessions(snap, sessions, "test")
	if err != nil || len(committed) != 1 || committed[0].Status != state.SessionStatusBlocked {
		t.Fatalf("commitSessions must return the database copy on conflict: %#v err=%v", committed, err)
	}
	if err := state.NewStore().UpdateSession("owner/repo", 1, func(s *state.Session) error { s.Status = state.SessionStatusSuccess; return nil }); err != nil {
		t.Fatal(err)
	}
	sessions[0].IssueTitle = "changed again"
	err = app.commitSessionsStrict(snap, sessions)
	var conflict *state.SessionConflictError
	if !errors.As(err, &conflict) || !errors.Is(err, state.ErrSessionConflict) || !strings.Contains(err.Error(), "re-run the command") {
		t.Fatalf("commitSessionsStrict must report the conflict: %v", err)
	}

	execStateSQL(t, app, `DROP TABLE sessions`)
	if _, err := app.commitSessions(nil, sessions, "test"); err == nil {
		t.Fatal("commitSessions without a snapshot must surface the load error")
	}
	if _, err := app.commitSessions(snap, sessions, "test"); err == nil {
		t.Fatal("commitSessions must surface the store error")
	}
	if err := app.commitSessionsStrict(snap, sessions); err == nil {
		t.Fatal("commitSessionsStrict must surface the store error")
	}
}

func TestRecordIssueSessionResultSurvivesStoreFailures(t *testing.T) {
	target := state.WatchTarget{Path: "/repos/a", Repo: "owner/repo", Provider: "codex"}
	result := state.Session{Repo: "owner/repo", IssueNumber: 1, Provider: "codex", Status: state.SessionStatusFailed, StartedAt: "2026-10-26T11:00:00Z"}

	t.Run("load fails", func(t *testing.T) {
		app := newStateTestApp(t)
		execStateSQL(t, app, `DROP TABLE sessions`)
		app.recordIssueSessionResult(context.Background(), target, ghcli.Issue{Number: 1}, result)
	})

	t.Run("update fails", func(t *testing.T) {
		app := newStateTestApp(t)
		previous := state.Session{Repo: "owner/repo", IssueNumber: 1, Provider: "codex", Status: state.SessionStatusRunning, StartedAt: "2026-10-26T10:00:00Z"}
		if err := app.state.PutSession(previous); err != nil {
			t.Fatal(err)
		}
		execStateSQL(t, app, `CREATE TRIGGER block BEFORE UPDATE ON sessions BEGIN SELECT RAISE(ABORT, 'blocked'); END`)
		app.recordIssueSessionResult(context.Background(), target, ghcli.Issue{Number: 1}, result)
		stored, _, _ := app.state.GetSession("owner/repo", 1)
		if stored.Status != state.SessionStatusRunning {
			t.Fatalf("a failed save must leave the stored session alone: %#v", stored)
		}
	})

	t.Run("insert fails", func(t *testing.T) {
		app := newStateTestApp(t)
		execStateSQL(t, app, `CREATE TRIGGER block BEFORE INSERT ON sessions BEGIN SELECT RAISE(ABORT, 'blocked'); END`)
		app.recordIssueSessionResult(context.Background(), target, ghcli.Issue{Number: 1}, result)
		if sessions, _ := app.state.ListSessions(); len(sessions) != 0 {
			t.Fatalf("expected nothing stored, got %#v", sessions)
		}
	})
}

func TestLaunchPullRequestMaintenanceDropsStaleResults(t *testing.T) {
	base := state.Session{
		Repo:                           "owner/repo",
		IssueNumber:                    1,
		Provider:                       "codex",
		Branch:                         "vigilante/issue-1",
		Status:                         state.SessionStatusSuccess,
		PullRequestMaintenanceInFlight: true,
	}

	t.Run("flag already cleared", func(t *testing.T) {
		app := newStateTestApp(t)
		stored := base
		stored.PullRequestMaintenanceInFlight = false
		stored.IssueTitle = "stored"
		if err := app.state.PutSession(stored); err != nil {
			t.Fatal(err)
		}
		app.launchPullRequestMaintenance(context.Background(), base, scanIssueDetailsCacheEntry{})
		app.waitForSessions()
		got, _, _ := app.state.GetSession("owner/repo", 1)
		if got.IssueTitle != "stored" || got.PullRequestMaintenanceInFlight {
			t.Fatalf("stale maintenance result must not be written: %#v", got)
		}
	})

	t.Run("session no longer successful", func(t *testing.T) {
		app := newStateTestApp(t)
		stored := base
		stored.Status = state.SessionStatusBlocked
		if err := app.state.PutSession(stored); err != nil {
			t.Fatal(err)
		}
		app.launchPullRequestMaintenance(context.Background(), base, scanIssueDetailsCacheEntry{})
		app.waitForSessions()
		got, _, _ := app.state.GetSession("owner/repo", 1)
		if got.Status != state.SessionStatusBlocked || got.PullRequestMaintenanceInFlight || got.UpdatedAt == "" {
			t.Fatalf("expected only the in-flight flag to clear: %#v", got)
		}
	})

	t.Run("session deleted", func(t *testing.T) {
		app := newStateTestApp(t)
		app.launchPullRequestMaintenance(context.Background(), base, scanIssueDetailsCacheEntry{})
		app.waitForSessions()
		if sessions, _ := app.state.ListSessions(); len(sessions) != 0 {
			t.Fatalf("expected nothing stored, got %#v", sessions)
		}
	})

	t.Run("save fails", func(t *testing.T) {
		app := newStateTestApp(t)
		if err := app.state.PutSession(base); err != nil {
			t.Fatal(err)
		}
		execStateSQL(t, app, `CREATE TRIGGER block BEFORE UPDATE ON sessions BEGIN SELECT RAISE(ABORT, 'blocked'); END`)
		app.launchPullRequestMaintenance(context.Background(), base, scanIssueDetailsCacheEntry{})
		app.waitForSessions()
		got, _, _ := app.state.GetSession("owner/repo", 1)
		if !got.PullRequestMaintenanceInFlight {
			t.Fatalf("a failed save must leave the stored session alone: %#v", got)
		}
	})
}

func oneOffStartRunner(repoPath string, worktreePath string, branch string) testutil.FakeRunner {
	return testutil.FakeRunner{
		LookPaths: map[string]string{"codex": "/usr/bin/codex"},
		Outputs: mergeStringMaps(
			repoDiscoverOutputs(repoPath, "owner/repo", "main"),
			freshBaseBranchOutputs(repoPath, "main"),
			map[string]string{
				"git worktree prune":            "ok",
				"git worktree list --porcelain": "worktree " + repoPath + "\nHEAD abcdef\nbranch refs/heads/main\n",
				"git worktree add -b " + branch + " " + worktreePath + " origin/main": "ok",
				"gh api repos/owner/repo/issues/10":                                   `{"title":"fix something","body":"fix it","html_url":"https://github.com/owner/repo/issues/10","state":"open","labels":[],"assignees":[]}`,
				"gh auth status":                                                      "Logged in",
				sessionStartCommentCommand("owner/repo", 10, worktreePath, state.Session{Branch: branch}):                                            "ok",
				preflightPromptCommand(worktreePath, "owner/repo", repoPath, 10, "fix something", "https://github.com/owner/repo/issues/10", branch): "baseline ok",
				issuePromptCommand(worktreePath, "owner/repo", repoPath, 10, "fix something", "https://github.com/owner/repo/issues/10", branch):     "done",
			},
		),
		Errors: map[string]error{
			"git show-ref --verify --quiet refs/heads/" + branch:          errors.New("exit status 1"),
			"git show-ref --verify --quiet refs/heads/vigilante/issue-10": errors.New("exit status 1"),
		},
	}
}

func TestStartOneOffSessionRejectsRunningSession(t *testing.T) {
	home := t.TempDir()
	repoPath := filepath.Join(home, "repo")
	worktreePath := filepath.Join(repoPath, ".worktrees", "vigilante", "issue-10")
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))

	app := New()
	app.stdout = &bytes.Buffer{}
	app.stderr = testutil.IODiscard{}
	app.env.Runner = oneOffStartRunner(repoPath, worktreePath, "vigilante/issue-10-fix-something")
	if err := app.state.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	if err := app.state.PutSession(state.Session{Repo: "owner/repo", IssueNumber: 10, Provider: "codex", Status: state.SessionStatusRunning}); err != nil {
		t.Fatal(err)
	}

	err := app.StartOneOffSession(context.Background(), repoPath, 10, "", false)
	if err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("expected already-running error, got %v", err)
	}

	execStateSQL(t, app, `DROP TABLE sessions`)
	if err := app.StartOneOffSession(context.Background(), repoPath, 10, "", false); err == nil {
		t.Fatal("expected the session lookup failure to abort the start")
	}
}

func TestStartOneOffSessionReportsResultSaveFailure(t *testing.T) {
	home := t.TempDir()
	repoPath := filepath.Join(home, "repo")
	worktreePath := filepath.Join(repoPath, ".worktrees", "vigilante", "issue-10")
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))

	app := New()
	app.stdout = &bytes.Buffer{}
	app.stderr = testutil.IODiscard{}
	app.clock = func() time.Time { return time.Date(2026, 4, 6, 12, 0, 0, 0, time.UTC) }
	app.env.Runner = oneOffStartRunner(repoPath, worktreePath, "vigilante/issue-10-fix-something")
	if err := app.state.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	// The initial running row inserts fine; the final result is an upsert on
	// the same key, which the trigger refuses.
	execStateSQL(t, app, `CREATE TRIGGER block BEFORE UPDATE ON sessions BEGIN SELECT RAISE(ABORT, 'blocked'); END`)

	err := app.StartOneOffSession(context.Background(), repoPath, 10, "", false)
	if err == nil || !strings.Contains(err.Error(), "could not be saved") {
		t.Fatalf("expected the save failure to be reported, got %v", err)
	}
}

func TestStatusCommandKeepsRenderingWhenReconciledSessionsCannotBeSaved(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))

	now := time.Date(2026, 3, 26, 19, 0, 0, 0, time.UTC)
	if err := state.NewStore().ReplaceSessions([]state.Session{{
		Repo:            "owner/repo",
		IssueNumber:     99,
		IssueTitle:      "stale",
		IssueURL:        "https://github.com/owner/repo/issues/99",
		Branch:          "vigilante/issue-99",
		Status:          state.SessionStatusRunning,
		StartedAt:       now.Add(-2 * time.Hour).Format(time.RFC3339),
		LastHeartbeatAt: now.Add(-2 * time.Hour).Format(time.RFC3339),
		UpdatedAt:       now.Add(-2 * time.Hour).Format(time.RFC3339),
	}}); err != nil {
		t.Fatal(err)
	}

	app := New()
	app.clock = func() time.Time { return now }
	var stdout bytes.Buffer
	app.stdout = &stdout
	app.stderr = testutil.IODiscard{}
	app.env.OS = "linux"
	app.env.Runner = testutil.FakeRunner{
		Outputs: map[string]string{
			"systemctl --user show --property=LoadState,ActiveState vigilante.service":                            "LoadState=not-found\nActiveState=inactive\n",
			"gh api repos/owner/repo/issues/99":                                                                   `{"title":"stale","body":"body","html_url":"https://github.com/owner/repo/issues/99","state":"closed","labels":[{"name":"vigilante:done"}]}`,
			"gh pr list --repo owner/repo --head vigilante/issue-99 --state all --json number,url,state,mergedAt": `[{"number":123,"url":"https://github.com/owner/repo/pull/123","state":"OPEN","mergedAt":null}]`,
			"gh pr view --repo owner/repo 123 --json number,title,body,url,state,mergedAt,labels,isDraft,mergeable,mergeStateStatus,reviewDecision,statusCheckRollup,baseRefName,headRefOid": `{"number":123,"title":"Test PR","body":"Body","url":"https://github.com/owner/repo/pull/123","state":"OPEN","mergedAt":null,"labels":[],"isDraft":false,"mergeable":"MERGEABLE","mergeStateStatus":"CLEAN","reviewDecision":"APPROVED","statusCheckRollup":[],"baseRefName":"main"}`,
		},
	}
	if err := app.state.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	execStateSQL(t, app, `CREATE TRIGGER block BEFORE UPDATE ON sessions BEGIN SELECT RAISE(ABORT, 'blocked'); END`)

	if code := app.Run(context.Background(), []string{"status"}); code != 0 {
		t.Fatalf("status must still render, got exit %d:\n%s", code, stdout.String())
	}
	if !strings.Contains(stdout.String(), "Completed / failed (1)") {
		t.Fatalf("expected the reconciled view to render from memory, got:\n%s", stdout.String())
	}
	loaded, err := app.state.ListSessions()
	if err != nil {
		t.Fatal(err)
	}
	if loaded[0].Status != state.SessionStatusRunning {
		t.Fatalf("a failed save must leave the stored session untouched: %#v", loaded[0])
	}
}

// hookedRunner runs fn before every command so a test can change the state
// database while the app is in the middle of a scan or resume, exactly where
// a real operator or daemon could interleave. A non-nil error from fn is
// returned as the command's failure.
type hookedRunner struct {
	testutil.FakeRunner
	fn func(name string, args []string) error
}

func (h hookedRunner) Run(ctx context.Context, dir string, name string, args ...string) (string, error) {
	if err := h.fn(name, args); err != nil {
		return "", err
	}
	return h.FakeRunner.Run(ctx, dir, name, args...)
}

func (h hookedRunner) RunStreaming(ctx context.Context, dir string, w interface{ Write([]byte) (int, error) }, name string, args ...string) (string, error) {
	if err := h.fn(name, args); err != nil {
		return "", err
	}
	return h.FakeRunner.RunStreaming(ctx, dir, w, name, args...)
}

func isCommand(name string, args []string, want ...string) bool {
	if name != want[0] || len(args) < len(want)-1 {
		return false
	}
	for i, arg := range want[1:] {
		if args[i] != arg {
			return false
		}
	}
	return true
}

// scanDispatchApp returns an app watching one repository with one open issue
// whose dispatch succeeds unless fn interferes.
func scanDispatchApp(t *testing.T, target state.WatchTarget, fn func(name string, args []string) error) (*App, *bytes.Buffer) {
	t.Helper()
	home := t.TempDir()
	repoPath := filepath.Join(home, "repo")
	worktreePath := filepath.Join(repoPath, ".worktrees", "vigilante", "issue-1")
	branch := "vigilante/issue-1-first"
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))

	var stdout bytes.Buffer
	app := New()
	app.stdout = &stdout
	app.stderr = testutil.IODiscard{}
	app.env.Runner = hookedRunner{
		FakeRunner: testutil.FakeRunner{
			LookPaths: map[string]string{"git": "/usr/bin/git", "gh": "/usr/bin/gh", "codex": "/usr/bin/codex"},
			Outputs: mergeStringMaps(freshBaseBranchOutputs(repoPath, "main"), map[string]string{
				"gh api user --jq .login": "nicobistolfi\n",
				"gh issue list --repo owner/repo --state open --assignee nicobistolfi --json number,title,createdAt,url,labels": `[{"number":1,"title":"first","createdAt":"2026-03-09T12:00:00Z","url":"https://github.com/owner/repo/issues/1","labels":[]}]`,
				"git worktree prune": "ok",
				"git worktree add -b " + branch + " " + worktreePath + " origin/main":                                                      "ok",
				sessionStartCommentCommand("owner/repo", 1, worktreePath, state.Session{Branch: branch}):                                   "ok",
				preflightPromptCommand(worktreePath, "owner/repo", repoPath, 1, "first", "https://github.com/owner/repo/issues/1", branch): "baseline ok",
				issuePromptCommand(worktreePath, "owner/repo", repoPath, 1, "first", "https://github.com/owner/repo/issues/1", branch):     "done",
			}),
		},
		fn: fn,
	}
	if err := app.state.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	target.Path = repoPath
	target.Repo = "owner/repo"
	target.Branch = "main"
	target.Assignee = "me"
	if err := app.state.SaveWatchTargets([]state.WatchTarget{target}); err != nil {
		t.Fatal(err)
	}
	return app, &stdout
}

func TestScanOnceBlocksDispatchWhenBranchOrForkPreparationFails(t *testing.T) {
	t.Run("branch resolution", func(t *testing.T) {
		// A pinned target resolves its branch per issue, so the first remote
		// lookup is the one that blocks the dispatch.
		app, stdout := scanDispatchApp(t, state.WatchTarget{}, func(name string, args []string) error {
			if isCommand(name, args, "git", "ls-remote", "--exit-code", "--heads", "origin", "main") {
				return errors.New("exit status 128: remote hung up")
			}
			return nil
		})
		if err := app.ScanOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
		app.waitForSessions()
		if !strings.Contains(stdout.String(), `repo: owner/repo blocked issue #1: pinned base branch "main" was not found on origin`) {
			t.Fatalf("expected blocked output, got: %s", stdout.String())
		}
		sessions, _ := app.state.ListSessions()
		if len(sessions) != 1 || sessions[0].Status != state.SessionStatusBlocked {
			t.Fatalf("expected a blocked session: %#v", sessions)
		}
	})

	t.Run("fork preparation", func(t *testing.T) {
		app, stdout := scanDispatchApp(t, state.WatchTarget{ForkMode: true}, func(string, []string) error { return nil })
		if err := app.ScanOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
		app.waitForSessions()
		if !strings.Contains(stdout.String(), "repo: owner/repo blocked issue #1:") {
			t.Fatalf("expected blocked output, got: %s", stdout.String())
		}
		sessions, _ := app.state.ListSessions()
		if len(sessions) != 1 || sessions[0].Status != state.SessionStatusBlocked {
			t.Fatalf("expected a blocked session: %#v", sessions)
		}
	})
}

// Each dispatch outcome is committed on its own; when that commit fails the
// scan stops with the store's error instead of carrying on with state it
// could not persist.
func TestScanOnceStopsWhenDispatchCommitFails(t *testing.T) {
	blockInserts := func(app **App) func(name string, args []string) error {
		return func(name string, args []string) error {
			if isCommand(name, args, "git", "worktree", "add") {
				execStateSQL(t, *app, `CREATE TRIGGER block BEFORE INSERT ON sessions BEGIN SELECT RAISE(ABORT, 'blocked'); END`)
			}
			return nil
		}
	}

	t.Run("after a successful dispatch", func(t *testing.T) {
		var app *App
		app, _ = scanDispatchApp(t, state.WatchTarget{}, blockInserts(&app))
		if err := app.ScanOnce(context.Background()); err == nil || !strings.Contains(err.Error(), "blocked") {
			t.Fatalf("expected the commit failure, got %v", err)
		}
		app.waitForSessions()
	})

	t.Run("after a worktree failure", func(t *testing.T) {
		var app *App
		app, _ = scanDispatchApp(t, state.WatchTarget{}, func(name string, args []string) error {
			if err := blockInserts(&app)(name, args); err != nil {
				return err
			}
			if isCommand(name, args, "git", "worktree", "add") {
				return errors.New("exit status 1: worktree add failed")
			}
			return nil
		})
		if err := app.ScanOnce(context.Background()); err == nil || !strings.Contains(err.Error(), "blocked") {
			t.Fatalf("expected the commit failure, got %v", err)
		}
		app.waitForSessions()
	})

	t.Run("after a branch resolution failure", func(t *testing.T) {
		var app *App
		app, _ = scanDispatchApp(t, state.WatchTarget{}, func(name string, args []string) error {
			if isCommand(name, args, "git", "ls-remote", "--exit-code", "--heads", "origin", "main") {
				execStateSQL(t, app, `CREATE TRIGGER IF NOT EXISTS block BEFORE INSERT ON sessions BEGIN SELECT RAISE(ABORT, 'blocked'); END`)
				return errors.New("exit status 128: remote hung up")
			}
			return nil
		})
		if err := app.ScanOnce(context.Background()); err == nil || !strings.Contains(err.Error(), "blocked") {
			t.Fatalf("expected the commit failure, got %v", err)
		}
		app.waitForSessions()
	})
}

// The stored session can move on between the result's first read and its
// write; the write re-checks and drops a result that no longer applies.
func TestRecordIssueSessionResultDropsResultSupersededDuringComment(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	t.Setenv("HOME", home)
	app := New()
	app.stdout = &bytes.Buffer{}
	app.stderr = testutil.IODiscard{}
	interleaved := false
	app.env.Runner = hookedRunner{
		FakeRunner: testutil.FakeRunner{},
		fn: func(name string, args []string) error {
			if name != "gh" || interleaved {
				return nil
			}
			interleaved = true
			return state.NewStore().UpdateSession("owner/repo", 1, func(s *state.Session) error { s.LastCleanupSource = "scan"; return nil })
		},
	}
	if err := app.state.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	previous := state.Session{Repo: "owner/repo", IssueNumber: 1, Provider: "codex", Status: state.SessionStatusRunning, StartedAt: "2026-10-26T10:00:00Z"}
	if err := app.state.PutSession(previous); err != nil {
		t.Fatal(err)
	}
	result := previous
	result.Status = state.SessionStatusFailed
	result.LastError = "provider crashed"

	app.recordIssueSessionResult(context.Background(), state.WatchTarget{Path: "/repos/a", Repo: "owner/repo", Provider: "codex"}, ghcli.Issue{Number: 1}, result)
	if !interleaved {
		t.Fatal("the failure comment never reached the runner")
	}
	stored, _, _ := app.state.GetSession("owner/repo", 1)
	if stored.Status != state.SessionStatusRunning || stored.LastCleanupSource != "scan" {
		t.Fatalf("a result superseded mid-write must not be stored: %#v", stored)
	}
}

var _ environment.Runner = hookedRunner{}

func blockedResumeSession(repoPath string, worktreePath string) state.Session {
	return state.Session{
		RepoPath:        repoPath,
		Repo:            "owner/repo",
		IssueNumber:     1,
		IssueTitle:      "first",
		IssueURL:        "https://github.com/owner/repo/issues/1",
		Branch:          "vigilante/issue-1",
		WorktreePath:    worktreePath,
		Status:          state.SessionStatusBlocked,
		BlockedAt:       "2026-03-11T13:19:12Z",
		BlockedStage:    "issue_execution",
		BlockedReason:   state.BlockedReason{Kind: "provider_auth", Operation: "codex exec", Summary: "session expired", Detail: "session expired"},
		RetryPolicy:     "paused",
		ResumeRequired:  true,
		ResumeHint:      "vigilante resume --repo owner/repo --issue 1",
		UpdatedAt:       "2026-03-11T13:19:12Z",
		LastHeartbeatAt: "2026-03-11T13:19:12Z",
		Provider:        "codex",
	}
}

func TestResumeAllBlocked(t *testing.T) {
	t.Run("nothing blocked", func(t *testing.T) {
		app := newStateTestApp(t)
		if err := app.state.PutSession(state.Session{Repo: "owner/repo", IssueNumber: 1, Provider: "codex", Status: state.SessionStatusRunning}); err != nil {
			t.Fatal(err)
		}
		if code := app.Run(context.Background(), []string{"resume", "--all-blocked"}); code != 0 {
			t.Fatalf("exit code %d", code)
		}
		got, _, _ := app.state.GetSession("owner/repo", 1)
		if got.Status != state.SessionStatusRunning {
			t.Fatalf("unrelated session changed: %#v", got)
		}
	})

	t.Run("sessions cannot be loaded", func(t *testing.T) {
		app := newStateTestApp(t)
		execStateSQL(t, app, `DROP TABLE sessions`)
		if err := app.ResumeAllBlocked(context.Background()); err == nil {
			t.Fatal("expected the load failure to be returned")
		}
	})

	t.Run("resume fails", func(t *testing.T) {
		app := newStateTestApp(t)
		// No codex on PATH: the provider_auth preflight fails before the
		// session runs.
		if err := app.state.PutSession(blockedResumeSession(t.TempDir(), t.TempDir())); err != nil {
			t.Fatal(err)
		}
		if err := app.ResumeAllBlocked(context.Background()); err == nil {
			t.Fatal("expected the preflight failure to be returned")
		}
		got, _, _ := app.state.GetSession("owner/repo", 1)
		if got.Status != state.SessionStatusBlocked {
			t.Fatalf("a failed resume must not be committed as resumed: %#v", got)
		}
	})

	t.Run("concurrent change is reported", func(t *testing.T) {
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
		app.stdout = &bytes.Buffer{}
		app.stderr = testutil.IODiscard{}
		interleaved := false
		app.env.Runner = hookedRunner{
			FakeRunner: testutil.FakeRunner{
				LookPaths: map[string]string{"codex": "/usr/bin/codex"},
				Outputs: map[string]string{
					"codex --version": "codex 1.0.0",
					issuePromptCommand(worktreePath, "owner/repo", repoPath, 1, "first", "https://github.com/owner/repo/issues/1", "vigilante/issue-1"): "done",
					localResumeSuccessCommentCommand("owner/repo", 1, state.Session{Branch: "vigilante/issue-1"}, "issue_execution", "provider_auth"):   "ok",
				},
			},
			fn: func(name string, args []string) error {
				if interleaved || name != "codex" || len(args) == 0 || args[0] == "--version" {
					return nil
				}
				interleaved = true
				if err := state.NewStore().UpdateSession("owner/repo", 1, func(s *state.Session) error { s.ResumeHint = "changed elsewhere"; return nil }); err != nil {
					t.Error(err)
				}
				return nil
			},
		}
		if err := app.state.EnsureLayout(); err != nil {
			t.Fatal(err)
		}
		if err := app.state.ReplaceSessions([]state.Session{blockedResumeSession(repoPath, worktreePath)}); err != nil {
			t.Fatal(err)
		}

		err := app.ResumeAllBlocked(context.Background())
		if !errors.Is(err, state.ErrSessionConflict) {
			t.Fatalf("expected a session conflict, got %v", err)
		}
		if !interleaved {
			t.Fatal("the hook never ran")
		}
		got, _, _ := app.state.GetSession("owner/repo", 1)
		if got.ResumeHint != "changed elsewhere" || got.Status != state.SessionStatusBlocked {
			t.Fatalf("the concurrent write must win: %#v", got)
		}
	})
}
