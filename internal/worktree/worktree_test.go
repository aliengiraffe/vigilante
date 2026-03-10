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

	worktree, err := CreateIssueWorktree(ctx, runner, state.WatchTarget{Path: repo, Repo: "owner/repo", Branch: "main"}, 9)
	if err != nil {
		t.Fatal(err)
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

func TestCreateIssueWorktreeReusesExistingBranch(t *testing.T) {
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

	worktree, err := CreateIssueWorktree(ctx, runner, state.WatchTarget{Path: repo, Repo: "owner/repo", Branch: "main"}, 9)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(worktree.Path); err != nil {
		t.Fatal(err)
	}
	if err := Remove(ctx, runner, repo, worktree.Path); err != nil {
		t.Fatal(err)
	}
}

func mustRun(t *testing.T, runner environment.Runner, ctx context.Context, dir, name string, args ...string) {
	t.Helper()
	if _, err := runner.Run(ctx, dir, name, args...); err != nil {
		t.Fatal(err)
	}
}
