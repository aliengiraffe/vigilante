package repo

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/nicobistolfi/vigilante/internal/environment"
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
	runner := environment.ExecRunner{}
	ctx := context.Background()

	mustRun(t, runner, ctx, dir, "git", "init", "--initial-branch=main")
	mustRun(t, runner, ctx, dir, "git", "remote", "add", "origin", "git@github.com:owner/repo.git")
	info, err := Discover(ctx, runner, dir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Repo != "owner/repo" {
		t.Fatalf("unexpected repo: %#v", info)
	}
	if info.Branch != "main" {
		t.Fatalf("unexpected branch: %#v", info)
	}
	if info.Classification.Shape != ShapeTraditional {
		t.Fatalf("unexpected classification: %#v", info.Classification)
	}
}

func TestClassifyTraditionalRepo(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/demo\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := Classify(dir)

	if got.Shape != ShapeTraditional {
		t.Fatalf("expected traditional classification, got %#v", got)
	}
}

func TestClassifyMonorepoFromWorkspaceSignals(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "pnpm-workspace.yaml"), []byte("packages:\n  - apps/*\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := Classify(dir)

	if got.Shape != ShapeMonorepo {
		t.Fatalf("expected monorepo classification, got %#v", got)
	}
	if len(got.ProcessHints.WorkspaceConfigFiles) != 1 || got.ProcessHints.WorkspaceConfigFiles[0] != "pnpm-workspace.yaml" {
		t.Fatalf("expected workspace config hint, got %#v", got.ProcessHints)
	}
}

func TestClassifyFallsBackSafelyForAmbiguousRepo(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "apps", "web"), 0o755); err != nil {
		t.Fatal(err)
	}

	got := Classify(dir)

	if got.Shape != ShapeTraditional {
		t.Fatalf("expected safe fallback to traditional, got %#v", got)
	}
	if len(got.ProcessHints.MultiPackageRoots) != 1 || got.ProcessHints.MultiPackageRoots[0] != "apps" {
		t.Fatalf("expected ambiguous multi-package hint to be preserved, got %#v", got.ProcessHints)
	}
}

func TestExpandPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	got, err := filepath.Abs(filepath.Join(home, "demo"))
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.Abs(filepath.Join(home, "demo"))
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("got %s want %s", got, want)
	}
}

func mustRun(t *testing.T, runner environment.Runner, ctx context.Context, dir, name string, args ...string) {
	t.Helper()
	if _, err := runner.Run(ctx, dir, name, args...); err != nil {
		t.Fatal(err)
	}
}
