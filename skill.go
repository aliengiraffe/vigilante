package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const vigilanteSkillName = "vigilante-issue-implementation"

const vigilanteSkillBody = `# Vigilante Issue Implementation

Use this skill when Vigilante launches Codex for a GitHub issue.

## Required Behavior

1. Comment on the GitHub issue when the session begins.
2. Continue posting concise progress comments at meaningful milestones.
3. If work fails or is blocked, comment on the issue with the failure details.
4. Implement the issue in the provided worktree and keep changes scoped to the issue.
5. Prefer repository-native tooling and avoid unnecessary dependencies.
`

func EnsureSkillInstalled(codexHome string) error {
	skillDir := filepath.Join(codexHome, "skills", vigilanteSkillName)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(vigilanteSkillBody), 0o644)
}

func BuildIssuePrompt(target WatchTarget, issue GitHubIssue, session Session) string {
	lines := []string{
		fmt.Sprintf("Use the `%s` skill for this task.", vigilanteSkillName),
		fmt.Sprintf("Repository: %s", target.Repo),
		fmt.Sprintf("Local repository path: %s", target.Path),
		fmt.Sprintf("Issue: #%d - %s", issue.Number, issue.Title),
		fmt.Sprintf("Issue URL: %s", issue.URL),
		fmt.Sprintf("Worktree path: %s", session.WorktreePath),
		fmt.Sprintf("Branch: %s", session.Branch),
		"Comment on the issue when you start working, add progress comments as you make meaningful progress, and report any execution failure back to the issue.",
		"Use the issue as the source of truth for the requested behavior and keep the implementation minimal.",
	}
	return strings.Join(lines, "\n")
}
