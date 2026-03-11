package skill

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	skillassets "github.com/nicobistolfi/vigilante"
	ghcli "github.com/nicobistolfi/vigilante/internal/github"
	"github.com/nicobistolfi/vigilante/internal/state"
)

const VigilanteIssueImplementation = "vigilante-issue-implementation"
const VigilanteConflictResolution = "vigilante-conflict-resolution"

func EnsureInstalled(codexHome string) error {
	for _, name := range []string{VigilanteIssueImplementation, VigilanteConflictResolution} {
		skillDir := filepath.Join(codexHome, "skills", name)
		source, err := resolveSkillSource(name)
		if err != nil {
			return err
		}
		if err := os.RemoveAll(skillDir); err != nil {
			return err
		}
		if err := source.install(skillDir); err != nil {
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
		"Use the same GitHub comment structure for every non-terminal milestone comment: a short header with the current stage and optional emoji, a 10-cell progress bar with percentage, an `ETA: ~N minutes` line, 1-3 concise bullets covering what just happened and what is next, and an optional short playful quote or tagline.",
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

type skillSource interface {
	install(dst string) error
}

type dirSkillSource string

func (s dirSkillSource) install(dst string) error {
	return copyDir(string(s), dst)
}

type embeddedSkillSource struct {
	root string
	fs   fs.FS
}

func (s embeddedSkillSource) install(dst string) error {
	return copyFS(s.fs, s.root, dst)
}

func resolveSkillSource(name string) (skillSource, error) {
	sourceDir := repoSkillDir(name)
	if _, err := os.Stat(filepath.Join(sourceDir, "SKILL.md")); err == nil {
		return dirSkillSource(sourceDir), nil
	}

	root := filepath.ToSlash(filepath.Join("skills", name))
	if _, err := fs.Stat(skillassets.Skills, pathJoin(root, "SKILL.md")); err != nil {
		return nil, err
	}
	return embeddedSkillSource{root: root, fs: skillassets.Skills}, nil
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

func copyFS(source fs.FS, root string, dst string) error {
	return fs.WalkDir(source, root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		rel, err := filepath.Rel(filepath.FromSlash(root), filepath.FromSlash(path))
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}

		info, err := entry.Info()
		if err != nil {
			return err
		}
		return copyFSFile(source, path, target, info.Mode())
	})
}

func copyFSFile(source fs.FS, src string, dst string, mode os.FileMode) error {
	in, err := source.Open(src)
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

func pathJoin(parts ...string) string {
	return strings.Join(parts, "/")
}
