package worktree

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/nicobistolfi/vigilante/internal/environment"
	"github.com/nicobistolfi/vigilante/internal/state"
)

type Worktree struct {
	Path               string
	Branch             string
	ReusedRemoteBranch string
}

var nonAlnumPattern = regexp.MustCompile(`[^a-z0-9]+`)

func CreateIssueWorktree(ctx context.Context, runner environment.Runner, target state.WatchTarget, issueNumber int, issueTitle string) (Worktree, error) {
	branch := IssueBranchName(issueNumber, issueTitle)
	path := IssueWorktreePath(target.Path, issueNumber)
	pushRemote := target.EffectivePushRemote()

	if _, err := runner.Run(ctx, target.Path, "git", "worktree", "prune"); err != nil {
		return Worktree{}, err
	}
	if _, err := os.Stat(path); err == nil {
		// Clean up stale worktree from a previous session so we can
		// re-create it with the latest branch state.
		if err := Remove(ctx, runner, target.Path, path); err != nil {
			return Worktree{}, fmt.Errorf("remove stale worktree for issue #%d: %w", issueNumber, err)
		}
		if _, err := runner.Run(ctx, target.Path, "git", "worktree", "prune"); err != nil {
			return Worktree{}, err
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return Worktree{}, err
	}

	for _, candidate := range IssueBranchCandidates(issueNumber, issueTitle) {
		exists, err := remoteBranchExistsWithError(ctx, runner, target.Path, pushRemote, candidate)
		if err != nil {
			return Worktree{}, err
		}
		if !exists {
			continue
		}
		if _, err := runner.Run(ctx, target.Path, "git", "fetch", pushRemote, candidate+":"+candidate); err != nil {
			return Worktree{}, fmt.Errorf("prepare remote issue branch %q: %w", candidate, err)
		}
		if _, err := runner.Run(ctx, target.Path, "git", "worktree", "add", path, candidate); err != nil {
			return Worktree{}, fmt.Errorf("checkout remote issue branch %q into worktree: %w", candidate, err)
		}
		return Worktree{Path: path, Branch: candidate, ReusedRemoteBranch: candidate}, nil
	}

	for _, candidate := range IssueBranchCandidates(issueNumber, issueTitle) {
		if branchExists(ctx, runner, target.Path, candidate) {
			branch = candidate
			if _, err := runner.Run(ctx, target.Path, "git", "worktree", "add", path, branch); err != nil {
				return Worktree{}, err
			}
			return Worktree{Path: path, Branch: branch}, nil
		}
	}
	if err := refreshBaseBranch(ctx, runner, target.Path, target.Branch); err != nil {
		return Worktree{}, err
	}

	if _, err := runner.Run(ctx, target.Path, "git", "worktree", "add", "-b", branch, path, "origin/"+target.Branch); err != nil {
		return Worktree{}, err
	}
	return Worktree{Path: path, Branch: branch}, nil
}

func refreshBaseBranch(ctx context.Context, runner environment.Runner, repoPath string, branch string) error {
	if _, err := runner.Run(ctx, repoPath, "git", "fetch", "origin", branch); err != nil {
		return err
	}

	attachedPath, err := worktreePathForBranch(ctx, runner, repoPath, branch)
	if err != nil {
		return err
	}
	if attachedPath == "" {
		_, err := runner.Run(ctx, repoPath, "git", "branch", "-f", branch, "refs/remotes/origin/"+branch)
		return err
	}

	status, err := runner.Run(ctx, attachedPath, "git", "status", "--porcelain", "--untracked-files=no")
	if err != nil {
		return err
	}
	if strings.TrimSpace(status) != "" {
		return fmt.Errorf("base branch %q has local changes in worktree %s", branch, attachedPath)
	}

	_, err = runner.Run(ctx, attachedPath, "git", "merge", "--ff-only", "origin/"+branch)
	return err
}

func IssueBranchName(issueNumber int, issueTitle string) string {
	slug := IssueTitleSlug(issueTitle)
	if slug == "" {
		return LegacyIssueBranchName(issueNumber)
	}
	return fmt.Sprintf("%s-%s", LegacyIssueBranchName(issueNumber), slug)
}

func LegacyIssueBranchName(issueNumber int) string {
	return fmt.Sprintf("vigilante/issue-%d", issueNumber)
}

func IssueWorktreePath(repoPath string, issueNumber int) string {
	return filepath.Join(repoPath, ".worktrees", "vigilante", fmt.Sprintf("issue-%d", issueNumber))
}

func IssueBranchCandidates(issueNumber int, issueTitle string) []string {
	primary := IssueBranchName(issueNumber, issueTitle)
	legacy := LegacyIssueBranchName(issueNumber)
	if primary == legacy {
		return []string{legacy}
	}
	return []string{primary, legacy}
}

func IssueTitleSlug(issueTitle string) string {
	normalized := strings.ToLower(issueTitle)
	normalized = nonAlnumPattern.ReplaceAllString(normalized, "-")
	return strings.Trim(normalized, "-")
}

func Remove(ctx context.Context, runner environment.Runner, repoPath string, worktreePath string) error {
	_, err := runner.Run(ctx, repoPath, "git", "worktree", "remove", "--force", worktreePath)
	return err
}

func Prune(ctx context.Context, runner environment.Runner, repoPath string) error {
	_, err := runner.Run(ctx, repoPath, "git", "worktree", "prune")
	return err
}

func CleanupIssueArtifacts(ctx context.Context, runner environment.Runner, repoPath string, worktreePath string, branch string) error {
	return CleanupIssueArtifactsForBranches(ctx, runner, repoPath, worktreePath, []string{branch})
}

func CleanupIssueArtifactsForBranches(ctx context.Context, runner environment.Runner, repoPath string, worktreePath string, branches []string) error {
	if err := Prune(ctx, runner, repoPath); err != nil {
		return err
	}

	if _, err := os.Stat(worktreePath); err == nil {
		if err := Remove(ctx, runner, repoPath, worktreePath); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	if err := Prune(ctx, runner, repoPath); err != nil {
		return err
	}

	seen := map[string]struct{}{}
	for _, branch := range branches {
		branch = strings.TrimSpace(branch)
		if branch == "" {
			continue
		}
		if _, ok := seen[branch]; ok {
			continue
		}
		seen[branch] = struct{}{}

		attached, err := branchAttachedToWorktree(ctx, runner, repoPath, branch)
		if err != nil {
			return err
		}
		if attached {
			continue
		}

		exists, err := branchExistsWithError(ctx, runner, repoPath, branch)
		if err != nil {
			return err
		}
		if !exists {
			continue
		}

		if _, err := runner.Run(ctx, repoPath, "git", "branch", "-D", branch); err != nil {
			return err
		}
	}
	return nil
}

func RecreateBranchWorktree(ctx context.Context, runner environment.Runner, repoPath string, worktreePath string, branch string) error {
	if err := Prune(ctx, runner, repoPath); err != nil {
		return err
	}

	if _, err := os.Stat(worktreePath); err == nil {
		if err := Remove(ctx, runner, repoPath, worktreePath); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	if err := Prune(ctx, runner, repoPath); err != nil {
		return err
	}

	remoteExists, err := remoteBranchExistsWithError(ctx, runner, repoPath, "origin", branch)
	if err != nil {
		return err
	}
	if remoteExists {
		if _, err := runner.Run(ctx, repoPath, "git", "fetch", "origin", branch+":"+branch); err != nil {
			return fmt.Errorf("prepare remote branch %q: %w", branch, err)
		}
	} else {
		exists, err := branchExistsWithError(ctx, runner, repoPath, branch)
		if err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("branch %q not found locally or on origin", branch)
		}
	}

	attached, err := branchAttachedToWorktree(ctx, runner, repoPath, branch)
	if err != nil {
		return err
	}
	if attached {
		return fmt.Errorf("branch %q is already attached to another worktree", branch)
	}

	_, err = runner.Run(ctx, repoPath, "git", "worktree", "add", worktreePath, branch)
	return err
}

func branchExists(ctx context.Context, runner environment.Runner, repoPath string, branch string) bool {
	_, err := runner.Run(ctx, repoPath, "git", "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	return err == nil
}

func branchExistsWithError(ctx context.Context, runner environment.Runner, repoPath string, branch string) (bool, error) {
	_, err := runner.Run(ctx, repoPath, "git", "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	if err == nil {
		return true, nil
	}
	if strings.Contains(err.Error(), "exit status 1") {
		return false, nil
	}
	return false, err
}

func remoteBranchExistsWithError(ctx context.Context, runner environment.Runner, repoPath string, remote string, branch string) (bool, error) {
	_, err := runner.Run(ctx, repoPath, "git", "ls-remote", "--exit-code", "--heads", remote, branch)
	if err == nil {
		return true, nil
	}
	if strings.Contains(err.Error(), "exit status 1") || strings.Contains(err.Error(), "exit status 2") {
		return false, nil
	}
	return false, err
}

func branchAttachedToWorktree(ctx context.Context, runner environment.Runner, repoPath string, branch string) (bool, error) {
	path, err := worktreePathForBranch(ctx, runner, repoPath, branch)
	if err != nil {
		return false, err
	}
	return path != "", nil
}

func worktreePathForBranch(ctx context.Context, runner environment.Runner, repoPath string, branch string) (string, error) {
	output, err := runner.Run(ctx, repoPath, "git", "worktree", "list", "--porcelain")
	if err != nil {
		return "", err
	}
	needle := "branch refs/heads/" + branch
	currentPath := ""
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "worktree ") {
			currentPath = strings.TrimSpace(strings.TrimPrefix(line, "worktree "))
			continue
		}
		if line == needle {
			return currentPath, nil
		}
	}
	return "", nil
}
