package ghcli

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/nicobistolfi/vigilante/internal/environment"
	"github.com/nicobistolfi/vigilante/internal/state"
)

type Issue struct {
	Number    int       `json:"number"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"createdAt"`
	URL       string    `json:"url"`
}

type PullRequest struct {
	Number   int        `json:"number"`
	URL      string     `json:"url"`
	MergedAt *time.Time `json:"mergedAt"`
}

func ListOpenIssues(ctx context.Context, runner environment.Runner, repo string) ([]Issue, error) {
	output, err := runner.Run(ctx, "", "gh", "issue", "list", "--repo", repo, "--state", "open", "--json", "number,title,createdAt,url")
	if err != nil {
		return nil, err
	}
	var issues []Issue
	if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &issues); err != nil {
		return nil, fmt.Errorf("parse gh issue list output: %w", err)
	}
	sort.Slice(issues, func(i, j int) bool {
		return issues[i].CreatedAt.Before(issues[j].CreatedAt)
	})
	return issues, nil
}

func SelectNextIssue(issues []Issue, sessions []state.Session, repo string) *Issue {
	active := map[int]bool{}
	for _, session := range sessions {
		if session.Repo == repo && session.Status == state.SessionStatusRunning {
			active[session.IssueNumber] = true
		}
	}
	for i := range issues {
		if !active[issues[i].Number] {
			return &issues[i]
		}
	}
	return nil
}

func CommentOnIssue(ctx context.Context, runner environment.Runner, repo string, number int, body string) error {
	_, err := runner.Run(ctx, "", "gh", "issue", "comment", "--repo", repo, fmt.Sprintf("%d", number), "--body", body)
	return err
}

func FindPullRequestForBranch(ctx context.Context, runner environment.Runner, repo string, branch string) (*PullRequest, error) {
	output, err := runner.Run(ctx, "", "gh", "pr", "list", "--repo", repo, "--head", branch, "--state", "all", "--json", "number,url,mergedAt")
	if err != nil {
		return nil, err
	}

	var prs []PullRequest
	if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &prs); err != nil {
		return nil, fmt.Errorf("parse gh pr list output: %w", err)
	}
	if len(prs) == 0 {
		return nil, nil
	}
	return &prs[0], nil
}
