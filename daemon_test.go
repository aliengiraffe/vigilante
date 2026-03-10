package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestScanOnceSelectsEligibleIssueAndPersistsSession(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))

	app := NewApp()
	app.stdout = &bytes.Buffer{}
	app.stderr = ioDiscard{}
	worktreePath := filepath.Join("/tmp/repo", ".worktrees", "vigilante", "issue-1")
	app.env.Runner = fakeRunner{
		lookPath: map[string]string{"git": "/usr/bin/git", "gh": "/usr/bin/gh", "codex": "/usr/bin/codex"},
		outputs: map[string]string{
			"gh auth status": "ok",
			"gh issue list --repo owner/repo --state open --json number,title,createdAt,url": `[{"number":1,"title":"first","createdAt":"2026-03-09T12:00:00Z","url":"https://github.com/owner/repo/issues/1"}]`,
			"git worktree prune": "ok",
			"git worktree add -b vigilante/issue-1 " + worktreePath + " main":                                                                                       "ok",
			"gh issue comment --repo owner/repo 1 --body Vigilante started a Codex session for this issue in `" + worktreePath + "` on branch `vigilante/issue-1`.": "ok",
			"codex exec --cd " + worktreePath + " --dangerously-bypass-approvals-and-sandbox Use the `vigilante-issue-implementation` skill for this task.\nRepository: owner/repo\nLocal repository path: /tmp/repo\nIssue: #1 - first\nIssue URL: https://github.com/owner/repo/issues/1\nWorktree path: " + worktreePath + "\nBranch: vigilante/issue-1\nComment on the issue when you start working, add progress comments as you make meaningful progress, and report any execution failure back to the issue.\nUse the issue as the source of truth for the requested behavior and keep the implementation minimal.": "done",
		},
	}
	if err := app.state.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	if err := app.state.SaveWatchTargets([]WatchTarget{{Path: "/tmp/repo", Repo: "owner/repo", Branch: "main"}}); err != nil {
		t.Fatal(err)
	}
	if err := app.ScanOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := app.stdout.(*bytes.Buffer).String(); !strings.Contains(got, "repo: owner/repo started issue #1 in "+worktreePath) || !strings.Contains(got, "scanned 1 watch target(s), started 1 issue session(s)") {
		t.Fatalf("unexpected output: %s", got)
	}
	sessions, err := app.state.LoadSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].Status != SessionStatusSuccess {
		t.Fatalf("unexpected sessions: %#v", sessions)
	}
}

func TestScanOncePrintsNoEligibleIssues(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	t.Setenv("HOME", home)

	var stdout bytes.Buffer
	app := NewApp()
	app.stdout = &stdout
	app.stderr = ioDiscard{}
	app.env.Runner = fakeRunner{
		lookPath: map[string]string{"git": "/usr/bin/git", "gh": "/usr/bin/gh", "codex": "/usr/bin/codex"},
		outputs: map[string]string{
			"gh issue list --repo owner/repo --state open --json number,title,createdAt,url": `[{"number":1,"title":"first","createdAt":"2026-03-09T12:00:00Z","url":"https://github.com/owner/repo/issues/1"}]`,
		},
	}
	if err := app.state.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	if err := app.state.SaveWatchTargets([]WatchTarget{{Path: "/tmp/repo", Repo: "owner/repo", Branch: "main"}}); err != nil {
		t.Fatal(err)
	}
	if err := app.state.SaveSessions([]Session{{Repo: "owner/repo", IssueNumber: 1, Status: SessionStatusRunning}}); err != nil {
		t.Fatal(err)
	}
	if err := app.ScanOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := stdout.String(); !strings.Contains(got, "repo: owner/repo no eligible issues (1 open)") || !strings.Contains(got, "scanned 1 watch target(s), started 0 issue session(s)") {
		t.Fatalf("unexpected output: %s", got)
	}
}

func TestScanOnceSkipsWhenAnotherProcessHoldsScanLock(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	t.Setenv("HOME", home)

	var stdout bytes.Buffer
	app := NewApp()
	app.stdout = &stdout
	app.stderr = ioDiscard{}
	if err := app.state.EnsureLayout(); err != nil {
		t.Fatal(err)
	}

	locked, err := app.state.TryWithScanLock(func() error {
		if err := app.ScanOnce(context.Background()); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !locked {
		t.Fatal("expected outer lock to be acquired")
	}
	if got := stdout.String(); !strings.Contains(got, "scan skipped: another vigilante daemon is already scanning") {
		t.Fatalf("unexpected output: %s", got)
	}
}
