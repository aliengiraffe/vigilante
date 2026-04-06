package linear

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/nicobistolfi/vigilante/internal/backend"
	"github.com/nicobistolfi/vigilante/internal/environment"
)

type Backend struct {
	runnerRef *environment.Runner
}

func NewBackend(runner *environment.Runner) *Backend {
	return &Backend{runnerRef: runner}
}

func (b *Backend) runner() environment.Runner {
	return *b.runnerRef
}

var (
	_ backend.IssueTracker = (*Backend)(nil)
	_ backend.LabelManager = (*Backend)(nil)
)

func (b *Backend) ID() backend.BackendID {
	return backend.BackendLinear
}

func (b *Backend) ResolveAssignee(_ context.Context, assignee string) (string, error) {
	assignee = strings.TrimSpace(assignee)
	if assignee == "" {
		return "me", nil
	}
	return assignee, nil
}

func (b *Backend) ListOpenWorkItems(ctx context.Context, project string, assignee string) ([]backend.WorkItem, error) {
	output, err := b.runner().Run(ctx, "", "linear", "issue", "list")
	if err != nil {
		return nil, err
	}

	ids := parseLinearIssueListIDs(strings.TrimSpace(output))
	items := make([]backend.WorkItem, 0, len(ids))
	for _, id := range ids {
		output, err := b.runner().Run(ctx, "", "linear", "issue", "view", id, "--json")
		if err != nil {
			return nil, err
		}
		item, err := parseLinearWorkItem(strings.TrimSpace(output))
		if err != nil {
			return nil, fmt.Errorf("parse linear issue view output for %q: %w", id, err)
		}
		items = append(items, item)
	}
	assignee = strings.TrimSpace(assignee)
	if assignee == "" || assignee == "me" {
		return items, nil
	}
	return nil, fmt.Errorf("linear issue tracker currently supports the default assignee filter only")
}

func (b *Backend) GetWorkItemDetails(ctx context.Context, project string, number int) (*backend.WorkItemDetails, error) {
	output, err := b.runner().Run(ctx, "", "linear", "issue", "view", strconv.Itoa(number), "--json")
	if err != nil {
		return nil, err
	}
	details, err := parseLinearWorkItemDetails(strings.TrimSpace(output))
	if err != nil {
		return nil, fmt.Errorf("parse linear issue view output: %w", err)
	}
	return details, nil
}

func (b *Backend) ListWorkItemComments(ctx context.Context, project string, number int) ([]backend.WorkItemComment, error) {
	output, err := b.runner().Run(ctx, "", "linear", "issue", "comment", "list", strconv.Itoa(number), "--json")
	if err != nil {
		return nil, err
	}
	comments, err := parseLinearComments(strings.TrimSpace(output))
	if err != nil {
		return nil, fmt.Errorf("parse linear issue comments output: %w", err)
	}
	return comments, nil
}

func (b *Backend) ListWorkItemCommentsForPolling(ctx context.Context, project string, number int, _ string, _ *slog.Logger) ([]backend.WorkItemComment, error) {
	return b.ListWorkItemComments(ctx, project, number)
}

func (b *Backend) CommentOnWorkItem(ctx context.Context, project string, number int, body string) error {
	_, err := b.runner().Run(ctx, "", "linear", "issue", "comment", "add", strconv.Itoa(number), "--body", body)
	return err
}

func (b *Backend) AddCommentReaction(context.Context, string, int64, string) error {
	return nil
}

func (b *Backend) CreateWorkItem(ctx context.Context, project string, title string, body string, _ []string, _ []string) (*backend.CreatedWorkItem, error) {
	output, err := b.runner().Run(ctx, "", "linear", "issue", "create", "--title", title, "--description", body, "--json")
	if err != nil {
		return nil, err
	}
	created, err := parseLinearCreatedWorkItem(strings.TrimSpace(output))
	if err != nil {
		return nil, fmt.Errorf("parse linear issue create output: %w", err)
	}
	return created, nil
}

func (b *Backend) CloseWorkItem(ctx context.Context, project string, number int) error {
	_, err := b.runner().Run(ctx, "", "linear", "issue", "update", strconv.Itoa(number), "--state", "Canceled")
	return err
}

func (b *Backend) IsWorkItemUnavailable(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "not found") || strings.Contains(text, "no such issue")
}

func (b *Backend) EnsureProjectLabels(context.Context, string, []backend.RepositoryLabelSpec) error {
	return nil
}

func (b *Backend) SyncWorkItemLabels(context.Context, string, int, []backend.Label, []string, []string) error {
	return nil
}

func (b *Backend) RemoveWorkItemLabel(context.Context, string, int, string) error {
	return nil
}

type linearWorkItem struct {
	Number      int             `json:"number"`
	Identifier  string          `json:"identifier"`
	Title       string          `json:"title"`
	URL         string          `json:"url"`
	CreatedAt   string          `json:"createdAt"`
	Description string          `json:"description"`
	State       *linearState    `json:"state"`
	Labels      []backend.Label `json:"labels"`
}

