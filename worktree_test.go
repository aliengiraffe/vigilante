package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestCreateAndRemoveWorktree(t *testing.T) {
	home := t.TempDir()
	repo := filepath.Join(home, "repo")
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}

	runner := ExecRunner{}
	ctx := context.Background()
	mustRun(t, runner, ctx, repo, "git", "init", "--initial-branch=main")
	mustRun(t, runner, ctx, repo, "git", "config", "user.email", "test@example.com")
	mustRun(t, runner, ctx, repo, "git", "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, runner, ctx, repo, "git", "add", "README.md")
	mustRun(t, runner, ctx, repo, "git", "commit", "-m", "init")

	worktree, err := CreateIssueWorktree(ctx, runner, WatchTarget{Path: repo, Repo: "owner/repo", Branch: "main"}, 9)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(repo, ".worktrees", "vigilante", "issue-9"); worktree.Path != want {
		t.Fatalf("unexpected worktree path: got %s want %s", worktree.Path, want)
	}
	if _, err := os.Stat(worktree.Path); err != nil {
		t.Fatal(err)
	}
	if err := RemoveWorktree(ctx, runner, repo, worktree.Path); err != nil {
		t.Fatal(err)
	}
}
