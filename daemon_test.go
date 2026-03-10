package main

import (
	"bytes"
	"context"
	"path/filepath"
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
	app.env.Runner = fakeRunner{
		lookPath: map[string]string{"git": "/usr/bin/git", "gh": "/usr/bin/gh", "codex": "/usr/bin/codex"},
		outputs: map[string]string{
			"gh auth status": "ok",
			"gh issue list --repo owner/repo --state open --json number,title,createdAt,url":                                                                                                                            `[{"number":1,"title":"first","createdAt":"2026-03-09T12:00:00Z","url":"https://github.com/owner/repo/issues/1"}]`,
			"git worktree add -b vigilante/issue-1 " + filepath.Join(app.state.WorktreesDir(), "owner_repo", "issue-1") + " main":                                                                                       "ok",
			"gh issue comment --repo owner/repo 1 --body Vigilante started a Codex session for this issue in `" + filepath.Join(app.state.WorktreesDir(), "owner_repo", "issue-1") + "` on branch `vigilante/issue-1`.": "ok",
			"codex exec --cd " + filepath.Join(app.state.WorktreesDir(), "owner_repo", "issue-1") + " --dangerously-bypass-approvals-and-sandbox Use the `vigilante-issue-implementation` skill for this task.\nRepository: owner/repo\nLocal repository path: /tmp/repo\nIssue: #1 - first\nIssue URL: https://github.com/owner/repo/issues/1\nWorktree path: " + filepath.Join(app.state.WorktreesDir(), "owner_repo", "issue-1") + "\nBranch: vigilante/issue-1\nComment on the issue when you start working, add progress comments as you make meaningful progress, and report any execution failure back to the issue.\nUse the issue as the source of truth for the requested behavior and keep the implementation minimal.": "done",
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
	sessions, err := app.state.LoadSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].Status != SessionStatusSuccess {
		t.Fatalf("unexpected sessions: %#v", sessions)
	}
}
