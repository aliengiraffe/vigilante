package skill

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	skillassets "github.com/nicobistolfi/vigilante"
	ghcli "github.com/nicobistolfi/vigilante/internal/github"
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

	if err := EnsureInstalled(dir); err != nil {
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

	if err := EnsureInstalled(dir); err != nil {
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
	for _, text := range []string{"Use the `vigilante-issue-implementation` skill", "Issue: #12 - Fix bug", "Worktree path: /tmp/worktree", "gh issue comment", "implementation plan", "open a pull request", "Coding Agent Launched: Codex", "10-cell progress bar", "ETA: ~N minutes"} {
		if !strings.Contains(prompt, text) {
			t.Fatalf("prompt missing %q: %s", text, prompt)
		}
	}
}

func TestBuildIssuePromptUsesTurborepoSkillWhenMarkersPresent(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "turbo.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "pnpm-workspace.yaml"), []byte("packages:\n  - apps/*\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	target := state.WatchTarget{Path: "/tmp/repo", Repo: "owner/repo"}
	issue := ghcli.Issue{Number: 12, Title: "Fix bug", URL: "https://example.com/issues/12"}
	session := state.Session{WorktreePath: root, Branch: "vigilante/issue-12", Provider: "Codex"}
	prompt := BuildIssuePrompt(target, issue, session)
	if !strings.Contains(prompt, "Use the `turborepo-issue-implementation` skill") {
		t.Fatalf("expected turborepo issue skill, got: %s", prompt)
	}
}

func TestSelectIssueImplementationSkillDetectsPackageJSONWorkspaces(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "turbo.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "package.json"), []byte("{\"workspaces\":[\"apps/*\",\"packages/*\"]}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if got := SelectIssueImplementationSkill(root, ""); got != TurborepoIssueImplementation {
		t.Fatalf("got %s want %s", got, TurborepoIssueImplementation)
	}
}

func TestSelectIssueImplementationSkillFallsBackWhenMarkersAbsent(t *testing.T) {
	root := t.TempDir()
	if got := SelectIssueImplementationSkill(root, ""); got != VigilanteIssueImplementation {
		t.Fatalf("got %s want %s", got, VigilanteIssueImplementation)
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

func TestEmbeddedTurborepoSkillDocumentsScopedValidationAndDatabaseLaunch(t *testing.T) {
	body, err := fs.ReadFile(skillassets.Skills, "skills/turborepo-issue-implementation/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, snippet := range []string{"smallest relevant workspace", "pnpm --filter <workspace> test", "turbo run build --filter <workspace>", "docker-compose-launch"} {
		if !strings.Contains(text, snippet) {
			t.Fatalf("embedded Turborepo skill missing %q: %s", snippet, text)
		}
	}
}
