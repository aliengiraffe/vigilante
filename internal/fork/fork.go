package fork

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/nicobistolfi/vigilante/internal/environment"
	"github.com/nicobistolfi/vigilante/internal/repo"
	"github.com/nicobistolfi/vigilante/internal/state"
)

const RemoteName = "fork"

type userResponse struct {
	Login string `json:"login"`
}

type repositoryResponse struct {
	FullName string `json:"full_name"`
	Parent   struct {
		FullName string `json:"full_name"`
	} `json:"parent"`
}

func PrepareTarget(ctx context.Context, runner environment.Runner, target state.WatchTarget) (state.WatchTarget, error) {
	if !target.ForkMode {
		return target, nil
	}

	authOwner, err := authenticatedOwner(ctx, runner)
	if err != nil {
		return target, err
	}
	forkOwner := strings.TrimSpace(target.ForkOwner)
	if forkOwner == "" {
		forkOwner = authOwner
	}

	upstreamName := target.Repo[strings.LastIndex(target.Repo, "/")+1:]
	forkRepo := forkOwner + "/" + upstreamName
	if err := ensureForkRepository(ctx, runner, target.Repo, forkRepo, authOwner, forkOwner); err != nil {
		return target, err
	}
	if err := ensureForkRemote(ctx, runner, target.Path, forkRepo); err != nil {
		return target, err
	}

	target.ForkOwner = forkOwner
	target.PushRemote = RemoteName
	target.PushRepo = forkRepo
	return target, nil
}

func ConfigureWorktree(ctx context.Context, runner environment.Runner, session state.Session) error {
	pushRemote := strings.TrimSpace(session.PushRemote)
	if pushRemote == "" || pushRemote == "origin" {
		return nil
	}
	if _, err := runner.Run(ctx, session.WorktreePath, "git", "config", "branch."+session.Branch+".remote", pushRemote); err != nil {
		return err
	}
	if _, err := runner.Run(ctx, session.WorktreePath, "git", "config", "branch."+session.Branch+".merge", "refs/heads/"+session.Branch); err != nil {
		return err
	}
	if _, err := runner.Run(ctx, session.WorktreePath, "git", "config", "branch."+session.Branch+".pushRemote", pushRemote); err != nil {
		return err
	}
	if base := strings.TrimSpace(session.BaseBranch); base != "" {
		if _, err := runner.Run(ctx, session.WorktreePath, "git", "config", "branch."+session.Branch+".gh-merge-base", base); err != nil {
			return err
		}
	}
	return nil
}

func authenticatedOwner(ctx context.Context, runner environment.Runner) (string, error) {
	output, err := runner.Run(ctx, "", "gh", "api", "user")
	if err != nil {
		return "", err
	}
	var user userResponse
	if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &user); err != nil {
		return "", fmt.Errorf("parse gh api user output: %w", err)
	}
	if strings.TrimSpace(user.Login) == "" {
		return "", fmt.Errorf("gh api user did not return a login")
	}
	return strings.TrimSpace(user.Login), nil
}

func ensureForkRepository(ctx context.Context, runner environment.Runner, upstreamRepo string, forkRepo string, authOwner string, forkOwner string) error {
	repoInfo, err := lookupRepository(ctx, runner, forkRepo)
	if err == nil {
		parent := strings.TrimSpace(repoInfo.Parent.FullName)
		if parent != "" && !strings.EqualFold(parent, upstreamRepo) {
			return fmt.Errorf("repository %s exists but is not a fork of %s", forkRepo, upstreamRepo)
		}
		return nil
	}
	if !isRepositoryNotFound(err) {
		return err
	}

	args := []string{"api", "--method", "POST", "repos/" + upstreamRepo + "/forks"}
	if !strings.EqualFold(authOwner, forkOwner) {
		args = append(args, "-f", "organization="+forkOwner)
	}
	if _, err := runner.Run(ctx, "", "gh", args...); err != nil {
		return err
	}

	var lastErr error
	for i := 0; i < 5; i++ {
		repoInfo, err = lookupRepository(ctx, runner, forkRepo)
		if err == nil {
			parent := strings.TrimSpace(repoInfo.Parent.FullName)
			if parent != "" && !strings.EqualFold(parent, upstreamRepo) {
				return fmt.Errorf("repository %s exists but is not a fork of %s", forkRepo, upstreamRepo)
			}
			return nil
		}
		lastErr = err
		time.Sleep(1 * time.Second)
	}
	return lastErr
}

func lookupRepository(ctx context.Context, runner environment.Runner, repoSlug string) (repositoryResponse, error) {
	output, err := runner.Run(ctx, "", "gh", "api", "repos/"+repoSlug)
	if err != nil {
		return repositoryResponse{}, fmt.Errorf("%s: %w", strings.TrimSpace(output), err)
	}
	var repoInfo repositoryResponse
	if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &repoInfo); err != nil {
		return repositoryResponse{}, fmt.Errorf("parse gh api repos output: %w", err)
	}
	return repoInfo, nil
}

func ensureForkRemote(ctx context.Context, runner environment.Runner, repoPath string, forkRepo string) error {
	originURL, err := runner.Run(ctx, repoPath, "git", "remote", "get-url", "origin")
	if err != nil {
		return err
	}
	forkURL, err := repo.RewriteGitHubRemote(strings.TrimSpace(originURL), forkRepo)
	if err != nil {
		return err
	}
	currentURL, err := runner.Run(ctx, repoPath, "git", "remote", "get-url", RemoteName)
	switch {
	case err == nil:
		if strings.TrimSpace(currentURL) == forkURL {
			return nil
		}
		_, err = runner.Run(ctx, repoPath, "git", "remote", "set-url", RemoteName, forkURL)
		return err
	case isUnknownRemote(err):
		_, err = runner.Run(ctx, repoPath, "git", "remote", "add", RemoteName, forkURL)
		return err
	default:
		return err
	}
}

func isRepositoryNotFound(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.Contains(text, "http 404") || strings.Contains(text, "not found")
}

func isUnknownRemote(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.Contains(text, "no such remote") || strings.Contains(text, "not a git repository")
}
