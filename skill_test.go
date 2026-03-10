package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureSkillInstalled(t *testing.T) {
	dir := t.TempDir()
	if err := EnsureSkillInstalled(dir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "skills", vigilanteSkillName, "SKILL.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "Comment on the GitHub issue") {
		t.Fatalf("unexpected skill body: %s", string(data))
	}
}

func TestBuildIssuePrompt(t *testing.T) {
	target := WatchTarget{Path: "/tmp/repo", Repo: "owner/repo"}
	issue := GitHubIssue{Number: 12, Title: "Fix bug", URL: "https://example.com/issues/12"}
	session := Session{WorktreePath: "/tmp/worktree", Branch: "vigilante/issue-12"}
	prompt := BuildIssuePrompt(target, issue, session)
	for _, text := range []string{"Use the `vigilante-issue-implementation` skill", "Issue: #12 - Fix bug", "Worktree path: /tmp/worktree"} {
		if !strings.Contains(prompt, text) {
			t.Fatalf("prompt missing %q: %s", text, prompt)
		}
	}
}
