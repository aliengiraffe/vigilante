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
	State    string     `json:"state"`
	MergedAt *time.Time `json:"mergedAt"`
}

type IssueComment struct {
	ID        int64     `json:"id"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
	User      struct {
		Login string `json:"login"`
	} `json:"user"`
}

type IssueDetails struct {
	Labels []Label `json:"labels"`
}

func ListOpenIssues(ctx context.Context, runner environment.Runner, repo string, assignee string) ([]Issue, error) {
	resolvedAssignee, err := resolveAssignee(ctx, runner, assignee)
	if err != nil {
		return nil, err
	}

	args := []string{"issue", "list", "--repo", repo, "--state", "open"}
	if resolvedAssignee != "" {
		args = append(args, "--assignee", resolvedAssignee)
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

func resolveAssignee(ctx context.Context, runner environment.Runner, assignee string) (string, error) {
	if assignee != "me" {
		return assignee, nil
	}

	output, err := runner.Run(ctx, "", "gh", "api", "user", "--jq", ".login")
	if err != nil {
		return "", fmt.Errorf("resolve assignee %q: %w", assignee, err)
	}
	return strings.TrimSpace(output), nil
}

func SelectNextIssue(issues []Issue, sessions []state.Session, target state.WatchTarget) *Issue {
	active := map[int]bool{}
	for _, session := range sessions {
		if session.Repo == target.Repo && sessionBlocksRedispatch(session) {
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

func sessionBlocksRedispatch(session state.Session) bool {
	if session.Status == state.SessionStatusRunning || session.Status == state.SessionStatusBlocked || session.Status == state.SessionStatusResuming {
		return true
	}
	if session.Status != state.SessionStatusSuccess {
		return false
	}
	if session.CleanupCompletedAt != "" || session.MonitoringStoppedAt != "" {
		return false
	}
	return true
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

func GetIssueDetails(ctx context.Context, runner environment.Runner, repo string, number int) (*IssueDetails, error) {
	output, err := runner.Run(ctx, "", "gh", "api", issueAPIPath(repo, number))
	if err != nil {
		return nil, err
	}

	var details IssueDetails
	if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &details); err != nil {
		return nil, fmt.Errorf("parse gh issue details output: %w", err)
	}
	return &details, nil
}

func ListIssueComments(ctx context.Context, runner environment.Runner, repo string, number int) ([]IssueComment, error) {
	output, err := runner.Run(ctx, "", "gh", "api", issueAPIPath(repo, number)+"/comments")
	if err != nil {
		return nil, err
	}

	var comments []IssueComment
	if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &comments); err != nil {
		return nil, fmt.Errorf("parse gh issue comments output: %w", err)
	}
	sort.Slice(comments, func(i, j int) bool {
		return comments[i].CreatedAt.Before(comments[j].CreatedAt)
	})
	return comments, nil
}

func AddIssueCommentReaction(ctx context.Context, runner environment.Runner, repo string, commentID int64, content string) error {
	_, err := runner.Run(
		ctx,
		"",
		"gh",
		"api",
		"--method", "POST",
		"-H", "Accept: application/vnd.github+json",
		fmt.Sprintf("repos/%s/issues/comments/%d/reactions", repo, commentID),
		"-f", "content="+content,
	)
	return err
}

func RemoveIssueLabel(ctx context.Context, runner environment.Runner, repo string, number int, label string) error {
	_, err := runner.Run(ctx, "", "gh", "issue", "edit", "--repo", repo, fmt.Sprintf("%d", number), "--remove-label", label)
	return err
}

func HasAnyLabel(labels []Label, wanted ...string) bool {
	for _, label := range labels {
		for _, candidate := range wanted {
			if label.Name == candidate {
				return true
			}
		}
	}
	return false
}

func FindResumeComment(comments []IssueComment, claimedCommentID int64) *IssueComment {
	for i := len(comments) - 1; i >= 0; i-- {
		body := strings.TrimSpace(comments[i].Body)
		if body != "@vigilanteai resume" {
			continue
		}
		if claimedCommentID != 0 && comments[i].ID == claimedCommentID {
			return nil
		}
		return &comments[i]
	}
	return nil
}

func FindPullRequestForBranch(ctx context.Context, runner environment.Runner, repo string, branch string) (*PullRequest, error) {
	output, err := runner.Run(ctx, "", "gh", "pr", "list", "--repo", repo, "--head", branch, "--state", "all", "--json", "number,url,state,mergedAt")
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

func issueAPIPath(repo string, number int) string {
	return "repos/" + repo + "/issues/" + fmt.Sprintf("%d", number)
}
