package worktree

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/nicobistolfi/vigilante/internal/environment"
	"github.com/nicobistolfi/vigilante/internal/state"
)

func TestCreateAndRemoveWorktree(t *testing.T) {
	home := t.TempDir()
	repo := filepath.Join(home, "repo")
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}

	runner := environment.ExecRunner{}
	ctx := context.Background()
	mustRun(t, runner, ctx, repo, "git", "init", "--initial-branch=main")
	mustRun(t, runner, ctx, repo, "git", "config", "user.email", "test@example.com")
	mustRun(t, runner, ctx, repo, "git", "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, runner, ctx, repo, "git", "add", "README.md")
	mustRun(t, runner, ctx, repo, "git", "commit", "-m", "init")

	worktree, err := CreateIssueWorktree(ctx, runner, state.WatchTarget{Path: repo, Repo: "owner/repo", Branch: "main"}, 9, "Add daemon status command")
	if err != nil {
		t.Fatal(err)
	}
	if want := "vigilante/issue-9-add-daemon-status-command"; worktree.Branch != want {
		t.Fatalf("unexpected branch: got %s want %s", worktree.Branch, want)
	}
	if want := filepath.Join(repo, ".worktrees", "vigilante", "issue-9"); worktree.Path != want {
		t.Fatalf("unexpected worktree path: got %s want %s", worktree.Path, want)
	}
	if _, err := os.Stat(worktree.Path); err != nil {
		t.Fatal(err)
	}
	if err := Remove(ctx, runner, repo, worktree.Path); err != nil {
		t.Fatal(err)
	}
}

func TestCreateIssueWorktreeReusesExistingLegacyBranch(t *testing.T) {
	home := t.TempDir()
	repo := filepath.Join(home, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}

	runner := environment.ExecRunner{}
	ctx := context.Background()
	mustRun(t, runner, ctx, repo, "git", "init", "--initial-branch=main")
	mustRun(t, runner, ctx, repo, "git", "config", "user.email", "test@example.com")
	mustRun(t, runner, ctx, repo, "git", "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, runner, ctx, repo, "git", "add", "README.md")
	mustRun(t, runner, ctx, repo, "git", "commit", "-m", "init")
	mustRun(t, runner, ctx, repo, "git", "branch", "vigilante/issue-9")

	worktree, err := CreateIssueWorktree(ctx, runner, state.WatchTarget{Path: repo, Repo: "owner/repo", Branch: "main"}, 9, "Add daemon status command")
	if err != nil {
		t.Fatal(err)
	}
	if want := "vigilante/issue-9"; worktree.Branch != want {
		t.Fatalf("unexpected branch: got %s want %s", worktree.Branch, want)
	}
	if _, err := os.Stat(worktree.Path); err != nil {
		t.Fatal(err)
	}
	if err := Remove(ctx, runner, repo, worktree.Path); err != nil {
		t.Fatal(err)
	}
}

func TestCreateIssueWorktreeReusesExistingRemoteBranch(t *testing.T) {
	home := t.TempDir()
	repo := filepath.Join(home, "repo")
	remote := filepath.Join(home, "remote.git")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(remote, 0o755); err != nil {
		t.Fatal(err)
	}

	runner := environment.ExecRunner{}
	ctx := context.Background()
	mustRun(t, runner, ctx, remote, "git", "init", "--bare")
	mustRun(t, runner, ctx, repo, "git", "init", "--initial-branch=main")
	mustRun(t, runner, ctx, repo, "git", "config", "user.email", "test@example.com")
	mustRun(t, runner, ctx, repo, "git", "config", "user.name", "Test User")
	mustRun(t, runner, ctx, repo, "git", "remote", "add", "origin", remote)
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, runner, ctx, repo, "git", "add", "README.md")
	mustRun(t, runner, ctx, repo, "git", "commit", "-m", "init")
	mustRun(t, runner, ctx, repo, "git", "push", "-u", "origin", "main")
	mustRun(t, runner, ctx, repo, "git", "checkout", "-b", "vigilante/issue-9-add-daemon-status-command")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\nremote change\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, runner, ctx, repo, "git", "commit", "-am", "remote work")
	mustRun(t, runner, ctx, repo, "git", "push", "-u", "origin", "vigilante/issue-9-add-daemon-status-command")
	mustRun(t, runner, ctx, repo, "git", "checkout", "main")
	mustRun(t, runner, ctx, repo, "git", "branch", "-D", "vigilante/issue-9-add-daemon-status-command")

	worktree, err := CreateIssueWorktree(ctx, runner, state.WatchTarget{Path: repo, Repo: "owner/repo", Branch: "main"}, 9, "Add daemon status command")
	if err != nil {
		t.Fatal(err)
	}
	if want := "vigilante/issue-9-add-daemon-status-command"; worktree.Branch != want {
		t.Fatalf("unexpected branch: got %s want %s", worktree.Branch, want)
	}
	if worktree.ReusedRemoteBranch != worktree.Branch {
		t.Fatalf("expected reused remote branch to be recorded: %#v", worktree)
	}
	data, err := os.ReadFile(filepath.Join(worktree.Path, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello\nremote change\n" {
		t.Fatalf("expected worktree to include remote branch content, got %q", string(data))
	}
}

