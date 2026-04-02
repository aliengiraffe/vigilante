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
	for _, text := range []string{"Use the `vigilante-issue-implementation` skill", "Detected repo shape: traditional", `Repo process context JSON: {"shape":"traditional"}`, "Selected issue implementation skill: vigilante-issue-implementation", "Issue: #12 - Fix bug", "Worktree path: /tmp/worktree", "vigilante gh issue comment", "vigilante commit", "vigilante git push", "vigilante gh pr create", "Closes #12", "Coding Agent Launched: Codex", "@vigilanteai resume", "@vigilanteai cleanup", "issue-comment commands rather than shell commands", "10-cell progress bar", "ETA: ~N minutes", "Use `vigilante commit` for all commit-producing operations", "Do not use `git commit` or GitHub CLI commit flows directly", "preserve the user's existing git author, committer, and signing configuration", "Do not add `Co-authored by:` trailers"} {
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
		"Base branch for comparison: main",
		"Diff summary against `main`: README.md | 2 ++",
		"Continue from the reused branch state",
	} {
		if !strings.Contains(prompt, text) {
			t.Fatalf("prompt missing %q: %s", text, prompt)
		}
	}
}

func TestBuildIssuePromptIncludesIssueBodyAndIterationContext(t *testing.T) {
	target := state.WatchTarget{Path: "/tmp/repo", Repo: "owner/repo"}
	issue := ghcli.Issue{Number: 12, Title: "Fix bug", URL: "https://example.com/issues/12"}
	session := state.Session{
		WorktreePath:           "/tmp/worktree",
		Branch:                 "vigilante/issue-12",
		Provider:               "Codex",
		IssueBody:              "Full issue body here.",
		IterationPromptContext: "Iteration context for this pass:\nPrimary focus comment:\n@vigilanteai tighten the validation path",
	}

	prompt := BuildIssuePrompt(target, issue, session)

	for _, text := range []string{
		"Full issue body:",
		"Full issue body here.",
		"Iteration context for this pass:",
		"@vigilanteai tighten the validation path",
	} {
		if !strings.Contains(prompt, text) {
			t.Fatalf("prompt missing %q: %s", text, prompt)
		}
	}
}

