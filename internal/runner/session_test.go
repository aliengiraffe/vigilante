package runner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nicobistolfi/vigilante/internal/environment"
	ghcli "github.com/nicobistolfi/vigilante/internal/github"
	"github.com/nicobistolfi/vigilante/internal/state"
	"github.com/nicobistolfi/vigilante/internal/testutil"
)

func TestRunIssueSessionSuccess(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	runner := testutil.FakeRunner{
		Outputs: map[string]string{
			"gh issue comment --repo owner/repo 7 --body Vigilante started a Codex session for this issue in `/tmp/worktree` on branch `vigilante/issue-7`.": "ok",
			"codex exec --cd /tmp/worktree --dangerously-bypass-approvals-and-sandbox Use the `vigilante-issue-implementation` skill for this task.\nRepository: owner/repo\nLocal repository path: /tmp/repo\nIssue: #7 - Demo\nIssue URL: https://github.com/owner/repo/issues/7\nWorktree path: /tmp/worktree\nBranch: vigilante/issue-7\nUse `gh issue comment` to comment on the issue when you start working, post a concise implementation plan before substantial coding, add milestone progress comments as you make progress, comment again when the PR is opened, push the branch, open a pull request, and report any execution failure back to the issue.\nUse the issue as the source of truth for the requested behavior and keep the implementation minimal.": "done",
		},
	}
	env := &environment.Environment{OS: "darwin", Runner: runner}
	store := state.NewStore()
	if err := store.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	session := state.Session{RepoPath: "/tmp/repo", IssueNumber: 7, WorktreePath: "/tmp/worktree", Branch: "vigilante/issue-7", Status: state.SessionStatusRunning}
	got := RunIssueSession(context.Background(), env, store, state.WatchTarget{Path: "/tmp/repo", Repo: "owner/repo"}, ghcli.Issue{Number: 7, Title: "Demo", URL: "https://github.com/owner/repo/issues/7"}, session)
	if got.Status != state.SessionStatusSuccess {
		t.Fatalf("unexpected status: %#v", got)
	}
	data, err := os.ReadFile(store.SessionLogPath(7))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "session succeeded") || !strings.Contains(string(data), "done") {
		t.Fatalf("unexpected log: %s", string(data))
	}
}

func TestRunIssueSessionFailureCommentsOnIssue(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	runner := testutil.FakeRunner{
		Outputs: map[string]string{
			"gh issue comment --repo owner/repo 7 --body Vigilante started a Codex session for this issue in `/tmp/worktree` on branch `vigilante/issue-7`.":                                              "ok",
			"gh issue comment --repo owner/repo 7 --body Vigilante Codex session failed for this issue: codex exec [--cd /tmp/worktree --dangerously-bypass-approvals-and-sandbox prompt]: exit status 1": "ok",
		},
		Errors: map[string]error{
			"codex exec --cd /tmp/worktree --dangerously-bypass-approvals-and-sandbox Use the `vigilante-issue-implementation` skill for this task.\nRepository: owner/repo\nLocal repository path: /tmp/repo\nIssue: #7 - Demo\nIssue URL: https://github.com/owner/repo/issues/7\nWorktree path: /tmp/worktree\nBranch: vigilante/issue-7\nUse `gh issue comment` to comment on the issue when you start working, post a concise implementation plan before substantial coding, add milestone progress comments as you make progress, comment again when the PR is opened, push the branch, open a pull request, and report any execution failure back to the issue.\nUse the issue as the source of truth for the requested behavior and keep the implementation minimal.": errors.New("codex exec [--cd /tmp/worktree --dangerously-bypass-approvals-and-sandbox prompt]: exit status 1"),
		},
	}
	env := &environment.Environment{OS: "darwin", Runner: runner}
	store := state.NewStore()
	if err := store.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	session := state.Session{RepoPath: "/tmp/repo", IssueNumber: 7, WorktreePath: "/tmp/worktree", Branch: "vigilante/issue-7", Status: state.SessionStatusRunning}
	got := RunIssueSession(context.Background(), env, store, state.WatchTarget{Path: "/tmp/repo", Repo: "owner/repo"}, ghcli.Issue{Number: 7, Title: "Demo", URL: "https://github.com/owner/repo/issues/7"}, session)
	if got.Status != state.SessionStatusFailed {
		t.Fatalf("unexpected status: %#v", got)
	}
	if !strings.Contains(got.LastError, "exit status 1") {
		t.Fatalf("unexpected error: %#v", got)
	}
	data, err := os.ReadFile(store.SessionLogPath(7))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "session failed") || !strings.Contains(string(data), "exit status 1") {
		t.Fatalf("unexpected log: %s", string(data))
	}
}

func TestRunConflictResolutionSessionFailureCommentsOnIssue(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	runner := testutil.FakeRunner{
		Outputs: map[string]string{
			"gh issue comment --repo owner/repo 7 --body Vigilante conflict-resolution session failed for PR #17 on branch `vigilante/issue-7`: codex exec [--cd /tmp/worktree --dangerously-bypass-approvals-and-sandbox prompt]: exit status 1": "ok",
		},
		Errors: map[string]error{
			"codex exec --cd /tmp/worktree --dangerously-bypass-approvals-and-sandbox Use the `vigilante-conflict-resolution` skill for this task.\nRepository: owner/repo\nLocal repository path: /tmp/repo\nIssue: #7 - Demo\nIssue URL: https://github.com/owner/repo/issues/7\nPull Request: #17\nPull Request URL: https://github.com/owner/repo/pull/17\nWorktree path: /tmp/worktree\nBranch: vigilante/issue-7\nBase branch: origin/main\nResolve the current rebase conflicts in the assigned worktree, use `gh issue comment` for progress and failures, rerun `go test ./...` after conflict resolution if the rebase succeeds, and push the updated branch when finished.\nKeep the changes minimal and focused on getting the PR back to a merge-ready state.": errors.New("codex exec [--cd /tmp/worktree --dangerously-bypass-approvals-and-sandbox prompt]: exit status 1"),
		},
	}
	env := &environment.Environment{OS: "darwin", Runner: runner}
	store := state.NewStore()
	if err := store.EnsureLayout(); err != nil {
		t.Fatal(err)
	}

	err := RunConflictResolutionSession(
		context.Background(),
		env,
		store,
		state.WatchTarget{Path: "/tmp/repo", Repo: "owner/repo"},
		state.Session{RepoPath: "/tmp/repo", IssueNumber: 7, IssueTitle: "Demo", IssueURL: "https://github.com/owner/repo/issues/7", WorktreePath: "/tmp/worktree", Branch: "vigilante/issue-7"},
		ghcli.PullRequest{Number: 17, URL: "https://github.com/owner/repo/pull/17"},
	)
	if err == nil {
		t.Fatal("expected error")
	}
}