func TestCreateIssueWorktreePrefersPrimaryRemoteBranchOverLegacyFallback(t *testing.T) {
	home := t.TempDir()
	repo := filepath.Join(home, "repo")
	remote := filepath.Join(home, "remote.git")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(remote, 0o755); err != nil {
		t.Fatal(err)
	}

	runner := environment.ExecRunner{}
	ctx := context.Background()
	mustRun(t, runner, ctx, remote, "git", "init", "--bare")
	mustRun(t, runner, ctx, repo, "git", "init", "--initial-branch=main")
	mustRun(t, runner, ctx, repo, "git", "config", "user.email", "test@example.com")
	mustRun(t, runner, ctx, repo, "git", "config", "user.name", "Test User")
	mustRun(t, runner, ctx, repo, "git", "remote", "add", "origin", remote)
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, runner, ctx, repo, "git", "add", "README.md")
	mustRun(t, runner, ctx, repo, "git", "commit", "-m", "init")
	mustRun(t, runner, ctx, repo, "git", "push", "-u", "origin", "main")

	mustRun(t, runner, ctx, repo, "git", "checkout", "-b", "vigilante/issue-9")
	if err := os.WriteFile(filepath.Join(repo, "legacy.txt"), []byte("legacy\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, runner, ctx, repo, "git", "add", "legacy.txt")
	mustRun(t, runner, ctx, repo, "git", "commit", "-m", "legacy work")
	mustRun(t, runner, ctx, repo, "git", "push", "-u", "origin", "vigilante/issue-9")

	mustRun(t, runner, ctx, repo, "git", "checkout", "main")
	mustRun(t, runner, ctx, repo, "git", "checkout", "-b", "vigilante/issue-9-add-daemon-status-command")
	if err := os.WriteFile(filepath.Join(repo, "primary.txt"), []byte("primary\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, runner, ctx, repo, "git", "add", "primary.txt")
	mustRun(t, runner, ctx, repo, "git", "commit", "-m", "primary work")
	mustRun(t, runner, ctx, repo, "git", "push", "-u", "origin", "vigilante/issue-9-add-daemon-status-command")
	mustRun(t, runner, ctx, repo, "git", "checkout", "main")
	mustRun(t, runner, ctx, repo, "git", "branch", "-D", "vigilante/issue-9")
	mustRun(t, runner, ctx, repo, "git", "branch", "-D", "vigilante/issue-9-add-daemon-status-command")

	worktree, err := CreateIssueWorktree(ctx, runner, state.WatchTarget{Path: repo, Repo: "owner/repo", Branch: "main"}, 9, "Add daemon status command")
	if err != nil {
		t.Fatal(err)
	}
	if want := "vigilante/issue-9-add-daemon-status-command"; worktree.Branch != want {
		t.Fatalf("unexpected branch: got %s want %s", worktree.Branch, want)
	}
	if _, err := os.Stat(filepath.Join(worktree.Path, "primary.txt")); err != nil {
		t.Fatalf("expected primary remote branch contents: %v", err)
	}
	if _, err := os.Stat(filepath.Join(worktree.Path, "legacy.txt")); err == nil {
		t.Fatalf("did not expect legacy-only file in preferred primary branch worktree")
	}
}

func TestIssueTitleSlug(t *testing.T) {
	tests := []struct {
		title string
		want  string
	}{
		{title: "Add daemon status command", want: "add-daemon-status-command"},
		{title: "Fix: spaces, punctuation, & CASE", want: "fix-spaces-punctuation-case"},
		{title: "   ---   ", want: ""},
	}

	for _, tt := range tests {
		if got := IssueTitleSlug(tt.title); got != tt.want {
			t.Fatalf("IssueTitleSlug(%q) = %q, want %q", tt.title, got, tt.want)
		}
	}
}

func TestIssueBranchCandidates(t *testing.T) {
	got := IssueBranchCandidates(21, "Add daemon status command")
	want := []string{"vigilante/issue-21-add-daemon-status-command", "vigilante/issue-21"}
	if len(got) != len(want) {
		t.Fatalf("unexpected candidates: %#v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("unexpected candidates: %#v", got)
		}
	}
}

func mustRun(t *testing.T, runner environment.Runner, ctx context.Context, dir, name string, args ...string) {
	t.Helper()
	if _, err := runner.Run(ctx, dir, name, args...); err != nil {
		t.Fatal(err)
	}
}
