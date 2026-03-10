package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Worktree struct {
	Path   string
	Branch string
}

func CreateIssueWorktree(ctx context.Context, runner Runner, state *StateStore, target WatchTarget, issueNumber int) (Worktree, error) {
	slug := strings.ReplaceAll(target.Repo, "/", "_")
	branch := fmt.Sprintf("vigilante/issue-%d", issueNumber)
	path := filepath.Join(state.WorktreesDir(), slug, fmt.Sprintf("issue-%d", issueNumber))

	if _, err := os.Stat(path); err == nil {
		return Worktree{}, fmt.Errorf("worktree already exists for issue #%d at %s", issueNumber, path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return Worktree{}, err
	}
	if _, err := runner.Run(ctx, target.Path, "git", "worktree", "add", "-b", branch, path, target.Branch); err != nil {
		return Worktree{}, err
	}
	return Worktree{Path: path, Branch: branch}, nil
}

func RemoveWorktree(ctx context.Context, runner Runner, repoPath string, worktreePath string) error {
	_, err := runner.Run(ctx, repoPath, "git", "worktree", "remove", "--force", worktreePath)
	return err
}
