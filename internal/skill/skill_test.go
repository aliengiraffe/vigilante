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
	for _, name := range []string{VigilanteIssueImplementation, VigilanteConflictResolution} {
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
	for _, name := range []string{VigilanteIssueImplementation, VigilanteConflictResolution} {
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
