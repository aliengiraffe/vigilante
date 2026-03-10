package main

import (
	"context"
	"strings"
	"testing"
)

func TestRunIssueSessionSuccess(t *testing.T) {
	runner := fakeRunner{
		outputs: map[string]string{
			"gh issue comment --repo owner/repo 7 --body Vigilante started a Codex session for this issue in `/tmp/worktree` on branch `vigilante/issue-7`.": "ok",
			"codex exec --cd /tmp/worktree --dangerously-bypass-approvals-and-sandbox Use the `vigilante-issue-implementation` skill for this task.\nRepository: owner/repo\nLocal repository path: /tmp/repo\nIssue: #7 - Demo\nIssue URL: https://github.com/owner/repo/issues/7\nWorktree path: /tmp/worktree\nBranch: vigilante/issue-7\nComment on the issue when you start working, add progress comments as you make meaningful progress, and report any execution failure back to the issue.\nUse the issue as the source of truth for the requested behavior and keep the implementation minimal.": "done",
		},
	}
	env := &Environment{OS: "darwin", Runner: runner}
	session := Session{RepoPath: "/tmp/repo", WorktreePath: "/tmp/worktree", Branch: "vigilante/issue-7", Status: SessionStatusRunning}
	got := RunIssueSession(context.Background(), env, NewStateStore(), WatchTarget{Path: "/tmp/repo", Repo: "owner/repo"}, GitHubIssue{Number: 7, Title: "Demo", URL: "https://github.com/owner/repo/issues/7"}, session)
	if got.Status != SessionStatusSuccess {
		t.Fatalf("unexpected status: %#v", got)
	}
}

func TestRunIssueSessionFailureCommentsOnIssue(t *testing.T) {
	runner := fakeRunner{
		outputs: map[string]string{
			"gh issue comment --repo owner/repo 7 --body Vigilante started a Codex session for this issue in `/tmp/worktree` on branch `vigilante/issue-7`.":                                              "ok",
			"gh issue comment --repo owner/repo 7 --body Vigilante Codex session failed for this issue: codex exec [--cd /tmp/worktree --dangerously-bypass-approvals-and-sandbox prompt]: exit status 1": "ok",
		},
		errors: map[string]error{
			"codex exec --cd /tmp/worktree --dangerously-bypass-approvals-and-sandbox Use the `vigilante-issue-implementation` skill for this task.\nRepository: owner/repo\nLocal repository path: /tmp/repo\nIssue: #7 - Demo\nIssue URL: https://github.com/owner/repo/issues/7\nWorktree path: /tmp/worktree\nBranch: vigilante/issue-7\nComment on the issue when you start working, add progress comments as you make meaningful progress, and report any execution failure back to the issue.\nUse the issue as the source of truth for the requested behavior and keep the implementation minimal.": errString("codex exec [--cd /tmp/worktree --dangerously-bypass-approvals-and-sandbox prompt]: exit status 1"),
		},
	}
	env := &Environment{OS: "darwin", Runner: runner}
	session := Session{RepoPath: "/tmp/repo", WorktreePath: "/tmp/worktree", Branch: "vigilante/issue-7", Status: SessionStatusRunning}
	got := RunIssueSession(context.Background(), env, NewStateStore(), WatchTarget{Path: "/tmp/repo", Repo: "owner/repo"}, GitHubIssue{Number: 7, Title: "Demo", URL: "https://github.com/owner/repo/issues/7"}, session)
	if got.Status != SessionStatusFailed {
		t.Fatalf("unexpected status: %#v", got)
	}
	if !strings.Contains(got.LastError, "exit status 1") {
		t.Fatalf("unexpected error: %#v", got)
	}
}