func TestBuildIssuePromptIncludesForkInstructions(t *testing.T) {
	target := state.WatchTarget{Path: "/tmp/repo", Repo: "owner/repo", Branch: "main", ForkMode: true, ForkOwner: "forker", PushRemote: "fork", PushRepo: "forker/repo"}
	issue := ghcli.Issue{Number: 12, Title: "Fix bug", URL: "https://example.com/issues/12"}
	session := state.Session{
		WorktreePath: "/tmp/worktree",
		Branch:       "vigilante/issue-12",
		BaseBranch:   "main",
		Provider:     "Codex",
		ForkMode:     true,
		ForkOwner:    "forker",
		PushRemote:   "fork",
		PushRepo:     "forker/repo",
	}

	prompt := BuildIssuePrompt(target, issue, session)
	for _, text := range []string{
		"Fork mode is enabled for this run.",
		"Push the branch to remote `fork` (repo `forker/repo`) rather than `origin`.",
		"Open the pull request against `owner/repo` with head `forker:vigilante/issue-12` so the PR is cross-repo from the fork.",
		"Benefits",
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

func TestEnsureInstalledIssueImplementationSkillsIncludeLogsTriageGuidance(t *testing.T) {
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

	expectedCommand := "vigilante logs --repo <owner/name> --issue <n>"
	for _, name := range []string{
		VigilanteIssueImplementation,
		VigilanteIssueImplementationOnMonorepo,
		VigilanteIssueImplementationOnTurborepo,
		VigilanteIssueImplementationOnRushMonorepo,
		VigilanteIssueImplementationOnBazelMonorepo,
		VigilanteIssueImplementationOnGradleMultiProject,
	} {
		data, err := os.ReadFile(filepath.Join(dir, "skills", name, "SKILL.md"))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		body := string(data)
		if !strings.Contains(body, expectedCommand) {
			t.Fatalf("%s missing logs triage guidance", name)
		}
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

func TestBuildIssuePromptSelectsRushMonorepoSkill(t *testing.T) {
	target := state.WatchTarget{
		Path: "/tmp/repo",
		Repo: "owner/repo",
		Classification: repo.Classification{
			Shape:         repo.ShapeMonorepo,
			MonorepoStack: repo.MonorepoStackRush,
			ProcessHints: repo.ProcessHints{
				WorkspaceConfigFiles: []string{"rush.json"},
				MultiPackageRoots:    []string{"apps", "packages"},
			},
		},
	}
	issue := ghcli.Issue{Number: 12, Title: "Fix bug", URL: "https://example.com/issues/12"}
	session := state.Session{WorktreePath: "/tmp/worktree", Branch: "vigilante/issue-12", Provider: "Codex"}

	prompt := BuildIssuePrompt(target, issue, session)

	for _, text := range []string{
		"Use the `vigilante-issue-implementation-on-rush-monorepo` skill",
		"Detected repo shape: monorepo",
		"Detected monorepo stack: rush",
		"Selected issue implementation skill: vigilante-issue-implementation-on-rush-monorepo",
		`"monorepo_stack":"rush"`,
		`"implementation_skill":"vigilante-issue-implementation-on-rush-monorepo"`,
		`"workspace_config_files":["rush.json"]`,
		`"multi_package_roots":["apps","packages"]`,
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
		"Detected repo shape: monorepo",
		"Detected monorepo stack: unknown",
		"Selected issue implementation skill: vigilante-issue-implementation-on-bazel-monorepo",
		`"monorepo_stack":"unknown"`,
		`"implementation_skill":"vigilante-issue-implementation-on-bazel-monorepo"`,
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
	session := state.Session{IssueNumber: 12, IssueTitle: "Fix bug", IssueBody: "Preserve the original validation behavior.", IssueURL: "https://example.com/issues/12", BaseBranch: "main", WorktreePath: "/tmp/worktree", Branch: "vigilante/issue-12", BranchDiffSummary: "Keep the error-state form inputs intact."}
	pr := ghcli.PullRequest{Number: 88, URL: "https://example.com/pull/88", Title: "Fix bug", Body: "This PR keeps the original UX behavior.", Mergeable: "CONFLICTING", MergeStateStatus: "DIRTY"}
	prompt := BuildConflictResolutionPrompt(target, session, pr)
	for _, text := range []string{"Use the `vigilante-conflict-resolution` skill", "Issue specification: Preserve the original validation behavior.", "Pull Request title: Fix bug", "GitHub mergeability: mergeable=CONFLICTING mergeStateStatus=DIRTY", "Work through the rebase commit by commit.", "Use `vigilante commit` for all commit-producing operations", "Do not use `git commit` or GitHub CLI commit flows directly", "preserve the user's existing git author, committer, and signing configuration", "Do not add `Co-authored by:` trailers", "vigilante logs --repo <owner/name> --issue <n>", "go test ./...", "Existing branch summary: Keep the error-state form inputs intact."} {
		if !strings.Contains(prompt, text) {
			t.Fatalf("prompt missing %q: %s", text, prompt)
		}
	}
}

func TestBuildCIRemediationPrompt(t *testing.T) {
	target := state.WatchTarget{Path: "/tmp/repo", Repo: "owner/repo"}
	session := state.Session{IssueNumber: 12, IssueTitle: "Fix bug", IssueURL: "https://example.com/issues/12", WorktreePath: "/tmp/worktree", Branch: "vigilante/issue-12"}
	pr := ghcli.PullRequest{Number: 88, URL: "https://example.com/pull/88"}
	prompt := BuildCIRemediationPrompt(target, session, pr, []ghcli.StatusCheckRoll{{Context: "test", Conclusion: "FAILURE"}})
	for _, text := range []string{"Use the `vigilante-issue-implementation` skill", "Pull Request: #88", "CI remediation context", "Failing check: test", "Use `vigilante commit` for all commit-producing operations", "Do not use `git commit` or GitHub CLI commit flows directly", "preserve the user's existing git author, committer, and signing configuration", "Do not add `Co-authored by:` trailers", "do not open a new pull request", "exit with a non-zero status"} {
		if !strings.Contains(prompt, text) {
			t.Fatalf("prompt missing %q: %s", text, prompt)
		}
	}
}

func TestEnsureInstalledCommitProducingSkillsIncludeCommitIdentityPolicy(t *testing.T) {
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

	for _, name := range []string{
		VigilanteIssueImplementation,
		VigilanteIssueImplementationOnMonorepo,
		VigilanteIssueImplementationOnTurborepo,
		VigilanteIssueImplementationOnNx,
		VigilanteIssueImplementationOnRush,
		VigilanteIssueImplementationOnRushMonorepo,
		VigilanteIssueImplementationOnBazel,
		VigilanteIssueImplementationOnGradle,
		VigilanteIssueImplementationOnGradleMultiProject,
		VigilanteIssueImplementationOnBazelMonorepo,
		VigilanteConflictResolution,
	} {
		data, err := os.ReadFile(filepath.Join(dir, "skills", name, "SKILL.md"))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		body := strings.ToLower(string(data))
		for _, text := range []string{
			"preserve the user's existing git author, committer, and signing configuration",
			"do not add `co-authored by:` trailers",
		} {
			if !strings.Contains(body, text) {
				t.Fatalf("%s missing commit identity guidance %q", name, text)
			}
		}
	}
}

func TestEnsureInstalledCommitProducingSkillsRequireVigilanteCommit(t *testing.T) {
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

	for _, name := range []string{
		VigilanteIssueImplementation,
		VigilanteIssueImplementationOnMonorepo,
		VigilanteIssueImplementationOnTurborepo,
		VigilanteIssueImplementationOnNx,
		VigilanteIssueImplementationOnRush,
		VigilanteIssueImplementationOnRushMonorepo,
		VigilanteIssueImplementationOnBazel,
		VigilanteIssueImplementationOnGradle,
		VigilanteIssueImplementationOnGradleMultiProject,
		VigilanteIssueImplementationOnBazelMonorepo,
		VigilanteConflictResolution,
	} {
		data, err := os.ReadFile(filepath.Join(dir, "skills", name, "SKILL.md"))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		body := string(data)
		if !strings.Contains(body, "vigilante commit") {
			t.Fatalf("%s missing vigilante commit requirement", name)
		}
		if !strings.Contains(body, "Do not use `git commit` or GitHub CLI commit flows directly") {
			t.Fatalf("%s missing git commit prohibition", name)
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

func TestEnsureInstalledForGeminiCreatesSkillsAndRemovesLegacyCommands(t *testing.T) {
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
		legacyCommandPath := filepath.Join(dir, "commands", name+".toml")
		if err := os.MkdirAll(filepath.Dir(legacyCommandPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(legacyCommandPath, []byte("prompt = \"legacy\"\n"), 0o644); err != nil {
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
		skillPath := filepath.Join(dir, "skills", name, "SKILL.md")
		if _, err := os.Stat(skillPath); err != nil {
			t.Fatalf("expected %s to exist: %v", skillPath, err)
		}
		legacyCommandPath := filepath.Join(dir, "commands", name+".toml")
		if _, err := os.Stat(legacyCommandPath); !os.IsNotExist(err) {
			t.Fatalf("expected %s to be removed, got: %v", legacyCommandPath, err)
		}
	}
}

func TestBuildIssuePromptForClaudeInlinesSkillInstructions(t *testing.T) {
	target := state.WatchTarget{Path: "/tmp/repo", Repo: "owner/repo"}
	issue := ghcli.Issue{Number: 12, Title: "Fix bug", URL: "https://example.com/issues/12"}
	session := state.Session{WorktreePath: "/tmp/worktree", Branch: "vigilante/issue-12", Provider: "claude"}
	prompt := BuildIssuePromptForRuntime(RuntimeClaude, target, issue, session)
	for _, text := range []string{"Follow these `vigilante-issue-implementation` skill instructions directly", "Coding Agent Launched: Claude Code", "@vigilanteai resume", "@vigilanteai cleanup", "Issue: #12 - Fix bug"} {
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
	for _, text := range []string{"Follow these `vigilante-issue-implementation` skill instructions directly", "Coding Agent Launched: Gemini CLI", "@vigilanteai resume", "@vigilanteai cleanup", "Issue: #12 - Fix bug"} {
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
		repo.MonorepoStackRush:      VigilanteIssueImplementationOnRushMonorepo,
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

func TestIssueImplementationSkillSelectsRushMonorepo(t *testing.T) {
	target := state.WatchTarget{
		Classification: repo.Classification{
			Shape: repo.ShapeMonorepo,
			ProcessHints: repo.ProcessHints{
				WorkspaceConfigFiles: []string{"rush.json"},
			},
		},
	}

	if got := IssueImplementationSkill(target); got != VigilanteIssueImplementationOnRushMonorepo {
		t.Fatalf("unexpected rush issue skill: %s", got)
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
		"Map Vigilante's internal classifications explicitly to GitHub's native issue types: `feature` -> `Feature`, `bug` -> `Bug`, `task` -> `Task`.",
		"Treat the native GitHub issue type as the source of truth whenever the repository supports it.",
	} {
		if !strings.Contains(text, snippet) {
			t.Fatalf("skill missing %q", snippet)
		}
	}
}

func TestVigilanteCreateIssueSkillDocumentsNativeIssueTypeFallback(t *testing.T) {
	body, err := os.ReadFile(repoSkillPath(VigilanteCreateIssue))
	if err != nil {
		t.Fatal(err)
	}

	text := string(body)
	for _, snippet := range []string{
		"If the repository rejects the native `type` field because issue types are unavailable or unsupported, retry issue creation without the native type and make the fallback explicit in the final response.",
		"Do not use labels or issue-body text as the primary type representation when the native issue type is set successfully.",
		"Only include an `Issue Type: ...` line in the issue body when returning a draft without creating the issue, or when native issue types are unavailable and the fallback needs to preserve the classification explicitly.",
		"the native GitHub issue type is used when the issue is created in a repository that supports it",
	} {
		if !strings.Contains(text, snippet) {
			t.Fatalf("skill missing %q", snippet)
		}
	}
}

func TestVigilanteCreateIssueSkillDocumentsNativeIssueRelationships(t *testing.T) {
	body, err := os.ReadFile(repoSkillPath(VigilanteCreateIssue))
	if err != nil {
		t.Fatal(err)
	}

	text := string(body)
	for _, snippet := range []string{
		"When the draft explicitly says the new issue is a follow-up, child, or sub-issue of a specific existing issue, carry that parent issue number through issue creation.",
		"After the base issue is created, attach it as a native GitHub sub-issue of that parent with `vigilante gh api --method POST repos/{owner}/{repo}/issues/{parent_issue_number}/sub_issues -f sub_issue_id={created_issue_id}`.",
		"Only create a native relationship when the parent mapping is explicit and low-ambiguity; incidental issue-number mentions in prose are not enough.",
		"If native sub-issue creation fails or the repository does not support it, keep the created issue, preserve the body text reference, and make the fallback explicit in the final response.",
		"If the native sub-issue relationship request is rejected or unsupported, do not fail the overall issue creation flow; keep the new issue and report that the relationship fell back to body-only text.",
		"Do not infer parent/child issue links from vague wording or unrelated issue references.",
		"explicit follow-up or child relationships are attached as native GitHub sub-issues when the parent issue is clearly identified",
		"ambiguous issue references do not create native parent/child links",
		"the final response says whether the native sub-issue relationship was created or whether issue creation fell back to body-only linking",
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
		"repository-owned `vigilante docker compose` or `docker-compose` flows before generating anything new",
		"Docker Compose is an allowed fallback, not the defining abstraction.",
		"`status`: `ready`, `not_needed`, or `failed`",
		"`mechanism`: `repo_native`, `repo_compose`, `repo_script`, `repo_task_runner`, or `generated_fallback`",
		"`vigilante logs --repo <owner/name> --issue <n>`",
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
		VigilanteIssueImplementationOnRushMonorepo,
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

func TestRushIssueImplementationSkillMentionsRushTargetingAndDockerComposeLaunch(t *testing.T) {
	body, err := os.ReadFile(repoSkillPath(VigilanteIssueImplementationOnRushMonorepo))
	if err != nil {
		t.Fatal(err)
	}

	text := string(body)
	for _, snippet := range []string{
		"`rush.json`",
		"Identify the smallest affected package or app scope first",
		"Prefer Rush-native commands",
		"`docker-compose-launch`",
		"Avoid full-repo validation unless the repository workflow requires it",
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

func TestBuildIssuePromptIncludesSecurityGuidanceForNodeJSRepo(t *testing.T) {
	target := state.WatchTarget{
		Path: "/tmp/repo",
		Repo: "owner/repo",
		Classification: repo.Classification{
			Shape:      repo.ShapeTraditional,
			TechStacks: []repo.TechStack{repo.TechStackNodeJS},
			ProcessHints: repo.ProcessHints{
				NodePackageManagers: []string{"npm"},
				NodeLockFiles:       []string{"package-lock.json"},
				TypeScriptConfigs:   []string{"tsconfig.json"},
			},
		},
	}
	issue := ghcli.Issue{Number: 12, Title: "Fix bug", URL: "https://example.com/issues/12"}
	session := state.Session{WorktreePath: "/tmp/worktree", Branch: "vigilante/issue-12", Provider: "Codex"}

	prompt := BuildIssuePrompt(target, issue, session)

	for _, text := range []string{
		"JS/TS/Node security guidance",
		"Dependency & supply-chain",
		"npm hardening",
		"Runtime security",
		"TypeScript safety",
		"CI/CD & secrets",
		"Static analysis",
		`"tech_stacks":["nodejs"]`,
		`"node_package_managers":["npm"]`,
		`"node_lock_files":["package-lock.json"]`,
		`"typescript_configs":["tsconfig.json"]`,
	} {
		if !strings.Contains(prompt, text) {
			t.Fatalf("prompt missing %q", text)
		}
	}
}

func TestBuildIssuePromptExcludesSecurityGuidanceForNonNodeRepo(t *testing.T) {
	target := state.WatchTarget{Path: "/tmp/repo", Repo: "owner/repo"}
	issue := ghcli.Issue{Number: 12, Title: "Fix bug", URL: "https://example.com/issues/12"}
	session := state.Session{WorktreePath: "/tmp/worktree", Branch: "vigilante/issue-12", Provider: "Codex"}

	prompt := BuildIssuePrompt(target, issue, session)

	if strings.Contains(prompt, "JS/TS/Node security guidance") {
		t.Fatalf("prompt should not include JS security guidance for non-Node repo")
	}
}

func TestBuildIssuePromptIncludesSecurityGuidanceForMonorepoNodeJS(t *testing.T) {
	target := state.WatchTarget{
		Path: "/tmp/repo",
		Repo: "owner/repo",
		Classification: repo.Classification{
			Shape:         repo.ShapeMonorepo,
			MonorepoStack: repo.MonorepoStackTurborepo,
			TechStacks:    []repo.TechStack{repo.TechStackNodeJS},
			ProcessHints: repo.ProcessHints{
				WorkspaceConfigFiles: []string{"pnpm-workspace.yaml", "turbo.json"},
				MultiPackageRoots:    []string{"apps", "packages"},
				NodePackageManagers:  []string{"pnpm"},
				NodeLockFiles:        []string{"pnpm-lock.yaml"},
			},
		},
	}
	issue := ghcli.Issue{Number: 12, Title: "Fix bug", URL: "https://example.com/issues/12"}
	session := state.Session{WorktreePath: "/tmp/worktree", Branch: "vigilante/issue-12", Provider: "Codex"}

	prompt := BuildIssuePrompt(target, issue, session)

	for _, text := range []string{
		"JS/TS/Node security guidance",
		"pnpm hardening",
		"Monorepo security",
		"phantom dependencies",
	} {
		if !strings.Contains(prompt, text) {
			t.Fatalf("prompt missing %q", text)
		}
	}
}

func TestBuildIssueCreatePromptContainsExpectedFields(t *testing.T) {
	target := state.WatchTarget{
		Repo:         "owner/repo",
		Path:         "/home/user/repo",
		IssueBackend: "github",
		ProjectRef:   "owner/repo",
	}
	prompt := "add dark mode support"

	for _, runtime := range []string{RuntimeCodex, RuntimeClaude, RuntimeGemini} {
		t.Run(runtime, func(t *testing.T) {
			result := BuildIssueCreatePrompt(runtime, target, prompt)
			for _, want := range []string{
				"owner/repo",
				"/home/user/repo",
				"github",
				"add dark mode support",
				VigilanteCreateIssue,
			} {
				if !strings.Contains(result, want) {
					t.Fatalf("expected prompt for runtime %s to contain %q", runtime, want)
				}
			}
		})
	}
}

func TestBuildIssueCreatePromptUsesInlineHeaderForClaudeAndGemini(t *testing.T) {
	target := state.WatchTarget{
		Repo: "owner/repo",
		Path: "/tmp/repo",
	}
	for _, runtime := range []string{RuntimeClaude, RuntimeGemini} {
		t.Run(runtime, func(t *testing.T) {
			result := BuildIssueCreatePrompt(runtime, target, "test")
			if !strings.Contains(result, "Follow these") {
				t.Fatalf("expected inline skill header for %s", runtime)
			}
		})
	}

	result := BuildIssueCreatePrompt(RuntimeCodex, target, "test")
	if !strings.Contains(result, "Use the `vigilante-create-issue` skill") {
		t.Fatalf("expected codex to reference skill by name, got: %s", result[:200])
	}
}

func TestBuildIssueCreatePromptDefaultUsesCodexRuntime(t *testing.T) {
	target := state.WatchTarget{
		Repo: "owner/repo",
		Path: "/tmp/repo",
	}
	result := BuildIssueCreatePromptDefault(target, "test prompt")
	if !strings.Contains(result, "Use the `vigilante-create-issue` skill") {
		t.Fatalf("expected default to use codex runtime")
	}
}

func TestIssueImplementationSkillSelectsGoForGoRepo(t *testing.T) {
	target := state.WatchTarget{
		Classification: repo.Classification{
			Shape:      repo.ShapeTraditional,
			TechStacks: []repo.TechStack{repo.TechStackGo},
		},
	}
	if got := IssueImplementationSkill(target); got != VigilanteIssueImplementationOnGo {
		t.Fatalf("expected go skill, got %s", got)
	}
}

func TestIssueImplementationSkillSelectsPythonForPythonRepo(t *testing.T) {
	target := state.WatchTarget{
		Classification: repo.Classification{
			Shape:      repo.ShapeTraditional,
			TechStacks: []repo.TechStack{repo.TechStackPython},
		},
	}
	if got := IssueImplementationSkill(target); got != VigilanteIssueImplementationOnPython {
		t.Fatalf("expected python skill, got %s", got)
	}
}

func TestIssueImplementationSkillFallsBackForNonGoTraditionalRepo(t *testing.T) {
	target := state.WatchTarget{
		Classification: repo.Classification{
			Shape: repo.ShapeTraditional,
		},
	}
	if got := IssueImplementationSkill(target); got != VigilanteIssueImplementation {
		t.Fatalf("expected default traditional skill, got %s", got)
	}
}

func TestIssueImplementationSkillPrefersMonorepoOverGo(t *testing.T) {
	target := state.WatchTarget{
		Classification: repo.Classification{
			Shape:         repo.ShapeMonorepo,
			MonorepoStack: repo.MonorepoStackUnknown,
			TechStacks:    []repo.TechStack{repo.TechStackGo},
		},
	}
	if got := IssueImplementationSkill(target); got != VigilanteIssueImplementationOnMonorepo {
		t.Fatalf("expected monorepo skill for Go monorepo, got %s", got)
	}
}

func TestBuildIssuePromptForGoRepoIncludesGoSecurityGuidance(t *testing.T) {
	target := state.WatchTarget{
		Repo: "owner/repo",
		Path: "/tmp/repo",
		Classification: repo.Classification{
			Shape:      repo.ShapeTraditional,
			TechStacks: []repo.TechStack{repo.TechStackGo},
		},
	}
	issue := ghcli.Issue{Number: 1, Title: "test", URL: "https://github.com/owner/repo/issues/1"}
	session := state.Session{WorktreePath: "/tmp/wt", Branch: "test-branch"}

	prompt := BuildIssuePromptForRuntime(RuntimeClaude, target, issue, session)

	for _, text := range []string{
		"Go security and tooling guidance",
		"gofmt",
		"go test",
		"go vet",
		"govulncheck",
		"crypto/rand",
		"do not broaden issue scope",
	} {
		if !strings.Contains(prompt, text) {
			t.Fatalf("prompt missing %q", text)
		}
	}
}

func TestBuildIssuePromptForGoRepoSelectsGoSkill(t *testing.T) {
	target := state.WatchTarget{
		Repo: "owner/repo",
		Path: "/tmp/repo",
		Classification: repo.Classification{
			Shape:      repo.ShapeTraditional,
			TechStacks: []repo.TechStack{repo.TechStackGo},
		},
	}
	issue := ghcli.Issue{Number: 1, Title: "test", URL: "https://github.com/owner/repo/issues/1"}
	session := state.Session{WorktreePath: "/tmp/wt", Branch: "test-branch"}

	prompt := BuildIssuePromptForRuntime(RuntimeClaude, target, issue, session)

	if !strings.Contains(prompt, VigilanteIssueImplementationOnGo) {
		t.Fatalf("prompt should reference Go skill, got: %s", prompt[:300])
	}
}

func TestBuildIssuePromptForPythonRepoIncludesPythonGuidance(t *testing.T) {
	target := state.WatchTarget{
		Repo: "owner/repo",
		Path: "/tmp/repo",
		Classification: repo.Classification{
			Shape:      repo.ShapeTraditional,
			TechStacks: []repo.TechStack{repo.TechStackPython},
			ProcessHints: repo.ProcessHints{
				PythonSignals: []string{"pyproject.toml"},
			},
		},
	}
	issue := ghcli.Issue{Number: 1, Title: "test", URL: "https://github.com/owner/repo/issues/1"}
	session := state.Session{WorktreePath: "/tmp/wt", Branch: "test-branch"}

	prompt := BuildIssuePromptForRuntime(RuntimeClaude, target, issue, session)

	for _, text := range []string{
		VigilanteIssueImplementationOnPython,
		`"tech_stacks":["python"]`,
		"Python security and tooling guidance",
		"python -m venv .venv",
		"ruff format",
		"pytest",
		"pip-audit",
		"secrets",
	} {
		if !strings.Contains(prompt, text) {
			t.Fatalf("prompt missing %q", text)
		}
	}
}

func TestBuildIssuePromptForNonPythonRepoDoesNotIncludePythonGuidance(t *testing.T) {
	target := state.WatchTarget{
		Repo: "owner/repo",
		Path: "/tmp/repo",
		Classification: repo.Classification{
			Shape:      repo.ShapeTraditional,
			TechStacks: []repo.TechStack{repo.TechStackGo},
		},
	}
	issue := ghcli.Issue{Number: 1, Title: "test", URL: "https://github.com/owner/repo/issues/1"}
	session := state.Session{WorktreePath: "/tmp/wt", Branch: "test-branch"}

	prompt := BuildIssuePromptForRuntime(RuntimeClaude, target, issue, session)

	if strings.Contains(prompt, "Python security and tooling guidance") {
		t.Fatalf("prompt should not include Python guidance for non-Python repo")
	}
}

func TestBuildIssuePromptForNonGoRepoDoesNotIncludeGoGuidance(t *testing.T) {
	target := state.WatchTarget{
		Repo: "owner/repo",
		Path: "/tmp/repo",
		Classification: repo.Classification{
			Shape: repo.ShapeTraditional,
		},
	}
	issue := ghcli.Issue{Number: 1, Title: "test", URL: "https://github.com/owner/repo/issues/1"}
	session := state.Session{WorktreePath: "/tmp/wt", Branch: "test-branch"}

	prompt := BuildIssuePromptForRuntime(RuntimeClaude, target, issue, session)

	if strings.Contains(prompt, "Go security and tooling guidance") {
		t.Fatalf("prompt should not include Go guidance for non-Go repo")
	}
}

func TestBuildIssuePromptForGoMonorepoIncludesGoGuidance(t *testing.T) {
	target := state.WatchTarget{
		Repo: "owner/repo",
		Path: "/tmp/repo",
		Classification: repo.Classification{
			Shape:         repo.ShapeMonorepo,
			MonorepoStack: repo.MonorepoStackTurborepo,
			TechStacks:    []repo.TechStack{repo.TechStackGo, repo.TechStackNodeJS},
		},
	}
	issue := ghcli.Issue{Number: 1, Title: "test", URL: "https://github.com/owner/repo/issues/1"}
	session := state.Session{WorktreePath: "/tmp/wt", Branch: "test-branch"}

	prompt := BuildIssuePromptForRuntime(RuntimeClaude, target, issue, session)

	for _, text := range []string{
		"Go security and tooling guidance",
		"gofmt",
		"go test",
		"go vet",
		"Mixed-language scope",
	} {
		if !strings.Contains(prompt, text) {
			t.Fatalf("prompt missing %q for Go monorepo", text)
		}
	}
	if !strings.Contains(prompt, "JS/TS/Node security guidance") {
		t.Fatalf("prompt missing Node.js guidance for Go+Node monorepo")
	}
}

func TestBuildIssuePromptForGoAndNodeJSTraditionalRepoIncludesMixedGuidance(t *testing.T) {
	target := state.WatchTarget{
		Repo: "owner/repo",
		Path: "/tmp/repo",
		Classification: repo.Classification{
			Shape:      repo.ShapeTraditional,
			TechStacks: []repo.TechStack{repo.TechStackGo, repo.TechStackNodeJS},
		},
	}
	issue := ghcli.Issue{Number: 1, Title: "test", URL: "https://github.com/owner/repo/issues/1"}
	session := state.Session{WorktreePath: "/tmp/wt", Branch: "test-branch"}

	prompt := BuildIssuePromptForRuntime(RuntimeClaude, target, issue, session)

	if !strings.Contains(prompt, "Go security and tooling guidance") {
		t.Fatalf("prompt missing Go guidance")
	}
	if !strings.Contains(prompt, "JS/TS/Node security guidance") {
		t.Fatalf("prompt missing Node.js guidance")
	}
	if !strings.Contains(prompt, "Mixed-language scope") {
		t.Fatalf("prompt missing mixed-language guidance for Go+Node.js traditional repo")
	}
	if !strings.Contains(prompt, VigilanteIssueImplementationOnGo) {
		t.Fatalf("prompt should select Go skill for traditional Go+Node.js repo")
	}
}

func TestBuildIssuePromptForGoOnlyTraditionalRepoOmitsMixedGuidance(t *testing.T) {
	target := state.WatchTarget{
		Repo: "owner/repo",
		Path: "/tmp/repo",
		Classification: repo.Classification{
			Shape:      repo.ShapeTraditional,
			TechStacks: []repo.TechStack{repo.TechStackGo},
		},
	}
	issue := ghcli.Issue{Number: 1, Title: "test", URL: "https://github.com/owner/repo/issues/1"}
	session := state.Session{WorktreePath: "/tmp/wt", Branch: "test-branch"}

	prompt := BuildIssuePromptForRuntime(RuntimeClaude, target, issue, session)

	if strings.Contains(prompt, "Mixed-language scope") {
		t.Fatalf("prompt should not include mixed-language guidance for Go-only traditional repo")
	}
}

func TestIssueImplementationSkillSelectsGitHubActionsForActionsRepo(t *testing.T) {
	target := state.WatchTarget{
		Classification: repo.Classification{
			Shape:      repo.ShapeTraditional,
			TechStacks: []repo.TechStack{repo.TechStackGitHubActions},
		},
	}
	if got := IssueImplementationSkill(target); got != VigilanteIssueImplementationOnGitHubActions {
		t.Fatalf("expected github-actions skill, got %s", got)
	}
}

func TestIssueImplementationSkillPrefersGoOverGitHubActions(t *testing.T) {
	target := state.WatchTarget{
		Classification: repo.Classification{
			Shape:      repo.ShapeTraditional,
			TechStacks: []repo.TechStack{repo.TechStackGo, repo.TechStackGitHubActions},
		},
	}
	if got := IssueImplementationSkill(target); got != VigilanteIssueImplementationOnGo {
		t.Fatalf("expected go skill to take priority over github-actions, got %s", got)
	}
}

func TestIssueImplementationSkillPrefersPythonOverGitHubActions(t *testing.T) {
	target := state.WatchTarget{
		Classification: repo.Classification{
			Shape:      repo.ShapeTraditional,
			TechStacks: []repo.TechStack{repo.TechStackPython, repo.TechStackGitHubActions},
		},
	}
	if got := IssueImplementationSkill(target); got != VigilanteIssueImplementationOnPython {
		t.Fatalf("expected python skill to take priority over github-actions, got %s", got)
	}
}
func TestIssueImplementationSkillPrefersMonorepoOverGitHubActions(t *testing.T) {
	target := state.WatchTarget{
		Classification: repo.Classification{
			Shape:         repo.ShapeMonorepo,
			MonorepoStack: repo.MonorepoStackUnknown,
			TechStacks:    []repo.TechStack{repo.TechStackGitHubActions},
		},
	}
	if got := IssueImplementationSkill(target); got != VigilanteIssueImplementationOnMonorepo {
		t.Fatalf("expected monorepo skill for monorepo with actions, got %s", got)
	}
}

func TestBuildIssuePromptForGitHubActionsRepoIncludesActionsGuidance(t *testing.T) {
	target := state.WatchTarget{
		Repo: "owner/repo",
		Path: "/tmp/repo",
		Classification: repo.Classification{
			Shape:      repo.ShapeTraditional,
			TechStacks: []repo.TechStack{repo.TechStackGitHubActions},
		},
	}
	issue := ghcli.Issue{Number: 1, Title: "test", URL: "https://github.com/owner/repo/issues/1"}
	session := state.Session{WorktreePath: "/tmp/wt", Branch: "test-branch"}

	prompt := BuildIssuePromptForRuntime(RuntimeClaude, target, issue, session)

	for _, text := range []string{
		"GitHub Actions workflow security guidance",
		"Pinned actions",
		"Least-privilege permissions",
		"Secret handling",
		"Injection prevention",
		"OIDC authentication",
		"actionlint",
		"do not broaden issue scope",
	} {
		if !strings.Contains(prompt, text) {
			t.Fatalf("prompt missing %q", text)
		}
	}
}

func TestBuildIssuePromptForGitHubActionsRepoSelectsActionsSkill(t *testing.T) {
	target := state.WatchTarget{
		Repo: "owner/repo",
		Path: "/tmp/repo",
		Classification: repo.Classification{
			Shape:      repo.ShapeTraditional,
			TechStacks: []repo.TechStack{repo.TechStackGitHubActions},
		},
	}
	issue := ghcli.Issue{Number: 1, Title: "test", URL: "https://github.com/owner/repo/issues/1"}
	session := state.Session{WorktreePath: "/tmp/wt", Branch: "test-branch"}

	prompt := BuildIssuePromptForRuntime(RuntimeClaude, target, issue, session)

	if !strings.Contains(prompt, VigilanteIssueImplementationOnGitHubActions) {
		t.Fatalf("prompt should reference GitHub Actions skill")
	}
}

func TestBuildIssuePromptForNonActionsRepoDoesNotIncludeActionsGuidance(t *testing.T) {
	target := state.WatchTarget{
		Repo: "owner/repo",
		Path: "/tmp/repo",
		Classification: repo.Classification{
			Shape: repo.ShapeTraditional,
		},
	}
	issue := ghcli.Issue{Number: 1, Title: "test", URL: "https://github.com/owner/repo/issues/1"}
	session := state.Session{WorktreePath: "/tmp/wt", Branch: "test-branch"}

	prompt := BuildIssuePromptForRuntime(RuntimeClaude, target, issue, session)

	if strings.Contains(prompt, "GitHub Actions workflow security guidance") {
		t.Fatalf("prompt should not include GitHub Actions guidance for non-Actions repo")
	}
}

func TestBuildIssuePromptForGoAndActionsRepoIncludesBothGuidances(t *testing.T) {
	target := state.WatchTarget{
		Repo: "owner/repo",
		Path: "/tmp/repo",
		Classification: repo.Classification{
			Shape:      repo.ShapeTraditional,
			TechStacks: []repo.TechStack{repo.TechStackGo, repo.TechStackGitHubActions},
		},
	}
	issue := ghcli.Issue{Number: 1, Title: "test", URL: "https://github.com/owner/repo/issues/1"}
	session := state.Session{WorktreePath: "/tmp/wt", Branch: "test-branch"}

	prompt := BuildIssuePromptForRuntime(RuntimeClaude, target, issue, session)

	if !strings.Contains(prompt, "Go security and tooling guidance") {
		t.Fatalf("prompt missing Go guidance")
	}
	if !strings.Contains(prompt, "GitHub Actions workflow security guidance") {
		t.Fatalf("prompt missing GitHub Actions guidance")
	}
	if !strings.Contains(prompt, VigilanteIssueImplementationOnGo) {
		t.Fatalf("prompt should select Go skill for Go+Actions repo")
	}
}

func TestIssueImplementationSkillSelectsMonorepoForGoTurborepo(t *testing.T) {
	target := state.WatchTarget{
		Classification: repo.Classification{
			Shape:         repo.ShapeMonorepo,
			MonorepoStack: repo.MonorepoStackTurborepo,
			TechStacks:    []repo.TechStack{repo.TechStackGo, repo.TechStackNodeJS},
		},
	}
	if got := IssueImplementationSkill(target); got != VigilanteIssueImplementationOnTurborepo {
		t.Fatalf("expected turborepo skill for Go+Node turborepo, got %s", got)
	}
}

func TestIssueImplementationSkillSelectsDockerForDockerOnlyRepo(t *testing.T) {
	target := state.WatchTarget{
		Classification: repo.Classification{
			Shape:      repo.ShapeTraditional,
			TechStacks: []repo.TechStack{repo.TechStackDocker},
		},
	}
	if got := IssueImplementationSkill(target); got != VigilanteIssueImplementationOnDocker {
		t.Fatalf("expected docker skill, got %s", got)
	}
}

func TestIssueImplementationSkillPrefersGoOverDockerForGoDockerRepo(t *testing.T) {
	target := state.WatchTarget{
		Classification: repo.Classification{
			Shape:      repo.ShapeTraditional,
			TechStacks: []repo.TechStack{repo.TechStackGo, repo.TechStackDocker},
		},
	}
	if got := IssueImplementationSkill(target); got != VigilanteIssueImplementationOnGo {
		t.Fatalf("expected go skill for Go+Docker repo, got %s", got)
	}
}

func TestIssueImplementationSkillPrefersMonorepoOverDockerForDockerMonorepo(t *testing.T) {
	target := state.WatchTarget{
		Classification: repo.Classification{
			Shape:         repo.ShapeMonorepo,
			MonorepoStack: repo.MonorepoStackTurborepo,
			TechStacks:    []repo.TechStack{repo.TechStackDocker, repo.TechStackNodeJS},
		},
	}
	if got := IssueImplementationSkill(target); got != VigilanteIssueImplementationOnTurborepo {
		t.Fatalf("expected turborepo skill for Docker+Node turborepo, got %s", got)
	}
}

func TestIssueImplementationSkillSelectsKubernetesForK8sRepo(t *testing.T) {
	target := state.WatchTarget{
		Classification: repo.Classification{
			Shape:      repo.ShapeTraditional,
			TechStacks: []repo.TechStack{repo.TechStackKubernetes},
		},
	}
	if got := IssueImplementationSkill(target); got != VigilanteIssueImplementationOnKubernetes {
		t.Fatalf("expected kubernetes skill, got %s", got)
	}
}

func TestIssueImplementationSkillFallsBackForNonK8sRepo(t *testing.T) {
	target := state.WatchTarget{
		Classification: repo.Classification{
			Shape: repo.ShapeTraditional,
		},
	}
	if got := IssueImplementationSkill(target); got != VigilanteIssueImplementation {
		t.Fatalf("expected default skill, got %s", got)
	}
}

func TestIssueImplementationSkillPrefersKubernetesOverGoForK8sRepo(t *testing.T) {
	target := state.WatchTarget{
		Classification: repo.Classification{
			Shape:      repo.ShapeTraditional,
			TechStacks: []repo.TechStack{repo.TechStackKubernetes, repo.TechStackGo},
		},
	}
	if got := IssueImplementationSkill(target); got != VigilanteIssueImplementationOnKubernetes {
		t.Fatalf("expected kubernetes skill over go for K8s+Go repo, got %s", got)
	}
}

func TestIssueImplementationSkillPrefersMonorepoOverKubernetes(t *testing.T) {
	target := state.WatchTarget{
		Classification: repo.Classification{
			Shape:         repo.ShapeMonorepo,
			MonorepoStack: repo.MonorepoStackUnknown,
			TechStacks:    []repo.TechStack{repo.TechStackKubernetes},
		},
	}
	if got := IssueImplementationSkill(target); got != VigilanteIssueImplementationOnMonorepo {
		t.Fatalf("expected monorepo skill for K8s monorepo, got %s", got)
	}
}

func TestBuildIssuePromptForDockerRepoIncludesDockerSecurityGuidance(t *testing.T) {
	target := state.WatchTarget{
		Repo: "owner/repo",
		Path: "/tmp/repo",
		Classification: repo.Classification{
			Shape:      repo.ShapeTraditional,
			TechStacks: []repo.TechStack{repo.TechStackDocker},
		},
	}
	issue := ghcli.Issue{Number: 1, Title: "test", URL: "https://github.com/owner/repo/issues/1"}
	session := state.Session{WorktreePath: "/tmp/wt", Branch: "test-branch"}

	prompt := BuildIssuePromptForRuntime(RuntimeClaude, target, issue, session)

	for _, text := range []string{
		"Docker and container security guidance",
		"pin base images",
		"multi-stage builds",
		"secret mounts",
		"non-root user",
		"do not broaden issue scope",
	} {
		if !strings.Contains(prompt, text) {
			t.Fatalf("prompt missing %q", text)
		}
	}
}

func TestBuildIssuePromptForK8sRepoIncludesK8sSecurityGuidance(t *testing.T) {
	target := state.WatchTarget{
		Repo: "owner/repo",
		Path: "/tmp/repo",
		Classification: repo.Classification{
			Shape:      repo.ShapeTraditional,
			TechStacks: []repo.TechStack{repo.TechStackKubernetes},
		},
	}
	issue := ghcli.Issue{Number: 1, Title: "test", URL: "https://github.com/owner/repo/issues/1"}
	session := state.Session{WorktreePath: "/tmp/wt", Branch: "test-branch"}

	prompt := BuildIssuePromptForRuntime(RuntimeClaude, target, issue, session)

	for _, text := range []string{
		"Kubernetes manifest and workload security guidance",
		"Service accounts",
		"Security context",
		"runAsNonRoot",
		"RBAC",
		"Image security",
		"NetworkPolicy",
		"do not broaden issue scope",
	} {
		if !strings.Contains(prompt, text) {
			t.Fatalf("prompt missing %q", text)
		}
	}
}

func TestBuildIssuePromptForDockerRepoSelectsDockerSkill(t *testing.T) {
	target := state.WatchTarget{
		Repo: "owner/repo",
		Path: "/tmp/repo",
		Classification: repo.Classification{
			Shape:      repo.ShapeTraditional,
			TechStacks: []repo.TechStack{repo.TechStackDocker},
		},
	}
	issue := ghcli.Issue{Number: 1, Title: "test", URL: "https://github.com/owner/repo/issues/1"}
	session := state.Session{WorktreePath: "/tmp/wt", Branch: "test-branch"}

	prompt := BuildIssuePromptForRuntime(RuntimeClaude, target, issue, session)

	if !strings.Contains(prompt, VigilanteIssueImplementationOnDocker) {
		t.Fatalf("prompt should reference Docker skill, got: %s", prompt[:300])
	}
}

func TestBuildIssuePromptForK8sRepoSelectsK8sSkill(t *testing.T) {
	target := state.WatchTarget{
		Repo: "owner/repo",
		Path: "/tmp/repo",
		Classification: repo.Classification{
			Shape:      repo.ShapeTraditional,
			TechStacks: []repo.TechStack{repo.TechStackKubernetes},
		},
	}
	issue := ghcli.Issue{Number: 1, Title: "test", URL: "https://github.com/owner/repo/issues/1"}
	session := state.Session{WorktreePath: "/tmp/wt", Branch: "test-branch"}

	prompt := BuildIssuePromptForRuntime(RuntimeClaude, target, issue, session)

	if !strings.Contains(prompt, VigilanteIssueImplementationOnKubernetes) {
		t.Fatalf("prompt should reference Kubernetes skill")
	}
}

func TestBuildIssuePromptForNonDockerRepoDoesNotIncludeDockerGuidance(t *testing.T) {
	target := state.WatchTarget{
		Repo: "owner/repo",
		Path: "/tmp/repo",
		Classification: repo.Classification{
			Shape: repo.ShapeTraditional,
		},
	}
	issue := ghcli.Issue{Number: 1, Title: "test", URL: "https://github.com/owner/repo/issues/1"}
	session := state.Session{WorktreePath: "/tmp/wt", Branch: "test-branch"}

	prompt := BuildIssuePromptForRuntime(RuntimeClaude, target, issue, session)

	if strings.Contains(prompt, "Docker and container security guidance") {
		t.Fatalf("prompt should not include Docker guidance for non-Docker repo")
	}
}

func TestBuildIssuePromptForNonK8sRepoDoesNotIncludeK8sGuidance(t *testing.T) {
	target := state.WatchTarget{
		Repo: "owner/repo",
		Path: "/tmp/repo",
		Classification: repo.Classification{
			Shape: repo.ShapeTraditional,
		},
	}
	issue := ghcli.Issue{Number: 1, Title: "test", URL: "https://github.com/owner/repo/issues/1"}
	session := state.Session{WorktreePath: "/tmp/wt", Branch: "test-branch"}

	prompt := BuildIssuePromptForRuntime(RuntimeClaude, target, issue, session)

	if strings.Contains(prompt, "Kubernetes manifest and workload security guidance") {
		t.Fatalf("prompt should not include Kubernetes guidance for non-K8s repo")
	}
}

func TestBuildIssuePromptForGoDockerRepoIncludesBothGuidance(t *testing.T) {
	target := state.WatchTarget{
		Repo: "owner/repo",
		Path: "/tmp/repo",
		Classification: repo.Classification{
			Shape:      repo.ShapeTraditional,
			TechStacks: []repo.TechStack{repo.TechStackGo, repo.TechStackDocker},
		},
	}
	issue := ghcli.Issue{Number: 1, Title: "test", URL: "https://github.com/owner/repo/issues/1"}
	session := state.Session{WorktreePath: "/tmp/wt", Branch: "test-branch"}

	prompt := BuildIssuePromptForRuntime(RuntimeClaude, target, issue, session)

	if !strings.Contains(prompt, "Go security and tooling guidance") {
		t.Fatalf("prompt missing Go guidance for Go+Docker repo")
	}
	if !strings.Contains(prompt, "Docker and container security guidance") {
		t.Fatalf("prompt missing Docker guidance for Go+Docker repo")
	}
	if !strings.Contains(prompt, VigilanteIssueImplementationOnGo) {
		t.Fatalf("prompt should select Go skill for Go+Docker repo")
	}
}

func TestDockerSkillIsBundled(t *testing.T) {
	name := VigilanteIssueImplementationOnDocker
	path := "skills/" + name + "/SKILL.md"
	_, err := fs.Stat(skillassets.Skills, path)
	if err != nil {
		t.Fatalf("expected %s to be bundled: %v", name, err)
	}
}

func TestDockerSkillCoversRequiredGuidanceAreas(t *testing.T) {
	body, err := os.ReadFile(repoSkillPath(VigilanteIssueImplementationOnDocker))
	if err != nil {
		t.Fatal(err)
	}

	text := string(body)
	for _, snippet := range []string{
		"pin base images",
		"multi-stage builds",
		"WORKDIR",
		"ARG",
		"ENV",
		"secret mounts",
		".dockerignore",
		"non-root user",
		"vigilante commit",
		"image scanning",
	} {
		if !strings.Contains(text, snippet) {
			t.Fatalf("Docker skill missing required guidance: %q", snippet)
		}
	}
}
