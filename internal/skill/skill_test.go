package skill

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	skillassets "github.com/nicobistolfi/vigilante"
	ghcli "github.com/nicobistolfi/vigilante/internal/github"
	"github.com/nicobistolfi/vigilante/internal/repo"
	"github.com/nicobistolfi/vigilante/internal/state"
)

func TestEnsureInstalledPrefersRepoSkillsWhenAvailable(t *testing.T) {
	dir := t.TempDir()
	repoRoot := t.TempDir()
	for _, name := range VigilanteSkillNames() {
		skillSourceDir := filepath.Join(repoRoot, "skills", name)
		if err := os.MkdirAll(skillSourceDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(skillSourceDir, "SKILL.md"), []byte("# repo skill\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(skillSourceDir, "agents"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(skillSourceDir, "agents", "openai.yaml"), []byte("interface:\n  display_name: test\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(repoRoot); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = os.Chdir(wd)
	}()

	if err := EnsureInstalled(RuntimeCodex, dir); err != nil {
		t.Fatal(err)
	}
	for _, name := range VigilanteSkillNames() {
		path := filepath.Join(dir, "skills", name, "SKILL.md")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != "# repo skill\n" {
			t.Fatalf("unexpected skill body: %s", string(data))
		}
		agentData, err := os.ReadFile(filepath.Join(dir, "skills", name, "agents", "openai.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		if string(agentData) != "interface:\n  display_name: test\n" {
			t.Fatalf("unexpected agent body: %s", string(agentData))
		}
	}
}

func TestResolveSkillSourceFallsBackToEmbeddedAssets(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Chdir(outside); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = os.Chdir(wd)
	}()

	for _, name := range VigilanteSkillNames() {
		source, err := resolveSkillSource(name)
		if err != nil {
			t.Fatal(err)
		}

		embedded, ok := source.(embeddedSkillSource)
		if !ok {
			t.Fatalf("expected embedded skill source for %s, got %T", name, source)
		}

		bodyPath := pathJoin(embedded.root, "SKILL.md")
		expected, err := fs.ReadFile(skillassets.Skills, bodyPath)
		if err != nil {
			t.Fatal(err)
		}
		actual, err := fs.ReadFile(embedded.fs, bodyPath)
		if err != nil {
			t.Fatal(err)
		}
		if string(actual) != string(expected) {
			t.Fatalf("unexpected embedded body for %s", name)
		}
	}
}

func TestEnsureInstalledUsesEmbeddedAssetsOutsideRepo(t *testing.T) {
	dir := t.TempDir()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Chdir(outside); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = os.Chdir(wd)
	}()

	if err := EnsureInstalled(RuntimeCodex, dir); err != nil {
		t.Fatal(err)
	}

	for _, name := range VigilanteSkillNames() {
		path := filepath.Join(dir, "skills", name, "SKILL.md")
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected %s to exist: %v", path, err)
		}
	}
}

func TestBuildIssuePrompt(t *testing.T) {
	target := state.WatchTarget{Path: "/tmp/repo", Repo: "owner/repo"}
	issue := ghcli.Issue{Number: 12, Title: "Fix bug", URL: "https://example.com/issues/12"}
	session := state.Session{WorktreePath: "/tmp/worktree", Branch: "vigilante/issue-12", Provider: "Codex"}
	prompt := BuildIssuePrompt(target, issue, session)
	for _, text := range []string{"Use the `vigilante-issue-implementation` skill", "Detected repo shape: traditional", `Repo process context JSON: {"shape":"traditional"}`, "Selected issue implementation skill: vigilante-issue-implementation", "Issue: #12 - Fix bug", "Worktree path: /tmp/worktree", "gh issue comment", "implementation plan", "open a pull request", "Coding Agent Launched: Codex", "10-cell progress bar", "ETA: ~N minutes"} {
		if !strings.Contains(prompt, text) {
			t.Fatalf("prompt missing %q: %s", text, prompt)
		}
	}
}

