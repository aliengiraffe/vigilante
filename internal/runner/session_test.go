package runner

import (
	"context"
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

func TestRunIssueSessionSuccess(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	runner := testutil.FakeRunner{
		Outputs: map[string]string{
			"gh issue comment --repo owner/repo 7 --body " + ghcli.FormatProgressComment(ghcli.ProgressComment{
				Stage:      "Session Start",
				Emoji:      "🚦",
				Percent:    20,
				ETAMinutes: 25,
				Items: []string{
					"Vigilante launched this implementation session in `/tmp/worktree`.",
					"Branch: `vigilante/issue-7`.",
					"Current stage: handing the issue off to Codex for investigation and implementation.",
				},
				Tagline: "Make it simple, but significant.",
			}): "ok",
			"codex exec --cd /tmp/worktree --dangerously-bypass-approvals-and-sandbox Use the `vigilante-issue-implementation` skill for this task.\nRepository: owner/repo\nLocal repository path: /tmp/repo\nIssue: #7 - Demo\nIssue URL: https://github.com/owner/repo/issues/7\nWorktree path: /tmp/worktree\nBranch: vigilante/issue-7\nUse `gh issue comment` to comment on the issue when you start working, post a concise implementation plan before substantial coding, add milestone progress comments as you make progress, comment again when the PR is opened, push the branch, open a pull request, and report any execution failure back to the issue.\nUse the same GitHub comment structure for every non-terminal milestone comment: a short header with the current stage and optional emoji, a 10-cell progress bar with percentage, an `ETA: ~N minutes` line, 1-3 concise bullets covering what just happened and what is next, and an optional short playful quote or tagline.\nUse the issue as the source of truth for the requested behavior and keep the implementation minimal.": "done",
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
			"gh issue comment --repo owner/repo 7 --body " + ghcli.FormatProgressComment(ghcli.ProgressComment{
				Stage:      "Session Start",
				Emoji:      "🚦",
				Percent:    20,
				ETAMinutes: 25,
				Items: []string{
					"Vigilante launched this implementation session in `/tmp/worktree`.",
					"Branch: `vigilante/issue-7`.",
					"Current stage: handing the issue off to Codex for investigation and implementation.",
				},
				Tagline: "Make it simple, but significant.",
			}): "ok",
			"gh issue comment --repo owner/repo 7 --body " + ghcli.FormatProgressComment(ghcli.ProgressComment{
				Stage:      "Blocked",
				Emoji:      "🛑",
				Percent:    95,
				ETAMinutes: 10,
				Items: []string{
					"Codex execution stopped before the issue implementation completed.",
					"Failure detail: `codex exec [--cd /tmp/worktree --dangerously-bypass-approvals-and-sandbox prompt]: exit status 1`.",
					"Next step: inspect the failing command or environment and redispatch once the blocker is resolved.",
				},
				Tagline: "Plans are only good intentions unless they immediately degenerate into hard work.",
			}): "ok",
		},
		Errors: map[string]error{
			"codex exec --cd /tmp/worktree --dangerously-bypass-approvals-and-sandbox Use the `vigilante-issue-implementation` skill for this task.\nRepository: owner/repo\nLocal repository path: /tmp/repo\nIssue: #7 - Demo\nIssue URL: https://github.com/owner/repo/issues/7\nWorktree path: /tmp/worktree\nBranch: vigilante/issue-7\nUse `gh issue comment` to comment on the issue when you start working, post a concise implementation plan before substantial coding, add milestone progress comments as you make progress, comment again when the PR is opened, push the branch, open a pull request, and report any execution failure back to the issue.\nUse the same GitHub comment structure for every non-terminal milestone comment: a short header with the current stage and optional emoji, a 10-cell progress bar with percentage, an `ETA: ~N minutes` line, 1-3 concise bullets covering what just happened and what is next, and an optional short playful quote or tagline.\nUse the issue as the source of truth for the requested behavior and keep the implementation minimal.": errors.New("codex exec [--cd /tmp/worktree --dangerously-bypass-approvals-and-sandbox prompt]: exit status 1"),
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
			"gh issue comment --repo owner/repo 7 --body " + ghcli.FormatProgressComment(ghcli.ProgressComment{
				Stage:      "Blocked",
				Emoji:      "🧯",
				Percent:    90,
				ETAMinutes: 12,
				Items: []string{
					"Conflict resolution for PR #17 on `vigilante/issue-7` did not complete.",
					"Failure detail: `codex exec [--cd /tmp/worktree --dangerously-bypass-approvals-and-sandbox prompt]: exit status 1`.",
					"Next step: review the rebase state in the worktree and rerun the dedicated conflict-resolution flow.",
				},
				Tagline: "An obstacle is often a stepping stone.",
			}): "ok",
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

func TestAppendSessionLogUsesLocalTimezone(t *testing.T) {
	originalLocal := time.Local
	time.Local = time.FixedZone("TEST", -8*60*60)
	t.Cleanup(func() {
		time.Local = originalLocal
	})

	path := filepath.Join(t.TempDir(), "issue-7.log")
	appendSessionLog(path, "session started", state.Session{
		IssueNumber:  7,
		Branch:       "vigilante/issue-7",
		WorktreePath: "/tmp/worktree",
		Status:       state.SessionStatusRunning,
	}, "")

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "-08:00] session started") {
		t.Fatalf("expected local timezone offset in session log entry, got %q", text)
	}
	if strings.Contains(text, "Z] session started") {
		t.Fatalf("expected local timezone log entry, got %q", text)
	}
}
