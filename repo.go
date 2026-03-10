package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
)

type RepositoryInfo struct {
	Path   string
	Repo   string
	Branch string
}

func DiscoverRepository(ctx context.Context, runner Runner, path string) (RepositoryInfo, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return RepositoryInfo{}, err
	}
	if _, err := runner.Run(ctx, absPath, "git", "rev-parse", "--is-inside-work-tree"); err != nil {
		return RepositoryInfo{}, fmt.Errorf("%s is not a git repository: %w", absPath, err)
	}

	remoteURL, err := runner.Run(ctx, absPath, "git", "remote", "get-url", "origin")
	if err != nil {
		return RepositoryInfo{}, fmt.Errorf("origin remote not found: %w", err)
	}
	repo, err := ParseGitHubRepo(strings.TrimSpace(remoteURL))
	if err != nil {
		return RepositoryInfo{}, err
	}

	branch := "main"
	if remoteHead, err := runner.Run(ctx, absPath, "git", "symbolic-ref", "--short", "refs/remotes/origin/HEAD"); err == nil {
		branch = strings.TrimPrefix(strings.TrimSpace(remoteHead), "origin/")
	} else if current, err := runner.Run(ctx, absPath, "git", "branch", "--show-current"); err == nil && strings.TrimSpace(current) != "" {
		branch = strings.TrimSpace(current)
	}

	return RepositoryInfo{
		Path:   absPath,
		Repo:   repo,
		Branch: branch,
	}, nil
}

func ParseGitHubRepo(remote string) (string, error) {
	remote = strings.TrimSpace(remote)
	if remote == "" {
		return "", errors.New("empty remote URL")
	}

	if strings.HasPrefix(remote, "git@github.com:") {
		path := strings.TrimPrefix(remote, "git@github.com:")
		return normalizeGitHubPath(path)
	}

	if strings.HasPrefix(remote, "ssh://") || strings.HasPrefix(remote, "https://") || strings.HasPrefix(remote, "http://") {
		parsed, err := url.Parse(remote)
		if err != nil {
			return "", err
		}
		if !strings.EqualFold(parsed.Host, "github.com") {
			return "", fmt.Errorf("unsupported remote host %q", parsed.Host)
		}
		return normalizeGitHubPath(strings.TrimPrefix(parsed.Path, "/"))
	}

	return "", fmt.Errorf("unsupported remote format %q", remote)
}

func normalizeGitHubPath(path string) (string, error) {
	path = strings.TrimSuffix(path, ".git")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", fmt.Errorf("invalid GitHub repo path %q", path)
	}
	return parts[0] + "/" + parts[1], nil
}
