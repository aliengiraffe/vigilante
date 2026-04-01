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

func TestRewriteGitHubRemote(t *testing.T) {
	tests := map[string]string{
		"git@github.com:owner/repo.git":   "git@github.com:forker/repo.git",
		"https://github.com/owner/repo":   "https://github.com/forker/repo.git",
		"ssh://git@github.com/owner/repo": "ssh://git@github.com/forker/repo.git",
	}
	for input, want := range tests {
		got, err := RewriteGitHubRemote(input, "forker/repo")
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

func TestClassifyDetectsJVMFromMavenSignal(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "pom.xml"), []byte("<project/>\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := Classify(dir)

	if got.Shape != ShapeTraditional {
		t.Fatalf("expected traditional classification, got %#v", got)
	}
	if len(got.TechStacks) != 1 || got.TechStacks[0] != TechStackJVM {
		t.Fatalf("expected JVM tech stack, got %#v", got.TechStacks)
	}
}

func TestClassifyDetectsJVMFromKotlinSourceLayout(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "src", "main", "kotlin", "demo"), 0o755); err != nil {
		t.Fatal(err)
	}

	got := Classify(dir)

	if got.Shape != ShapeTraditional {
		t.Fatalf("expected traditional classification, got %#v", got)
	}
	if len(got.TechStacks) != 1 || got.TechStacks[0] != TechStackJVM {
		t.Fatalf("expected JVM tech stack, got %#v", got.TechStacks)
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

func TestClassifyNodeJSRepoFromPackageJSON(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte("{\"name\":\"demo\"}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "package-lock.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := Classify(dir)

	if len(got.TechStacks) != 1 || got.TechStacks[0] != TechStackNodeJS {
		t.Fatalf("expected nodejs tech stack, got %#v", got.TechStacks)
	}
	if len(got.ProcessHints.NodePackageManagers) != 1 || got.ProcessHints.NodePackageManagers[0] != "npm" {
		t.Fatalf("expected npm package manager, got %#v", got.ProcessHints.NodePackageManagers)
	}
	if len(got.ProcessHints.NodeLockFiles) != 1 || got.ProcessHints.NodeLockFiles[0] != "package-lock.json" {
		t.Fatalf("expected package-lock.json lock file, got %#v", got.ProcessHints.NodeLockFiles)
	}
}

func TestClassifyNodeJSRepoWithPnpm(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte("{\"name\":\"demo\"}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pnpm-lock.yaml"), []byte("lockfileVersion: 5\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := Classify(dir)

	if len(got.TechStacks) != 1 || got.TechStacks[0] != TechStackNodeJS {
		t.Fatalf("expected nodejs tech stack, got %#v", got.TechStacks)
	}
	if len(got.ProcessHints.NodePackageManagers) != 1 || got.ProcessHints.NodePackageManagers[0] != "pnpm" {
		t.Fatalf("expected pnpm package manager, got %#v", got.ProcessHints.NodePackageManagers)
	}
	if len(got.ProcessHints.NodeLockFiles) != 1 || got.ProcessHints.NodeLockFiles[0] != "pnpm-lock.yaml" {
		t.Fatalf("expected pnpm-lock.yaml lock file, got %#v", got.ProcessHints.NodeLockFiles)
	}
}

func TestClassifyNodeJSRepoWithYarn(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte("{\"name\":\"demo\"}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "yarn.lock"), []byte("# yarn lockfile\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := Classify(dir)

	if len(got.TechStacks) != 1 || got.TechStacks[0] != TechStackNodeJS {
		t.Fatalf("expected nodejs tech stack, got %#v", got.TechStacks)
	}
	if len(got.ProcessHints.NodePackageManagers) != 1 || got.ProcessHints.NodePackageManagers[0] != "yarn" {
		t.Fatalf("expected yarn package manager, got %#v", got.ProcessHints.NodePackageManagers)
	}
}

func TestClassifyNodeJSRepoWithTypeScript(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte("{\"name\":\"demo\"}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tsconfig.json"), []byte("{\"compilerOptions\":{}}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := Classify(dir)

	if len(got.TechStacks) != 1 || got.TechStacks[0] != TechStackNodeJS {
		t.Fatalf("expected nodejs tech stack, got %#v", got.TechStacks)
	}
	if len(got.ProcessHints.TypeScriptConfigs) != 1 || got.ProcessHints.TypeScriptConfigs[0] != "tsconfig.json" {
		t.Fatalf("expected tsconfig.json, got %#v", got.ProcessHints.TypeScriptConfigs)
	}
}

func TestClassifyNodeJSRepoDefaultsToNpmWhenNoLockFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte("{\"name\":\"demo\"}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := Classify(dir)

	if len(got.TechStacks) != 1 || got.TechStacks[0] != TechStackNodeJS {
		t.Fatalf("expected nodejs tech stack, got %#v", got.TechStacks)
	}
	if len(got.ProcessHints.NodePackageManagers) != 1 || got.ProcessHints.NodePackageManagers[0] != "npm" {
		t.Fatalf("expected npm default package manager, got %#v", got.ProcessHints.NodePackageManagers)
	}
	if len(got.ProcessHints.NodeLockFiles) != 0 {
		t.Fatalf("expected no lock files, got %#v", got.ProcessHints.NodeLockFiles)
	}
}

func TestClassifyNonNodeJSRepoHasNoNodeJSTechStack(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/demo\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := Classify(dir)

	for _, stack := range got.TechStacks {
		if stack == TechStackNodeJS {
			t.Fatalf("expected no nodejs tech stack for Go-only repo, got %#v", got.TechStacks)
		}
	}
	if len(got.ProcessHints.NodePackageManagers) != 0 {
		t.Fatalf("expected no node package managers, got %#v", got.ProcessHints.NodePackageManagers)
	}
}

func TestClassifyTurborepoMonorepoAlsoDetectsNodeJS(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte("{\"workspaces\":[\"apps/*\"]}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pnpm-workspace.yaml"), []byte("packages:\n  - apps/*\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "turbo.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pnpm-lock.yaml"), []byte("lockfileVersion: 5\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tsconfig.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := Classify(dir)

	if got.Shape != ShapeMonorepo {
		t.Fatalf("expected monorepo, got %#v", got.Shape)
	}
	if len(got.TechStacks) != 1 || got.TechStacks[0] != TechStackNodeJS {
		t.Fatalf("expected nodejs tech stack on turborepo, got %#v", got.TechStacks)
	}
	if len(got.ProcessHints.NodePackageManagers) != 1 || got.ProcessHints.NodePackageManagers[0] != "pnpm" {
		t.Fatalf("expected pnpm, got %#v", got.ProcessHints.NodePackageManagers)
	}
	if len(got.ProcessHints.TypeScriptConfigs) != 1 {
		t.Fatalf("expected tsconfig, got %#v", got.ProcessHints.TypeScriptConfigs)
	}
}

func TestClassifyGoRepoFromGoMod(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/demo\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := Classify(dir)

	if got.Shape != ShapeTraditional {
		t.Fatalf("expected traditional shape, got %#v", got.Shape)
	}
	if len(got.TechStacks) != 1 || got.TechStacks[0] != TechStackGo {
		t.Fatalf("expected go tech stack, got %#v", got.TechStacks)
	}
}

func TestClassifyNonGoRepoHasNoGoTechStack(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.py"), []byte("print('hello')\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := Classify(dir)

	for _, stack := range got.TechStacks {
		if stack == TechStackGo {
			t.Fatalf("unexpected go tech stack for non-Go repo")
		}
	}
}

func TestClassifyPythonRepoFromPyproject(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "pyproject.toml"), []byte("[project]\nname = \"demo\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := Classify(dir)

	if len(got.TechStacks) != 1 || got.TechStacks[0] != TechStackPython {
		t.Fatalf("expected python tech stack, got %#v", got.TechStacks)
	}
	if len(got.ProcessHints.PythonSignals) != 1 || got.ProcessHints.PythonSignals[0] != "pyproject.toml" {
		t.Fatalf("expected pyproject signal, got %#v", got.ProcessHints.PythonSignals)
	}
}

func TestClassifyPythonRepoFromRequirementsAndPackageLayout(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "requirements.txt"), []byte("pytest\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "src", "demo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "src", "demo", "__init__.py"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}

	got := Classify(dir)

	if len(got.TechStacks) != 1 || got.TechStacks[0] != TechStackPython {
		t.Fatalf("expected python tech stack, got %#v", got.TechStacks)
	}
	if len(got.ProcessHints.PythonSignals) != 2 {
		t.Fatalf("expected python signals, got %#v", got.ProcessHints.PythonSignals)
	}
}

func TestClassifyPythonRepoFromPackageLayoutOnly(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "tests"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tests", "test_demo.py"), []byte("def test_demo():\n    assert True\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := Classify(dir)

	if len(got.TechStacks) != 1 || got.TechStacks[0] != TechStackPython {
		t.Fatalf("expected python tech stack from package layout, got %#v", got.TechStacks)
	}
	if len(got.ProcessHints.PythonSignals) != 1 || got.ProcessHints.PythonSignals[0] != "python_package_layout" {
		t.Fatalf("expected python package layout signal, got %#v", got.ProcessHints.PythonSignals)
	}
}

func TestClassifyNonPythonRepoHasNoPythonTechStack(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# demo\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := Classify(dir)

	for _, stack := range got.TechStacks {
		if stack == TechStackPython {
			t.Fatalf("unexpected python tech stack for non-Python repo")
		}
	}
	if len(got.ProcessHints.PythonSignals) != 0 {
		t.Fatalf("expected no python signals, got %#v", got.ProcessHints.PythonSignals)
	}
}

func TestClassifyNonPythonRepoWithGenericSourceLayoutHasNoPythonTechStack(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "src", "demo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "src", "demo", "index.ts"), []byte("export const demo = true;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "tests"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tests", "README.md"), []byte("# tests\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := Classify(dir)

	for _, stack := range got.TechStacks {
		if stack == TechStackPython {
			t.Fatalf("unexpected python tech stack for generic source layout: %#v", got.TechStacks)
		}
	}
	if len(got.ProcessHints.PythonSignals) != 0 {
		t.Fatalf("expected no python signals for generic source layout, got %#v", got.ProcessHints.PythonSignals)
	}
}

func TestClassifyGoAndPythonRepoDetectsBoth(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/demo\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pyproject.toml"), []byte("[project]\nname = \"demo\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := Classify(dir)

	hasGo := false
	hasPython := false
	for _, stack := range got.TechStacks {
		if stack == TechStackGo {
			hasGo = true
		}
		if stack == TechStackPython {
			hasPython = true
		}
	}
	if !hasGo {
		t.Fatalf("expected go tech stack, got %#v", got.TechStacks)
	}
	if !hasPython {
		t.Fatalf("expected python tech stack, got %#v", got.TechStacks)
	}
}

func TestClassifyGoAndNodeJSRepoDetectsBoth(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/demo\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte("{\"name\":\"demo\"}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := Classify(dir)

	hasGo := false
	hasNodeJS := false
	for _, stack := range got.TechStacks {
		if stack == TechStackGo {
			hasGo = true
		}
		if stack == TechStackNodeJS {
			hasNodeJS = true
		}
	}
	if !hasGo {
		t.Fatalf("expected go tech stack, got %#v", got.TechStacks)
	}
	if !hasNodeJS {
		t.Fatalf("expected nodejs tech stack, got %#v", got.TechStacks)
	}
}

func TestClassifyGoWithTurborepoFrontendDetectsBothStacks(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/demo\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte("{\"workspaces\":[\"apps/*\"]}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "turbo.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pnpm-workspace.yaml"), []byte("packages:\n  - apps/*\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pnpm-lock.yaml"), []byte("lockfileVersion: 5\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := Classify(dir)

	if got.Shape != ShapeMonorepo {
		t.Fatalf("expected monorepo, got %#v", got.Shape)
	}
	if got.MonorepoStack != MonorepoStackTurborepo {
		t.Fatalf("expected turborepo stack, got %#v", got.MonorepoStack)
	}
	hasGo := false
	hasNodeJS := false
	for _, stack := range got.TechStacks {
		if stack == TechStackGo {
			hasGo = true
		}
		if stack == TechStackNodeJS {
			hasNodeJS = true
		}
	}
	if !hasGo {
		t.Fatalf("expected go tech stack, got %#v", got.TechStacks)
	}
	if !hasNodeJS {
		t.Fatalf("expected nodejs tech stack, got %#v", got.TechStacks)
	}
}

func TestClassifyGoWithHTMXFrontendRemainsTraditional(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/demo\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "templates"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "templates", "index.html"), []byte("<html></html>\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := Classify(dir)

	if got.Shape != ShapeTraditional {
		t.Fatalf("expected traditional for Go+HTMX, got %#v", got.Shape)
	}
	if len(got.TechStacks) != 1 || got.TechStacks[0] != TechStackGo {
		t.Fatalf("expected go-only tech stack, got %#v", got.TechStacks)
	}
}

func TestClassifyGitHubActionsRepoFromWorkflows(t *testing.T) {
	dir := t.TempDir()
	workflowsDir := filepath.Join(dir, ".github", "workflows")
	if err := os.MkdirAll(workflowsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workflowsDir, "ci.yml"), []byte("name: CI\non: push\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := Classify(dir)

	if got.Shape != ShapeTraditional {
		t.Fatalf("expected traditional shape, got %#v", got.Shape)
	}
	hasActions := false
	for _, stack := range got.TechStacks {
		if stack == TechStackGitHubActions {
			hasActions = true
		}
	}
	if !hasActions {
		t.Fatalf("expected github-actions tech stack, got %#v", got.TechStacks)
	}
}

func TestClassifyDotNetRepoFromCSharpProject(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "demo.csproj"), []byte("<Project Sdk=\"Microsoft.NET.Sdk\"></Project>\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := Classify(dir)

	if got.Shape != ShapeTraditional {
		t.Fatalf("expected traditional shape, got %#v", got.Shape)
	}
	if len(got.TechStacks) != 1 || got.TechStacks[0] != TechStackDotNet {
		t.Fatalf("expected dotnet tech stack, got %#v", got.TechStacks)
	}
}

func TestClassifyGitHubActionsRepoFromYAMLExtension(t *testing.T) {
	dir := t.TempDir()
	workflowsDir := filepath.Join(dir, ".github", "workflows")
	if err := os.MkdirAll(workflowsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workflowsDir, "deploy.yaml"), []byte("name: Deploy\non: push\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := Classify(dir)

	hasActions := false
	for _, stack := range got.TechStacks {
		if stack == TechStackGitHubActions {
			hasActions = true
		}
	}
	if !hasActions {
		t.Fatalf("expected github-actions tech stack for .yaml workflow, got %#v", got.TechStacks)
	}
}

func TestClassifyDotNetRepoFromNestedProjectLayout(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "src", "App"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "src", "App", "App.csproj"), []byte("<Project Sdk=\"Microsoft.NET.Sdk.Web\"></Project>\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := Classify(dir)

	if len(got.TechStacks) != 1 || got.TechStacks[0] != TechStackDotNet {
		t.Fatalf("expected dotnet tech stack from nested project layout, got %#v", got.TechStacks)
	}
}

func TestClassifyNoGitHubActionsWithoutWorkflows(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".github"), 0o755); err != nil {
		t.Fatal(err)
	}

	got := Classify(dir)

	for _, stack := range got.TechStacks {
		if stack == TechStackGitHubActions {
			t.Fatalf("unexpected github-actions tech stack for repo without workflows")
		}
	}
}

func TestClassifyNoGitHubActionsWithEmptyWorkflowsDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".github", "workflows"), 0o755); err != nil {
		t.Fatal(err)
	}

	got := Classify(dir)

	for _, stack := range got.TechStacks {
		if stack == TechStackGitHubActions {
			t.Fatalf("unexpected github-actions tech stack for empty workflows directory")
		}
	}
}

func TestClassifyGoAndGitHubActionsRepoDetectsBoth(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/demo\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	workflowsDir := filepath.Join(dir, ".github", "workflows")
	if err := os.MkdirAll(workflowsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workflowsDir, "ci.yml"), []byte("name: CI\non: push\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := Classify(dir)

	hasGo := false
	hasActions := false
	for _, stack := range got.TechStacks {
		if stack == TechStackGo {
			hasGo = true
		}
		if stack == TechStackGitHubActions {
			hasActions = true
		}
	}
	if !hasGo {
		t.Fatalf("expected go tech stack, got %#v", got.TechStacks)
	}
	if !hasActions {
		t.Fatalf("expected github-actions tech stack, got %#v", got.TechStacks)
	}
}

func TestClassifyDockerRepoFromDockerfile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM alpine:3.19\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := Classify(dir)

	if got.Shape != ShapeTraditional {
		t.Fatalf("expected traditional shape, got %#v", got.Shape)
	}
	hasDocker := false
	for _, stack := range got.TechStacks {
		if stack == TechStackDocker {
			hasDocker = true
		}
	}
	if !hasDocker {
		t.Fatalf("expected docker tech stack, got %#v", got.TechStacks)
	}
	if len(got.ProcessHints.DockerFiles) != 1 || got.ProcessHints.DockerFiles[0] != "Dockerfile" {
		t.Fatalf("expected Dockerfile in docker files, got %#v", got.ProcessHints.DockerFiles)
	}
}

func TestClassifyDockerRepoFromComposeFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte("version: '3'\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := Classify(dir)

	hasDocker := false
	for _, stack := range got.TechStacks {
		if stack == TechStackDocker {
			hasDocker = true
		}
	}
	if !hasDocker {
		t.Fatalf("expected docker tech stack from compose file, got %#v", got.TechStacks)
	}
	if len(got.ProcessHints.DockerFiles) != 1 || got.ProcessHints.DockerFiles[0] != "docker-compose.yml" {
		t.Fatalf("expected docker-compose.yml in docker files, got %#v", got.ProcessHints.DockerFiles)
	}
}

func TestClassifyDockerRepoMultipleSignals(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM node:20\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "docker-compose.yaml"), []byte("version: '3'\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".dockerignore"), []byte("node_modules\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := Classify(dir)

	if len(got.ProcessHints.DockerFiles) != 3 {
		t.Fatalf("expected 3 docker files, got %#v", got.ProcessHints.DockerFiles)
	}
}

func TestClassifyNonDockerRepoHasNoDockerTechStack(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.py"), []byte("print('hello')\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := Classify(dir)

	for _, stack := range got.TechStacks {
		if stack == TechStackDocker {
			t.Fatalf("unexpected docker tech stack for non-Docker repo")
		}
	}
	if len(got.ProcessHints.DockerFiles) != 0 {
		t.Fatalf("expected no docker files, got %#v", got.ProcessHints.DockerFiles)
	}
}

func TestClassifyGoAndDotNetRepoDetectsBoth(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/demo\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "demo.sln"), []byte("Microsoft Visual Studio Solution File, Format Version 12.00\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := Classify(dir)

	hasGo := false
	hasDotNet := false
	for _, stack := range got.TechStacks {
		if stack == TechStackGo {
			hasGo = true
		}
		if stack == TechStackDotNet {
			hasDotNet = true
		}
	}
	if !hasGo || !hasDotNet {
		t.Fatalf("expected go and dotnet tech stacks, got %#v", got.TechStacks)
	}
}

func TestClassifyGoAndDockerRepoDetectsBoth(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/demo\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM golang:1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := Classify(dir)

	hasGo := false
	hasDocker := false
	for _, stack := range got.TechStacks {
		if stack == TechStackGo {
			hasGo = true
		}
		if stack == TechStackDocker {
			hasDocker = true
		}
	}
	if !hasGo {
		t.Fatalf("expected go tech stack, got %#v", got.TechStacks)
	}
	if !hasDocker {
		t.Fatalf("expected docker tech stack, got %#v", got.TechStacks)
	}
}

func TestClassifyKubernetesRepoFromKustomization(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "kustomization.yaml"), []byte("resources:\n  - deployment.yaml\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := Classify(dir)

	hasK8s := false
	for _, stack := range got.TechStacks {
		if stack == TechStackKubernetes {
			hasK8s = true
		}
	}
	if !hasK8s {
		t.Fatalf("expected kubernetes tech stack, got %#v", got.TechStacks)
	}
}

func TestClassifyKubernetesRepoFromSkaffold(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "skaffold.yaml"), []byte("apiVersion: skaffold/v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := Classify(dir)

	hasK8s := false
	for _, stack := range got.TechStacks {
		if stack == TechStackKubernetes {
			hasK8s = true
		}
	}
	if !hasK8s {
		t.Fatalf("expected kubernetes tech stack from skaffold.yaml, got %#v", got.TechStacks)
	}
}

func TestClassifyKubernetesRepoFromHelmChart(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Chart.yaml"), []byte("apiVersion: v2\nname: demo\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := Classify(dir)

	hasK8s := false
	for _, stack := range got.TechStacks {
		if stack == TechStackKubernetes {
			hasK8s = true
		}
	}
	if !hasK8s {
		t.Fatalf("expected kubernetes tech stack from Chart.yaml, got %#v", got.TechStacks)
	}
}

func TestClassifyKubernetesRepoFromManifestDirectory(t *testing.T) {
	dir := t.TempDir()
	k8sDir := filepath.Join(dir, "k8s")
	if err := os.MkdirAll(k8sDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(k8sDir, "deployment.yaml"), []byte("apiVersion: apps/v1\nkind: Deployment\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := Classify(dir)

	hasK8s := false
	for _, stack := range got.TechStacks {
		if stack == TechStackKubernetes {
			hasK8s = true
		}
	}
	if !hasK8s {
		t.Fatalf("expected kubernetes tech stack from k8s/ directory, got %#v", got.TechStacks)
	}
}

func TestClassifyNonKubernetesRepoHasNoKubernetesTechStack(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := Classify(dir)

	for _, stack := range got.TechStacks {
		if stack == TechStackKubernetes {
			t.Fatalf("unexpected kubernetes tech stack for non-Kubernetes repo")
		}
	}
}

func TestClassifyKubernetesAndGoRepoDetectsBoth(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/demo\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "kustomization.yaml"), []byte("resources:\n  - deployment.yaml\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := Classify(dir)

	hasGo := false
	hasK8s := false
	for _, stack := range got.TechStacks {
		if stack == TechStackGo {
			hasGo = true
		}
		if stack == TechStackKubernetes {
			hasK8s = true
		}
	}
	if !hasGo {
		t.Fatalf("expected go tech stack, got %#v", got.TechStacks)
	}
	if !hasK8s {
		t.Fatalf("expected kubernetes tech stack, got %#v", got.TechStacks)
	}
}

func TestClassifyKubernetesNotDetectedFromNonK8sYAML(t *testing.T) {
	dir := t.TempDir()
	yamlDir := filepath.Join(dir, "k8s")
	if err := os.MkdirAll(yamlDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(yamlDir, "config.yaml"), []byte("key: value\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := Classify(dir)

	for _, stack := range got.TechStacks {
		if stack == TechStackKubernetes {
			t.Fatalf("should not detect kubernetes from non-Kubernetes YAML")
		}
	}
}

func TestClassifyPHPRepoFromComposerJSON(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "composer.json"), []byte("{\"name\":\"vendor/demo\"}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := Classify(dir)

	if got.Shape != ShapeTraditional {
		t.Fatalf("expected traditional shape, got %#v", got.Shape)
	}
	if len(got.TechStacks) != 1 || got.TechStacks[0] != TechStackPHP {
		t.Fatalf("expected php tech stack, got %#v", got.TechStacks)
	}
}

func TestClassifyNonPHPRepoHasNoPHPTechStack(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.py"), []byte("print('hello')\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := Classify(dir)

	for _, stack := range got.TechStacks {
		if stack == TechStackPHP {
			t.Fatalf("unexpected php tech stack for non-PHP repo")
		}
	}
}

func TestClassifyPHPAndNodeJSRepoDetectsBoth(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "composer.json"), []byte("{\"name\":\"vendor/demo\"}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte("{\"name\":\"demo\"}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := Classify(dir)

	hasPHP := false
	hasNodeJS := false
	for _, stack := range got.TechStacks {
		if stack == TechStackPHP {
			hasPHP = true
		}
		if stack == TechStackNodeJS {
			hasNodeJS = true
		}
	}
	if !hasPHP {
		t.Fatalf("expected php tech stack, got %#v", got.TechStacks)
	}
	if !hasNodeJS {
		t.Fatalf("expected nodejs tech stack, got %#v", got.TechStacks)
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