func TestBuildIssuePromptIncludesReusedRemoteBranchContext(t *testing.T) {
	target := state.WatchTarget{Path: "/tmp/repo", Repo: "owner/repo", Branch: "main"}
	issue := ghcli.Issue{Number: 12, Title: "Fix bug", URL: "https://example.com/issues/12"}
	session := state.Session{
		WorktreePath:       "/tmp/worktree",
		Branch:             "vigilante/issue-12-fix-bug",
		BaseBranch:         "main",
		ReusedRemoteBranch: "vigilante/issue-12-fix-bug",
		BranchDiffSummary:  "README.md | 2 ++",
		Provider:           "Codex",
	}
	prompt := BuildIssuePrompt(target, issue, session)
	for _, text := range []string{
		"Existing remote issue branch detected: origin/vigilante/issue-12-fix-bug",
		"Default branch for comparison: main",
		"Diff summary against `main`: README.md | 2 ++",
		"Continue from the reused branch state",
	} {
		if !strings.Contains(prompt, text) {
			t.Fatalf("prompt missing %q: %s", text, prompt)
		}
	}
}

func TestVigilanteSkillNamesIncludesLocalServiceDependencies(t *testing.T) {
	foundLocalServices := false
	foundComposeLaunch := false
	for _, name := range VigilanteSkillNames() {
		if name == VigilanteLocalServiceDependencies {
			foundLocalServices = true
		}
		if name == DockerComposeLaunch {
			foundComposeLaunch = true
		}
	}
	if !foundLocalServices {
		t.Fatalf("expected %s to be bundled", VigilanteLocalServiceDependencies)
	}
	if !foundComposeLaunch {
		t.Fatalf("expected %s to be bundled", DockerComposeLaunch)
	}
}

func TestBuildIssuePromptSelectsMonorepoSkill(t *testing.T) {
	target := state.WatchTarget{
		Path: "/tmp/repo",
		Repo: "owner/repo",
		Classification: repo.Classification{
			Shape:         repo.ShapeMonorepo,
			MonorepoStack: repo.MonorepoStackTurborepo,
			ProcessHints: repo.ProcessHints{
				WorkspaceConfigFiles: []string{"pnpm-workspace.yaml", "turbo.json"},
				MultiPackageRoots:    []string{"apps", "packages"},
			},
		},
	}
	issue := ghcli.Issue{Number: 12, Title: "Fix bug", URL: "https://example.com/issues/12"}
	session := state.Session{WorktreePath: "/tmp/worktree", Branch: "vigilante/issue-12", Provider: "Codex"}

	prompt := BuildIssuePrompt(target, issue, session)

	for _, text := range []string{
		"Use the `vigilante-issue-implementation-on-turborepo` skill",
		"Detected repo shape: monorepo",
		"Detected monorepo stack: turborepo",
		"Selected issue implementation skill: vigilante-issue-implementation-on-turborepo",
		`"monorepo_stack":"turborepo"`,
		`"implementation_skill":"vigilante-issue-implementation-on-turborepo"`,
		`"workspace_config_files":["pnpm-workspace.yaml","turbo.json"]`,
		`"turbo.json"`,
		`"multi_package_roots":["apps","packages"]`,
		`"launch_skill":"docker-compose-launch"`,
		`"supported_service_types":["mysql","mariadb","postgres","mongodb"]`,
	} {
		if !strings.Contains(prompt, text) {
			t.Fatalf("prompt missing %q: %s", text, prompt)
		}
	}
}

func TestBuildIssuePromptFallsBackForUnknownMonorepoStack(t *testing.T) {
	target := state.WatchTarget{
		Path: "/tmp/repo",
		Repo: "owner/repo",
		Classification: repo.Classification{
			Shape:         repo.ShapeMonorepo,
			MonorepoStack: repo.MonorepoStackUnknown,
		},
	}
	issue := ghcli.Issue{Number: 12, Title: "Fix bug", URL: "https://example.com/issues/12"}
	session := state.Session{WorktreePath: "/tmp/worktree", Branch: "vigilante/issue-12", Provider: "Codex"}

	prompt := BuildIssuePrompt(target, issue, session)

	for _, text := range []string{
		"Use the `vigilante-issue-implementation-on-monorepo` skill",
		"Detected monorepo stack: unknown",
		"Selected issue implementation skill: vigilante-issue-implementation-on-monorepo",
	} {
		if !strings.Contains(prompt, text) {
			t.Fatalf("prompt missing %q: %s", text, prompt)
		}
	}
}