type linearState struct {
	Name string `json:"name"`
}

func parseLinearWorkItems(raw string) ([]backend.WorkItem, error) {
	if raw == "" {
		return nil, nil
	}
	var payload []linearWorkItem
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return nil, err
	}
	items := make([]backend.WorkItem, 0, len(payload))
	for _, item := range payload {
		number := item.Number
		if number == 0 {
			number = parseLinearIssueNumber(item.Identifier)
		}
		createdAt, _ := time.Parse(time.RFC3339, strings.TrimSpace(item.CreatedAt))
		workItem := backend.WorkItem{
			Number:    number,
			Title:     strings.TrimSpace(item.Title),
			CreatedAt: createdAt,
			URL:       strings.TrimSpace(item.URL),
			Labels:    item.Labels,
		}
		if item.State != nil {
			workItem.Stage = strings.TrimSpace(item.State.Name)
		}
		items = append(items, workItem)
	}
	return items, nil
}

func parseLinearWorkItem(raw string) (backend.WorkItem, error) {
	var item linearWorkItem
	if err := json.Unmarshal([]byte(raw), &item); err != nil {
		return backend.WorkItem{}, err
	}
	number := item.Number
	if number == 0 {
		number = parseLinearIssueNumber(item.Identifier)
	}
	createdAt, _ := time.Parse(time.RFC3339, strings.TrimSpace(item.CreatedAt))
	workItem := backend.WorkItem{
		Number:    number,
		Title:     strings.TrimSpace(item.Title),
		CreatedAt: createdAt,
		URL:       strings.TrimSpace(item.URL),
		Labels:    item.Labels,
	}
	if item.State != nil {
		workItem.Stage = strings.TrimSpace(item.State.Name)
	}
	return workItem, nil
}

func parseLinearWorkItemDetails(raw string) (*backend.WorkItemDetails, error) {
	var item linearWorkItem
	if err := json.Unmarshal([]byte(raw), &item); err != nil {
		return nil, err
	}
	return &backend.WorkItemDetails{
		Title:  strings.TrimSpace(item.Title),
		Body:   strings.TrimSpace(item.Description),
		URL:    strings.TrimSpace(item.URL),
		State:  "open",
		Labels: item.Labels,
	}, nil
}

func parseLinearComments(raw string) ([]backend.WorkItemComment, error) {
	if raw == "" {
		return nil, nil
	}
	var payload []struct {
		ID        int64  `json:"id"`
		Body      string `json:"body"`
		CreatedAt string `json:"createdAt"`
		User      struct {
			Name string `json:"name"`
		} `json:"user"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return nil, err
	}
	comments := make([]backend.WorkItemComment, 0, len(payload))
	for _, comment := range payload {
		createdAt, _ := time.Parse(time.RFC3339, strings.TrimSpace(comment.CreatedAt))
		item := backend.WorkItemComment{
			ID:        comment.ID,
			Body:      strings.TrimSpace(comment.Body),
			CreatedAt: createdAt,
		}
		item.User.Login = strings.TrimSpace(comment.User.Name)
		comments = append(comments, item)
	}
	return comments, nil
}

func parseLinearCreatedWorkItem(raw string) (*backend.CreatedWorkItem, error) {
	var item linearWorkItem
	if err := json.Unmarshal([]byte(raw), &item); err != nil {
		return nil, err
	}
	number := item.Number
	if number == 0 {
		number = parseLinearIssueNumber(item.Identifier)
	}
	return &backend.CreatedWorkItem{
		Number: number,
		URL:    strings.TrimSpace(item.URL),
	}, nil
}

func parseLinearIssueNumber(identifier string) int {
	parts := strings.Split(strings.TrimSpace(identifier), "-")
	if len(parts) == 0 {
		return 0
	}
	number, _ := strconv.Atoi(parts[len(parts)-1])
	return number
}

func parseLinearIssueListIDs(raw string) []string {
	if raw == "" {
		return nil
	}
	lines := strings.Split(raw, "\n")
	ids := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if isLinearIssueListSeparator(trimmed) {
			continue
		}
		if linearIssueListHeaderRegex.MatchString(line) {
			continue
		}
		match := linearIssueListIDRegex.FindString(line)
		if match == "" {
			continue
		}
		ids = append(ids, match)
	}
	return ids
}

var linearIssueListHeaderRegex = regexp.MustCompile(`(^|\s)ID(\s|$)`)
var linearIssueListIDRegex = regexp.MustCompile(`\b[A-Z][A-Z0-9]*-\d+\b`)

func isLinearIssueListSeparator(line string) bool {
	for _, r := range line {
		if r != '-' && r != '─' && r != ' ' {
			return false
		}
	}
	return true
}
