package worktree

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nicobistolfi/vigilante/internal/environment"
	"github.com/nicobistolfi/vigilante/internal/state"
)

type Worktree struct {
	Path   string
	Branch string
}

func CreateIssueWorktree(ctx context.Context, runner environment.Runner, target state.WatchTarget, issueNumber int) (Worktree, error) {
	branch := fmt.Sprintf("vigilante/issue-%d", issueNumber)
	path := filepath.Join(target.Path, ".worktrees", "vigilante", fmt.Sprintf("issue-%d", issueNumber))

	if _, err := runner.Run(ctx, target.Path, "git", "worktree", "prune"); err != nil {
		return Worktree{}, err
	}
	if _, err := os.Stat(path); err == nil {
		return Worktree{}, fmt.Errorf("worktree already exists for issue #%d at %s", issueNumber, path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return Worktree{}, err
	}

	args := []string{"worktree", "add", "-b", branch, path, target.Branch}
	if branchExists(ctx, runner, target.Path, branch) {
		args = []string{"worktree", "add", path, branch}
	}

	if _, err := runner.Run(ctx, target.Path, "git", args...); err != nil {
		return Worktree{}, err
	}
	return Worktree{Path: path, Branch: branch}, nil
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

	attached, err := branchAttachedToWorktree(ctx, runner, repoPath, branch)
	if err != nil {
		return err
	}
	if attached {
		return nil
	}

	exists, err := branchExistsWithError(ctx, runner, repoPath, branch)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}

	_, err = runner.Run(ctx, repoPath, "git", "branch", "-D", branch)
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