func TestBuildIssuePromptSelectsTurborepoSkill(t *testing.T) {
	target := state.WatchTarget{
		Path: "/tmp/repo",
		Repo: "owner/repo",
		Classification: repo.Classification{
			Shape: repo.ShapeMonorepo,
			ProcessHints: repo.ProcessHints{
				WorkspaceConfigFiles:   []string{"pnpm-workspace.yaml", "turbo.json"},
				WorkspaceManifestFiles: []string{"package.json"},
				MultiPackageRoots:      []string{"apps", "packages"},
			},
		},
	}
	issue := ghcli.Issue{Number: 12, Title: "Fix bug", URL: "https://example.com/issues/12"}
	session := state.Session{WorktreePath: "/tmp/worktree", Branch: "vigilante/issue-12", Provider: "Codex"}

	prompt := BuildIssuePrompt(target, issue, session)

	for _, text := range []string{
		"Use the `vigilante-issue-implementation-on-turborepo` skill",
		"Detected repo shape: monorepo",
		"Selected issue implementation skill: vigilante-issue-implementation-on-turborepo",
		`"workspace_config_files":["pnpm-workspace.yaml","turbo.json"]`,
		`"workspace_manifest_files":["package.json"]`,
		`"multi_package_roots":["apps","packages"]`,
	} {
		if !strings.Contains(prompt, text) {
			t.Fatalf("prompt missing %q: %s", text, prompt)
		}
	}
}

func TestBuildIssuePromptSelectsGradleMultiProjectSkill(t *testing.T) {
	target := state.WatchTarget{
		Path: "/tmp/repo",
		Repo: "owner/repo",
		Classification: repo.Classification{
			Shape: repo.ShapeGradleMultiProject,
			ProcessHints: repo.ProcessHints{
				GradleSettingsFiles:  []string{"settings.gradle.kts"},
				GradleRootBuildFiles: []string{"build.gradle.kts"},
			},
		},
	}
	issue := ghcli.Issue{Number: 12, Title: "Fix bug", URL: "https://example.com/issues/12"}
	session := state.Session{WorktreePath: "/tmp/worktree", Branch: "vigilante/issue-12", Provider: "Codex"}

	prompt := BuildIssuePrompt(target, issue, session)

	for _, text := range []string{
		"Use the `vigilante-issue-implementation-on-gradle-multi-project` skill",
		"Detected repo shape: gradle_multi_project",
		"Selected issue implementation skill: vigilante-issue-implementation-on-gradle-multi-project",
		`"gradle_settings_files":["settings.gradle.kts"]`,
		`"gradle_root_build_files":["build.gradle.kts"]`,
	} {
		if !strings.Contains(prompt, text) {
			t.Fatalf("prompt missing %q: %s", text, prompt)
		}
	}
}

func TestBuildIssuePromptSelectsNxSkill(t *testing.T) {
	target := state.WatchTarget{
		Path: "/tmp/repo",
		Repo: "owner/repo",
		Classification: repo.Classification{
			Shape:         repo.ShapeMonorepo,
			MonorepoStack: repo.MonorepoStackNx,
			ProcessHints: repo.ProcessHints{
				WorkspaceConfigFiles: []string{"nx.json", "pnpm-workspace.yaml"},
				MultiPackageRoots:    []string{"apps", "libs"},
			},
		},
	}
	issue := ghcli.Issue{Number: 12, Title: "Fix bug", URL: "https://example.com/issues/12"}
	session := state.Session{WorktreePath: "/tmp/worktree", Branch: "vigilante/issue-12", Provider: "Codex"}

	prompt := BuildIssuePrompt(target, issue, session)

	for _, text := range []string{
		"Use the `vigilante-issue-implementation-on-nx` skill",
		"Detected repo shape: monorepo",
		"Detected monorepo stack: nx",
		"Selected issue implementation skill: vigilante-issue-implementation-on-nx",
		`"monorepo_stack":"nx"`,
		`"implementation_skill":"vigilante-issue-implementation-on-nx"`,
		`"workspace_config_files":["nx.json","pnpm-workspace.yaml"]`,
		`"multi_package_roots":["apps","libs"]`,
	} {
		if !strings.Contains(prompt, text) {
			t.Fatalf("prompt missing %q: %s", text, prompt)
		}
	}
}

