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

func TestClassifyTraditionalRepository(t *testing.T) {
	dir := t.TempDir()

	got, err := Classify(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Shape != ShapeTraditional {
		t.Fatalf("unexpected shape: %#v", got)
	}
	if len(got.ProcessHints.WorkspaceFiles) != 0 || len(got.ProcessHints.WorkspaceGlobs) != 0 || len(got.ProcessHints.ProjectRoots) != 0 {
		t.Fatalf("expected no process hints: %#v", got)
	}
}

func TestClassifyMonorepoWithWorkspaceSignals(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"workspaces":["apps/*","packages/*"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		filepath.Join(dir, "apps", "web"),
		filepath.Join(dir, "packages", "ui"),
	} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	got, err := Classify(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Shape != ShapeMonorepo {
		t.Fatalf("unexpected shape: %#v", got)
	}
	for _, want := range []string{"package.json#workspaces"} {
		if !contains(got.ProcessHints.WorkspaceFiles, want) {
			t.Fatalf("expected workspace file %q in %#v", want, got.ProcessHints)
		}
	}
	for _, want := range []string{"apps/*", "packages/*"} {
		if !contains(got.ProcessHints.WorkspaceGlobs, want) {
			t.Fatalf("expected workspace glob %q in %#v", want, got.ProcessHints)
		}
	}
	for _, want := range []string{"apps/web", "packages/ui"} {
		if !contains(got.ProcessHints.ProjectRoots, want) {
			t.Fatalf("expected project root %q in %#v", want, got.ProcessHints)
		}
	}
}

func TestClassifyAmbiguousRepositoryFallsBackToTraditional(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "packages", "single"), 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := Classify(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Shape != ShapeTraditional {
		t.Fatalf("unexpected shape: %#v", got)
	}
	if !contains(got.ProcessHints.ProjectRoots, "packages/single") {
		t.Fatalf("expected project root hint: %#v", got.ProcessHints)
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
