package skill

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	ghcli "github.com/nicobistolfi/vigilante/internal/github"
	"github.com/nicobistolfi/vigilante/internal/state"
)

const VigilanteIssueImplementation = "vigilante-issue-implementation"
const VigilanteConflictResolution = "vigilante-conflict-resolution"

func EnsureInstalled(codexHome string) error {
	for _, name := range []string{VigilanteIssueImplementation, VigilanteConflictResolution} {
		skillDir := filepath.Join(codexHome, "skills", name)
		sourceDir := repoSkillDir(name)
		if _, err := os.Stat(filepath.Join(sourceDir, "SKILL.md")); err != nil {
			return err
		}
		if err := os.RemoveAll(skillDir); err != nil {
			return err
		}
		if err := copyDir(sourceDir, skillDir); err != nil {
			return err
		}
	}
	return nil
}

func BuildIssuePrompt(target state.WatchTarget, issue ghcli.Issue, session state.Session) string {
	lines := []string{
		fmt.Sprintf("Use the `%s` skill for this task.", VigilanteIssueImplementation),
		fmt.Sprintf("Repository: %s", target.Repo),
		fmt.Sprintf("Local repository path: %s", target.Path),
		fmt.Sprintf("Issue: #%d - %s", issue.Number, issue.Title),
		fmt.Sprintf("Issue URL: %s", issue.URL),
		fmt.Sprintf("Worktree path: %s", session.WorktreePath),
		fmt.Sprintf("Branch: %s", session.Branch),
		"Use `gh issue comment` to comment on the issue when you start working, post a concise implementation plan before substantial coding, add milestone progress comments as you make progress, comment again when the PR is opened, push the branch, open a pull request, and report any execution failure back to the issue.",
		"Use the issue as the source of truth for the requested behavior and keep the implementation minimal.",
	}
	return strings.Join(lines, "\n")
}

func BuildConflictResolutionPrompt(target state.WatchTarget, session state.Session, pr ghcli.PullRequest) string {
	lines := []string{
		fmt.Sprintf("Use the `%s` skill for this task.", VigilanteConflictResolution),
		fmt.Sprintf("Repository: %s", target.Repo),
		fmt.Sprintf("Local repository path: %s", target.Path),
		fmt.Sprintf("Issue: #%d - %s", session.IssueNumber, session.IssueTitle),
		fmt.Sprintf("Issue URL: %s", session.IssueURL),
		fmt.Sprintf("Pull Request: #%d", pr.Number),
		fmt.Sprintf("Pull Request URL: %s", pr.URL),
		fmt.Sprintf("Worktree path: %s", session.WorktreePath),
		fmt.Sprintf("Branch: %s", session.Branch),
		"Base branch: origin/main",
		"Resolve the current rebase conflicts in the assigned worktree, use `gh issue comment` for progress and failures, rerun `go test ./...` after conflict resolution if the rebase succeeds, and push the updated branch when finished.",
		"Keep the changes minimal and focused on getting the PR back to a merge-ready state.",
	}
	return strings.Join(lines, "\n")
}

func repoSkillPath(name string) string {
	return filepath.Join(repoRoot(), "skills", name, "SKILL.md")
}

func repoSkillDir(name string) string {
	return filepath.Join(repoRoot(), "skills", name)
}

func repoRoot() string {
	exe, err := os.Executable()
	if err == nil {
		if root, ok := findRepoRoot(filepath.Dir(exe)); ok {
			return root
		}
	}

	wd, err := os.Getwd()
	if err == nil {
		if root, ok := findRepoRoot(wd); ok {
			return root
		}
	}

	return "."
}

func findRepoRoot(start string) (string, bool) {
	dir := start
	for {
		if _, err := os.Stat(filepath.Join(dir, "skills")); err == nil {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

func copyDir(src string, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode())
		}
		return copyFile(path, target, info.Mode())
	})
}

func copyFile(src string, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}