func TestBuildIssuePromptSelectsBazelMonorepoSkill(t *testing.T) {
	target := state.WatchTarget{
		Path: "/tmp/repo",
		Repo: "owner/repo",
		Classification: repo.Classification{
			Shape: repo.ShapeMonorepo,
			ProcessHints: repo.ProcessHints{
				BazelRepoMarkers:  []string{"MODULE.bazel", "WORKSPACE"},
				BazelPackageRoots: []string{"apps", "services"},
			},
		},
	}
	issue := ghcli.Issue{Number: 12, Title: "Fix bug", URL: "https://example.com/issues/12"}
	session := state.Session{WorktreePath: "/tmp/worktree", Branch: "vigilante/issue-12", Provider: "Codex"}

	prompt := BuildIssuePrompt(target, issue, session)

	for _, text := range []string{
		"Use the `vigilante-issue-implementation-on-bazel-monorepo` skill",
		"Selected issue implementation skill: vigilante-issue-implementation-on-bazel-monorepo",
		`"bazel_repo_markers":["MODULE.bazel","WORKSPACE"]`,
		`"bazel_package_roots":["apps","services"]`,
	} {
		if !strings.Contains(prompt, text) {
			t.Fatalf("prompt missing %q: %s", text, prompt)
		}
	}
}

func TestBuildIssuePreflightPrompt(t *testing.T) {
	target := state.WatchTarget{Path: "/tmp/repo", Repo: "owner/repo"}
	issue := ghcli.Issue{Number: 12, Title: "Fix bug", URL: "https://example.com/issues/12"}
	session := state.Session{WorktreePath: "/tmp/worktree", Branch: "vigilante/issue-12", Provider: "Codex"}
	prompt := BuildIssuePreflightPrompt(target, issue, session)
	for _, text := range []string{"Repository: owner/repo", "Issue: #12 - Fix bug", "`main`-derived worktree", "build or equivalent verification command", "existing test suite", "Do not implement the issue", "do not comment on GitHub"} {
		if !strings.Contains(prompt, text) {
			t.Fatalf("prompt missing %q: %s", text, prompt)
		}
	}
}

func TestBuildIssuePreflightPromptForReusedRemoteBranch(t *testing.T) {
	target := state.WatchTarget{Path: "/tmp/repo", Repo: "owner/repo"}
	issue := ghcli.Issue{Number: 12, Title: "Fix bug", URL: "https://example.com/issues/12"}
	session := state.Session{
		WorktreePath:       "/tmp/worktree",
		Branch:             "vigilante/issue-12-fix-bug",
		BaseBranch:         "main",
		ReusedRemoteBranch: "vigilante/issue-12-fix-bug",
	}
	prompt := BuildIssuePreflightPrompt(target, issue, session)
	for _, text := range []string{
		"reused issue-branch worktree",
		"origin/vigilante/issue-12-fix-bug",
		"compared against `main`",
	} {
		if !strings.Contains(prompt, text) {
			t.Fatalf("prompt missing %q: %s", text, prompt)
		}
	}
}

