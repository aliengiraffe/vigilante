package backend

import (
	"testing"
	"time"
)

func TestBackendIDConstants(t *testing.T) {
	if BackendGitHub != "github" {
		t.Fatalf("unexpected GitHub backend ID: %q", BackendGitHub)
	}
	if BackendLinear != "linear" {
		t.Fatalf("unexpected Linear backend ID: %q", BackendLinear)
	}
	if BackendJira != "jira" {
		t.Fatalf("unexpected Jira backend ID: %q", BackendJira)
	}
}

func TestWorkItemTypeCompatibility(t *testing.T) {
	item := WorkItem{
		Number:    42,
		Title:     "Test issue",
		CreatedAt: time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC),
		URL:       "https://example.com/issues/42",
		Labels:    []Label{{Name: "bug"}},
	}
	if item.Number != 42 {
		t.Fatal("WorkItem.Number mismatch")
	}
	if item.Labels[0].Name != "bug" {
		t.Fatal("WorkItem.Labels mismatch")
	}
}

func TestPullRequestTypeCompatibility(t *testing.T) {
	merged := time.Date(2026, 3, 15, 12, 0, 0, 0, time.UTC)
	pr := PullRequest{
		Number:           1,
		Title:            "Fix bug",
		State:            "MERGED",
		MergedAt:         &merged,
		Mergeable:        "MERGEABLE",
		MergeStateStatus: "CLEAN",
		ReviewDecision:   "APPROVED",
		StatusCheckRollup: []StatusCheck{
			{Name: "ci", State: "SUCCESS", Conclusion: "success"},
		},
	}
	if pr.Number != 1 {
		t.Fatal("PullRequest.Number mismatch")
	}
	if pr.MergedAt == nil {
		t.Fatal("PullRequest.MergedAt should not be nil")
	}
	if len(pr.StatusCheckRollup) != 1 {
		t.Fatal("PullRequest.StatusCheckRollup length mismatch")
	}
}

func TestWorkItemDetailsContainsExpectedFields(t *testing.T) {
	details := WorkItemDetails{
		Title:     "Test",
		Body:      "body",
		URL:       "url",
		State:     "open",
		Labels:    []Label{{Name: "enhancement"}},
		Assignees: []UserRef{{Login: "user1"}},
	}
	if details.Title != "Test" || len(details.Assignees) != 1 {
		t.Fatal("WorkItemDetails field mismatch")
	}
}

func TestWorkItemCommentHasNestedUserField(t *testing.T) {
	comment := WorkItemComment{
		ID:   123,
		Body: "test comment",
	}
	comment.User.Login = "testuser"
	if comment.User.Login != "testuser" {
		t.Fatal("WorkItemComment.User.Login mismatch")
	}
}
