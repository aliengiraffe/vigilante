package main

import (
	"context"
	"path/filepath"
	"testing"
)

func TestParseGitHubRepo(t *testing.T) {
	tests := map[string]string{
		"git@github.com:owner/repo.git":   "owner/repo",
		"https://github.com/owner/repo":   "owner/repo",
		"ssh://git@github.com/owner/repo": "owner/repo",
	}
	for input, want := range tests {
		got, err := ParseGitHubRepo(input)
		if err != nil {
			t.Fatalf("%s: %v", input, err)
		}
		if got != want {
			t.Fatalf("%s: got %s want %s", input, got, want)
		}
	}
}

func TestDiscoverRepositoryWithRealGit(t *testing.T) {
	dir := t.TempDir()
	runner := ExecRunner{}
	ctx := context.Background()

	mustRun(t, runner, ctx, dir, "git", "init", "--initial-branch=main")
	mustRun(t, runner, ctx, dir, "git", "remote", "add", "origin", "git@github.com:owner/repo.git")
	info, err := DiscoverRepository(ctx, runner, dir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Repo != "owner/repo" {
		t.Fatalf("unexpected repo: %#v", info)
	}
	if info.Branch != "main" {
		t.Fatalf("unexpected branch: %#v", info)
	}
}

func TestExpandPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	got, err := ExpandPath("~/demo")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, "demo")
	if got != want {
		t.Fatalf("got %s want %s", got, want)
	}
}

func mustRun(t *testing.T, runner Runner, ctx context.Context, dir, name string, args ...string) {
	t.Helper()
	if _, err := runner.Run(ctx, dir, name, args...); err != nil {
		t.Fatal(err)
	}
}