func TestBuildConflictResolutionPrompt(t *testing.T) {
	target := state.WatchTarget{Path: "/tmp/repo", Repo: "owner/repo"}
	session := state.Session{IssueNumber: 12, IssueTitle: "Fix bug", IssueURL: "https://example.com/issues/12", WorktreePath: "/tmp/worktree", Branch: "vigilante/issue-12"}
	pr := ghcli.PullRequest{Number: 88, URL: "https://example.com/pull/88"}
	prompt := BuildConflictResolutionPrompt(target, session, pr)
	for _, text := range []string{"Use the `vigilante-conflict-resolution` skill", "Pull Request: #88", "Base branch: origin/main", "go test ./...", "merge-ready state"} {
		if !strings.Contains(prompt, text) {
			t.Fatalf("prompt missing %q: %s", text, prompt)
		}
	}
}

func TestEnsureInstalledForClaudeCreatesCommandsAndSkills(t *testing.T) {
	dir := t.TempDir()
	repoRoot := t.TempDir()
	for _, name := range VigilanteSkillNames() {
		skillSourceDir := filepath.Join(repoRoot, "skills", name)
		if err := os.MkdirAll(skillSourceDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(skillSourceDir, "SKILL.md"), []byte("# repo skill\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(repoRoot); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = os.Chdir(wd)
	}()

	if err := EnsureInstalled(RuntimeClaude, dir); err != nil {
		t.Fatal(err)
	}

	for _, name := range VigilanteSkillNames() {
		for _, path := range []string{
			filepath.Join(dir, "skills", name, "SKILL.md"),
			filepath.Join(dir, "commands", name, "SKILL.md"),
		} {
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("expected %s to exist: %v", path, err)
			}
		}
	}
}

func TestEnsureInstalledForGeminiCreatesCommandsAndSkills(t *testing.T) {
	dir := t.TempDir()
	repoRoot := t.TempDir()
	for _, name := range VigilanteSkillNames() {
		skillSourceDir := filepath.Join(repoRoot, "skills", name)
		if err := os.MkdirAll(skillSourceDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(skillSourceDir, "SKILL.md"), []byte("# repo skill\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(repoRoot); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = os.Chdir(wd)
	}()

	if err := EnsureInstalled(RuntimeGemini, dir); err != nil {
		t.Fatal(err)
	}

	for _, name := range VigilanteSkillNames() {
		for _, path := range []string{
			filepath.Join(dir, "skills", name, "SKILL.md"),
			filepath.Join(dir, "commands", name+".toml"),
		} {
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("expected %s to exist: %v", path, err)
			}
		}
	}
}

func TestBuildIssuePromptForClaudeInlinesSkillInstructions(t *testing.T) {
	target := state.WatchTarget{Path: "/tmp/repo", Repo: "owner/repo"}
	issue := ghcli.Issue{Number: 12, Title: "Fix bug", URL: "https://example.com/issues/12"}
	session := state.Session{WorktreePath: "/tmp/worktree", Branch: "vigilante/issue-12", Provider: "claude"}
	prompt := BuildIssuePromptForRuntime(RuntimeClaude, target, issue, session)
	for _, text := range []string{"Follow these `vigilante-issue-implementation` skill instructions directly", "Coding Agent Launched: Claude Code", "Issue: #12 - Fix bug"} {
		if !strings.Contains(prompt, text) {
			t.Fatalf("prompt missing %q: %s", text, prompt)
		}
	}
}

func TestBuildIssuePromptForGeminiInlinesSkillInstructions(t *testing.T) {
	target := state.WatchTarget{Path: "/tmp/repo", Repo: "owner/repo"}
	issue := ghcli.Issue{Number: 12, Title: "Fix bug", URL: "https://example.com/issues/12"}
	session := state.Session{WorktreePath: "/tmp/worktree", Branch: "vigilante/issue-12", Provider: "gemini"}
	prompt := BuildIssuePromptForRuntime(RuntimeGemini, target, issue, session)
	for _, text := range []string{"Follow these `vigilante-issue-implementation` skill instructions directly", "Coding Agent Launched: Gemini CLI", "Issue: #12 - Fix bug"} {
		if !strings.Contains(prompt, text) {
			t.Fatalf("prompt missing %q: %s", text, prompt)
		}
	}
}

func TestIssueImplementationSkillDefaultsToTraditional(t *testing.T) {
	if got := IssueImplementationSkill(state.WatchTarget{}); got != VigilanteIssueImplementation {
		t.Fatalf("unexpected default issue skill: %s", got)
	}
}

func TestIssueImplementationSkillMapsKnownMonorepoStacks(t *testing.T) {
	tests := map[repo.MonorepoStack]string{
		repo.MonorepoStackTurborepo: VigilanteIssueImplementationOnTurborepo,
		repo.MonorepoStackNx:        VigilanteIssueImplementationOnNx,
		repo.MonorepoStackRush:      VigilanteIssueImplementationOnRush,
		repo.MonorepoStackBazel:     VigilanteIssueImplementationOnBazel,
		repo.MonorepoStackGradle:    VigilanteIssueImplementationOnGradle,
		repo.MonorepoStackUnknown:   VigilanteIssueImplementationOnMonorepo,
	}
	for stack, want := range tests {
		target := state.WatchTarget{
			Classification: repo.Classification{
				Shape:         repo.ShapeMonorepo,
				MonorepoStack: stack,
			},
		}
		if got := IssueImplementationSkill(target); got != want {
			t.Fatalf("stack %s: got %s want %s", stack, got, want)
		}
	}
}

func TestIssueImplementationSkillSelectsTurborepo(t *testing.T) {
	target := state.WatchTarget{
		Classification: repo.Classification{
			Shape: repo.ShapeMonorepo,
			ProcessHints: repo.ProcessHints{
				WorkspaceConfigFiles:   []string{"pnpm-workspace.yaml", "turbo.json"},
				WorkspaceManifestFiles: []string{"package.json"},
			},
		},
	}

	if got := IssueImplementationSkill(target); got != VigilanteIssueImplementationOnTurborepo {
		t.Fatalf("unexpected turborepo issue skill: %s", got)
	}
}

func TestIssueImplementationSkillSelectsGradleMultiProject(t *testing.T) {
	target := state.WatchTarget{
		Classification: repo.Classification{Shape: repo.ShapeGradleMultiProject},
	}
	if got := IssueImplementationSkill(target); got != VigilanteIssueImplementationOnGradleMultiProject {
		t.Fatalf("unexpected gradle multi-project issue skill: %s", got)
	}
}

func TestVigilanteCreateIssueSkillCoversIssueTypeClassification(t *testing.T) {
	body, err := os.ReadFile(repoSkillPath(VigilanteCreateIssue))
	if err != nil {
		t.Fatal(err)
	}

	text := string(body)
	for _, snippet := range []string{
		"classified as a `feature`, `bug`, or `task` before the draft is finalized",
		"Decide whether the request is best treated as a `feature`, `bug`, or `task`.",
		"If the request is ambiguous, infer the most likely type and state briefly that the type was inferred.",
		"Issue Type: <feature | bug | task>[ (inferred)]",
	} {
		if !strings.Contains(text, snippet) {
			t.Fatalf("skill missing %q", snippet)
		}
	}
}

func TestVigilanteCreateIssueSkillIncludesTypeSpecificDetailGuidance(t *testing.T) {
	body, err := os.ReadFile(repoSkillPath(VigilanteCreateIssue))
	if err != nil {
		t.Fatal(err)
	}

	text := string(body)
	for _, snippet := range []string{
		"For `bug` issues, prioritize current behavior, expected behavior, impact, reproduction clues, and regression risk.",
		"For `feature` issues, prioritize the desired user-facing outcome, scope boundaries, and non-goals.",
		"For `task` issues, prioritize the concrete deliverable, operational context, constraints, and completion conditions.",
		"- `bug`: include current behavior, expected behavior, impact, and reproduction clues when available.",
		"- `feature`: include the desired outcome, boundaries, and explicit non-goals.",
		"- `task`: include the deliverable, operational context, constraints, and concrete done criteria.",
	} {
		if !strings.Contains(text, snippet) {
			t.Fatalf("skill missing %q", snippet)
		}
	}
}

func TestLocalServiceDependenciesSkillCoversStructuredOutputAndFailureModes(t *testing.T) {
	body, err := os.ReadFile(repoSkillPath(VigilanteLocalServiceDependencies))
	if err != nil {
		t.Fatal(err)
	}

	text := string(body)
	for _, snippet := range []string{
		"Prefer repository-provided service startup mechanisms",
		"repository-owned `docker compose` or `docker-compose` files before generating anything new",
		"Docker Compose is an allowed fallback, not the defining abstraction.",
		"`status`: `ready`, `not_needed`, or `failed`",
		"`mechanism`: `repo_native`, `repo_compose`, `repo_script`, `repo_task_runner`, or `generated_fallback`",
		"missing local tooling",
		"unsupported repository setup",
		"startup failure",
		"readiness or connection failure",
	} {
		if !strings.Contains(text, snippet) {
			t.Fatalf("skill missing %q", snippet)
		}
	}
}

func TestIssueImplementationSkillsReferenceLocalServiceDependencySkill(t *testing.T) {
	for _, name := range []string{
		VigilanteIssueImplementationOnMonorepo,
		VigilanteIssueImplementationOnTurborepo,
		VigilanteIssueImplementationOnNx,
		VigilanteIssueImplementationOnRush,
		VigilanteIssueImplementationOnBazel,
		VigilanteIssueImplementationOnGradle,
		VigilanteIssueImplementationOnGradleMultiProject,
		VigilanteIssueImplementationOnBazelMonorepo,
	} {
		body, err := os.ReadFile(repoSkillPath(name))
		if err != nil {
			t.Fatal(err)
		}
		text := string(body)
		if !strings.Contains(text, VigilanteLocalServiceDependencies) {
			t.Fatalf("%s does not mention %s", name, VigilanteLocalServiceDependencies)
		}
		if !strings.Contains(text, DockerComposeLaunch) {
			t.Fatalf("%s does not mention %s", name, DockerComposeLaunch)
		}
	}
}

func TestDockerComposeLaunchSkillDocumentsSharedContract(t *testing.T) {
	body, err := os.ReadFile(repoSkillPath(DockerComposeLaunch))
	if err != nil {
		t.Fatal(err)
	}

	text := string(body)
	for _, snippet := range []string{
		"assigned worktree",
		"`required`: `true` or `false`",
		"`service_types`: one or more of `mysql`, `mariadb`, `postgres`, or `mongodb`",
		"`status`: `ready`, `not_needed`, or `failed`",
		"`connection`",
		"`cleanup`",
	} {
		if !strings.Contains(text, snippet) {
			t.Fatalf("skill missing %q", snippet)
		}
	}
}

func TestGradleMultiProjectSkillCoversSubprojectValidationAndComposeLaunch(t *testing.T) {
	body, err := os.ReadFile(repoSkillPath(VigilanteIssueImplementationOnGradleMultiProject))
	if err != nil {
		t.Fatal(err)
	}

	text := string(body)
	for _, snippet := range []string{
		"Use `settings.gradle` or `settings.gradle.kts`",
		"identify the relevant Gradle subproject scope",
		"Prefer repo-defined Gradle tasks",
		"Keep implementation and validation scoped to the affected subproject(s)",
		"`docker-compose-launch`",
		"Avoid JS workspace assumptions",
		"Log the selected subproject(s) and Gradle task scope",
	} {
		if !strings.Contains(text, snippet) {
			t.Fatalf("skill missing %q", snippet)
		}
	}
}

func TestBazelMonorepoSkillCoversTargetSelectionAndDatabaseFlows(t *testing.T) {
	body, err := os.ReadFile(repoSkillPath(VigilanteIssueImplementationOnBazelMonorepo))
	if err != nil {
		t.Fatal(err)
	}

	text := string(body)
	for _, snippet := range []string{
		"Work in terms of Bazel packages and targets",
		"Choose the smallest explainable Bazel target scope",
		"Log which Bazel target or package scope you selected and why.",
		"`docker-compose-launch`",
		VigilanteLocalServiceDependencies,
	} {
		if !strings.Contains(text, snippet) {
			t.Fatalf("skill missing %q", snippet)
		}
	}
}
