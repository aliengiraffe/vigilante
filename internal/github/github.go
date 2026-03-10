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
	Labels    []Label   `json:"labels"`
}

type Label struct {
	Name string `json:"name"`
}

type PullRequest struct {
	Number   int        `json:"number"`
	URL      string     `json:"url"`
	MergedAt *time.Time `json:"mergedAt"`
}

func ListOpenIssues(ctx context.Context, runner environment.Runner, repo string, assignee string) ([]Issue, error) {
	args := []string{"issue", "list", "--repo", repo, "--state", "open"}
	if assignee != "" {
		args = append(args, "--assignee", assignee)
	}
	args = append(args, "--json", "number,title,createdAt,url,labels")
	output, err := runner.Run(ctx, "", "gh", args...)

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

func SelectNextIssue(issues []Issue, sessions []state.Session, target state.WatchTarget) *Issue {
	active := map[int]bool{}
	for _, session := range sessions {
		if session.Repo == target.Repo && session.Status == state.SessionStatusRunning {
			active[session.IssueNumber] = true
		}
	}
	for i := range issues {
		if active[issues[i].Number] {
			continue
		}
		if !matchesLabelAllowlist(issues[i], target.Labels) {
			continue
		}
		return &issues[i]
	}
	return nil
}

func matchesLabelAllowlist(issue Issue, allowlist []string) bool {
	if len(allowlist) == 0 {
		return true
	}

	for _, configured := range allowlist {
		for _, label := range issue.Labels {
			if label.Name == configured {
				return true
			}
		}
	}
	return false
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
