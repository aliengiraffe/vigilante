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

	if _, err := runner.Run(ctx, target.Path, "git", "worktree", "prune"); err != nil {
		return Worktree{}, err
	}
	if _, err := os.Stat(path); err == nil {
		return Worktree{}, fmt.Errorf("worktree already exists for issue #%d at %s", issueNumber, path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return Worktree{}, err
	}

	for _, candidate := range IssueBranchCandidates(issueNumber, issueTitle) {
		exists, err := remoteBranchExistsWithError(ctx, runner, target.Path, candidate)
		if err != nil {
			return Worktree{}, err
		}
		if !exists {
			continue
		}
		if _, err := runner.Run(ctx, target.Path, "git", "fetch", "origin", candidate+":"+candidate); err != nil {
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

	if _, err := runner.Run(ctx, target.Path, "git", "worktree", "add", "-b", branch, path, target.Branch); err != nil {
		return Worktree{}, err
	}
	return Worktree{Path: path, Branch: branch}, nil
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

	for _, branch := range uniqueNonEmptyStrings(branches) {
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

func remoteBranchExistsWithError(ctx context.Context, runner environment.Runner, repoPath string, branch string) (bool, error) {
	_, err := runner.Run(ctx, repoPath, "git", "ls-remote", "--exit-code", "--heads", "origin", branch)
	if err == nil {
		return true, nil
	}
	if strings.Contains(err.Error(), "exit status 1") || strings.Contains(err.Error(), "exit status 2") {
		return false, nil
	}
	return false, err
}

func branchAttachedToWorktree(ctx context.Context, runner environment.Runner, repoPath string, branch string) (bool, error) {
	output, err := runner.Run(ctx, repoPath, "git", "worktree", "list", "--porcelain")
	if err != nil {
		return false, err
	}
	needle := "branch refs/heads/" + branch
	for _, line := range strings.Split(output, "\n") {
		if strings.TrimSpace(line) == needle {
			return true, nil
		}
	}
	return false, nil
}

func uniqueNonEmptyStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	unique := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		unique = append(unique, value)
	}
	return unique
}
