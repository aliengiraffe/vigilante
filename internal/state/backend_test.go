package state

import (
	"encoding/json"
	"testing"
)

func TestWatchTargetEffectiveIssueBackendDefaultsToGitHub(t *testing.T) {
	target := WatchTarget{Repo: "owner/repo"}
	if got := target.EffectiveIssueBackend(); got != "github" {
		t.Fatalf("expected github, got %q", got)
	}
}

func TestWatchTargetEffectiveIssueBackendRespectsExplicit(t *testing.T) {
	target := WatchTarget{Repo: "owner/repo", IssueBackend: "linear"}
	if got := target.EffectiveIssueBackend(); got != "linear" {
		t.Fatalf("expected linear, got %q", got)
	}
}

func TestWatchTargetEffectiveIssueStageDefaultsLinearToTodo(t *testing.T) {
	target := WatchTarget{Repo: "owner/repo", IssueBackend: "linear"}
	if got := target.EffectiveIssueStage(); got != "Todo" {
		t.Fatalf("expected Todo, got %q", got)
	}
}

func TestWatchTargetEffectiveIssueStageRespectsExplicit(t *testing.T) {
	target := WatchTarget{Repo: "owner/repo", IssueBackend: "linear", IssueStage: "In Progress"}
	if got := target.EffectiveIssueStage(); got != "In Progress" {
		t.Fatalf("expected In Progress, got %q", got)
	}
}

func TestWatchTargetEffectiveGitBackendDefaultsToGitHub(t *testing.T) {
	target := WatchTarget{Repo: "owner/repo"}
	if got := target.EffectiveGitBackend(); got != "github" {
		t.Fatalf("expected github, got %q", got)
	}
}

func TestWatchTargetEffectivePRBackendDefaultsToGitHub(t *testing.T) {
	target := WatchTarget{Repo: "owner/repo"}
	if got := target.EffectivePRBackend(); got != "github" {
		t.Fatalf("expected github, got %q", got)
	}
}

func TestWatchTargetEffectiveProjectRefFallsBackToRepo(t *testing.T) {
	target := WatchTarget{Repo: "owner/repo"}
	if got := target.EffectiveProjectRef(); got != "owner/repo" {
		t.Fatalf("expected owner/repo, got %q", got)
	}
}

func TestWatchTargetEffectiveProjectRefRespectsExplicit(t *testing.T) {
	target := WatchTarget{Repo: "owner/repo", ProjectRef: "LINEAR-123"}
	if got := target.EffectiveProjectRef(); got != "LINEAR-123" {
		t.Fatalf("expected LINEAR-123, got %q", got)
	}
}

func TestMixedBackendModelingLinearIssuesPlusGitHubPRs(t *testing.T) {
	target := WatchTarget{
		Path:         "/repos/myapp",
		Repo:         "owner/myapp",
		IssueBackend: "linear",
		GitBackend:   "github",
		PRBackend:    "github",
		ProjectRef:   "TEAM-project-id",
	}
	if target.EffectiveIssueBackend() != "linear" {
		t.Fatal("issue backend should be linear")
	}
	if target.EffectiveGitBackend() != "github" {
		t.Fatal("git backend should be github")
	}
	if target.EffectivePRBackend() != "github" {
		t.Fatal("PR backend should be github")
	}
	if target.EffectiveProjectRef() != "TEAM-project-id" {
		t.Fatal("project ref should be TEAM-project-id")
	}
}

func TestMixedBackendModelingJiraIssuesPlusGitHubPRs(t *testing.T) {
	target := WatchTarget{
		Path:         "/repos/myapp",
		Repo:         "owner/myapp",
		IssueBackend: "jira",
		GitBackend:   "github",
		PRBackend:    "github",
		ProjectRef:   "PROJ-board",
	}
	if target.EffectiveIssueBackend() != "jira" {
		t.Fatal("issue backend should be jira")
	}
	if target.EffectiveGitBackend() != "github" {
		t.Fatal("git backend should be github")
	}
}

func TestSessionBackendFieldsSerializeCorrectly(t *testing.T) {
	session := Session{
		Repo:         "owner/repo",
		IssueNumber:  42,
		IssueBackend: "linear",
		GitBackend:   "github",
		PRBackend:    "github",
		Status:       SessionStatusRunning,
	}
	data, err := json.Marshal(session)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Session
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.IssueBackend != "linear" {
		t.Fatalf("expected linear, got %q", decoded.IssueBackend)
	}
	if decoded.GitBackend != "github" {
		t.Fatalf("expected github, got %q", decoded.GitBackend)
	}
	if decoded.PRBackend != "github" {
		t.Fatalf("expected github, got %q", decoded.PRBackend)
	}
}

func TestWatchTargetBackendFieldsSerializeCorrectly(t *testing.T) {
	target := WatchTarget{
		Path:         "/repos/test",
		Repo:         "owner/test",
		IssueBackend: "jira",
		GitBackend:   "github",
		PRBackend:    "github",
		ProjectRef:   "PROJ-123",
	}
	data, err := json.Marshal(target)
	if err != nil {
		t.Fatal(err)
	}
	var decoded WatchTarget
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.IssueBackend != "jira" || decoded.ProjectRef != "PROJ-123" {
		t.Fatalf("backend fields did not round-trip: %+v", decoded)
	}
}

func TestWatchTargetBackendFieldsOmittedWhenEmpty(t *testing.T) {
	target := WatchTarget{
		Path: "/repos/test",
		Repo: "owner/test",
	}
	data, err := json.Marshal(target)
	if err != nil {
		t.Fatal(err)
	}
	raw := string(data)
	for _, field := range []string{"issue_backend", "git_backend", "pr_backend", "project_ref"} {
		if contains(raw, field) {
			t.Fatalf("expected %q to be omitted from JSON, got: %s", field, raw)
		}
	}
}

func contains(s string, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s string, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
