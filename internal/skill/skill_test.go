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

func TestBuildIssuePromptSelectsMonorepoSkill(t *testing.T) {
	target := state.WatchTarget{
		Path: "/tmp/repo",
		Repo: "owner/repo",
		Classification: repo.Classification{
			Shape: repo.ShapeMonorepo,
			ProcessHints: repo.ProcessHints{
				WorkspaceConfigFiles: []string{"pnpm-workspace.yaml"},
				MultiPackageRoots:    []string{"apps", "packages"},
			},
		},
	}
	issue := ghcli.Issue{Number: 12, Title: "Fix bug", URL: "https://example.com/issues/12"}
	session := state.Session{WorktreePath: "/tmp/worktree", Branch: "vigilante/issue-12", Provider: "Codex"}

	prompt := BuildIssuePrompt(target, issue, session)

	for _, text := range []string{
		"Use the `vigilante-issue-implementation-on-monorepo` skill",
		"Detected repo shape: monorepo",
		"Selected issue implementation skill: vigilante-issue-implementation-on-monorepo",
		`"workspace_config_files":["pnpm-workspace.yaml"]`,
		`"multi_package_roots":["apps","packages"]`,
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
