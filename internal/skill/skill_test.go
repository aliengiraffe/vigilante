package skill

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	ghcli "github.com/nicobistolfi/vigilante/internal/github"
	"github.com/nicobistolfi/vigilante/internal/state"
)

func TestEnsureInstalled(t *testing.T) {
	dir := t.TempDir()
	repoRoot := t.TempDir()
	skillSourceDir := filepath.Join(repoRoot, "skills", VigilanteIssueImplementation)
	if err := os.MkdirAll(skillSourceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sourceBody := "# repo skill\n"
	if err := os.WriteFile(filepath.Join(skillSourceDir, "SKILL.md"), []byte(sourceBody), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(skillSourceDir, "agents"), 0o755); err != nil {
		t.Fatal(err)
	}
	agentBody := "interface:\n  display_name: test\n"
	if err := os.WriteFile(filepath.Join(skillSourceDir, "agents", "openai.yaml"), []byte(agentBody), 0o644); err != nil {
		t.Fatal(err)
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
	path := filepath.Join(dir, "skills", VigilanteIssueImplementation, "SKILL.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != sourceBody {
		t.Fatalf("unexpected skill body: %s", string(data))
	}
	agentData, err := os.ReadFile(filepath.Join(dir, "skills", VigilanteIssueImplementation, "agents", "openai.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(agentData) != agentBody {
		t.Fatalf("unexpected agent body: %s", string(agentData))
	}
}

func TestBuildIssuePrompt(t *testing.T) {
	target := state.WatchTarget{Path: "/tmp/repo", Repo: "owner/repo"}
	issue := ghcli.Issue{Number: 12, Title: "Fix bug", URL: "https://example.com/issues/12"}
	session := state.Session{WorktreePath: "/tmp/worktree", Branch: "vigilante/issue-12"}
	prompt := BuildIssuePrompt(target, issue, session)
	for _, text := range []string{"Use the `vigilante-issue-implementation` skill", "Issue: #12 - Fix bug", "Worktree path: /tmp/worktree", "gh issue comment", "implementation plan", "open a pull request"} {
		if !strings.Contains(prompt, text) {
			t.Fatalf("prompt missing %q: %s", text, prompt)
		}
	}
}
