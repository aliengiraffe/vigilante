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
	if err := os.WriteFile(filepath.Join(dir, "turbo.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := Classify(dir)

	if got.Shape != ShapeMonorepo {
		t.Fatalf("expected monorepo classification, got %#v", got)
	}
	if len(got.ProcessHints.WorkspaceConfigFiles) != 2 {
		t.Fatalf("expected workspace config hints, got %#v", got.ProcessHints)
	}
	if got.MonorepoStack != MonorepoStackTurborepo {
		t.Fatalf("expected turborepo stack, got %#v", got)
	}
}

func TestClassifyPreservesTurborepoMarkers(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "pnpm-workspace.yaml"), []byte("packages:\n  - apps/*\n  - packages/*\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "turbo.json"), []byte("{\"pipeline\":{}}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte("{\"workspaces\":[\"apps/*\",\"packages/*\"]}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "apps", "web"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "packages", "ui"), 0o755); err != nil {
		t.Fatal(err)
	}

	got := Classify(dir)

	if got.Shape != ShapeMonorepo {
		t.Fatalf("expected monorepo classification, got %#v", got)
	}
	if len(got.ProcessHints.WorkspaceConfigFiles) != 2 {
		t.Fatalf("expected turborepo config hints, got %#v", got.ProcessHints)
	}
	if got.ProcessHints.WorkspaceConfigFiles[0] != "pnpm-workspace.yaml" || got.ProcessHints.WorkspaceConfigFiles[1] != "turbo.json" {
		t.Fatalf("expected sorted turborepo config hints, got %#v", got.ProcessHints.WorkspaceConfigFiles)
	}
	if len(got.ProcessHints.WorkspaceManifestFiles) != 1 || got.ProcessHints.WorkspaceManifestFiles[0] != "package.json" {
		t.Fatalf("expected package.json workspace manifest hint, got %#v", got.ProcessHints.WorkspaceManifestFiles)
	}
	if len(got.ProcessHints.MultiPackageRoots) != 2 || got.ProcessHints.MultiPackageRoots[0] != "apps" || got.ProcessHints.MultiPackageRoots[1] != "packages" {
		t.Fatalf("expected apps/packages roots, got %#v", got.ProcessHints.MultiPackageRoots)
	}
}

func TestClassifyGradleMultiProjectFromSettingsInclude(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "settings.gradle.kts"), []byte("rootProject.name = \"demo\"\ninclude(\":app\", \":shared\")\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "build.gradle.kts"), []byte("plugins {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := Classify(dir)

	if got.Shape != ShapeGradleMultiProject {
		t.Fatalf("expected gradle multi-project classification, got %#v", got)
	}
	if len(got.ProcessHints.GradleSettingsFiles) != 1 || got.ProcessHints.GradleSettingsFiles[0] != "settings.gradle.kts" {
		t.Fatalf("expected gradle settings hint, got %#v", got.ProcessHints)
	}
	if len(got.ProcessHints.GradleRootBuildFiles) != 1 || got.ProcessHints.GradleRootBuildFiles[0] != "build.gradle.kts" {
		t.Fatalf("expected gradle root build hint, got %#v", got.ProcessHints)
	}
}

func TestClassifyNxRepoFromNxConfig(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "nx.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pnpm-workspace.yaml"), []byte("packages:\n  - apps/*\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := Classify(dir)

	if got.Shape != ShapeMonorepo {
		t.Fatalf("expected nx classification, got %#v", got)
	}
	if len(got.ProcessHints.WorkspaceConfigFiles) != 2 {
		t.Fatalf("expected nx and workspace config hints, got %#v", got.ProcessHints)
	}
	if got.MonorepoStack != MonorepoStackNx {
		t.Fatalf("expected nx monorepo stack, got %#v", got)
	}
}

func TestClassifyRushRepoPreservesRushMarker(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "rush.json"), []byte("{\"rushVersion\":\"5.0.0\"}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := Classify(dir)

	if got.Shape != ShapeMonorepo {
		t.Fatalf("expected monorepo classification, got %#v", got)
	}
	if len(got.ProcessHints.WorkspaceConfigFiles) != 1 || got.ProcessHints.WorkspaceConfigFiles[0] != "rush.json" {
		t.Fatalf("expected rush workspace config hint, got %#v", got.ProcessHints)
	}
	if got.MonorepoStack != MonorepoStackRush {
		t.Fatalf("expected rush monorepo stack, got %#v", got)
	}
}

func TestClassifyMonorepoFromBazelSignals(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "MODULE.bazel"), []byte("module(name = \"demo\")\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "services", "api"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "services", "api", "BUILD.bazel"), []byte("go_test(name = \"api_test\")\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := Classify(dir)

	if got.Shape != ShapeMonorepo {
		t.Fatalf("expected bazel monorepo classification, got %#v", got)
	}
	if len(got.ProcessHints.BazelRepoMarkers) != 1 || got.ProcessHints.BazelRepoMarkers[0] != "MODULE.bazel" {
		t.Fatalf("expected bazel repo marker hint, got %#v", got.ProcessHints)
	}
	if len(got.ProcessHints.BazelPackageRoots) != 1 || got.ProcessHints.BazelPackageRoots[0] != "services" {
		t.Fatalf("expected bazel package root hint, got %#v", got.ProcessHints)
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
	if got.MonorepoStack != "" {
		t.Fatalf("expected no monorepo stack for traditional repo fallback, got %#v", got)
	}
}

func TestClassifyMonorepoUsesUnknownStackFallback(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "pnpm-workspace.yaml"), []byte("packages:\n  - apps/*\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := Classify(dir)

	if got.Shape != ShapeMonorepo {
		t.Fatalf("expected monorepo classification, got %#v", got)
	}
	if got.MonorepoStack != MonorepoStackUnknown {
		t.Fatalf("expected unknown monorepo stack fallback, got %#v", got)
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
