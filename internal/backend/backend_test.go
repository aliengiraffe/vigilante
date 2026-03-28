package backend

import (
	"testing"
	"time"

	"github.com/nicobistolfi/vigilante/internal/state"
)

func TestSelectWorkItemsRespectsLimit(t *testing.T) {
	items := []WorkItem{
		{Number: 1, Labels: []string{"to-do"}},
		{Number: 2, Labels: []string{"to-do"}},
		{Number: 3, Labels: []string{"to-do"}},
	}
	selected := SelectWorkItems(items, nil, state.WatchTarget{Repo: "owner/repo"}, 2)
	if len(selected) != 2 {
		t.Fatalf("expected 2, got %d", len(selected))
	}
	if selected[0].Number != 1 || selected[1].Number != 2 {
		t.Fatalf("unexpected selection: %#v", selected)
	}
}

func TestSelectWorkItemsFiltersActiveSessionIssues(t *testing.T) {
	items := []WorkItem{
		{Number: 1, Labels: []string{"to-do"}},
		{Number: 2, Labels: []string{"to-do"}},
	}
	sessions := []state.Session{
		{Repo: "owner/repo", IssueNumber: 1, Status: state.SessionStatusRunning},
	}
	selected := SelectWorkItems(items, sessions, state.WatchTarget{Repo: "owner/repo"}, 5)
	if len(selected) != 1 || selected[0].Number != 2 {
		t.Fatalf("expected issue 2, got %#v", selected)
	}
}

func TestSelectWorkItemsRespectsLabelAllowlist(t *testing.T) {
	items := []WorkItem{
		{Number: 1, Labels: []string{"bug"}},
		{Number: 2, Labels: []string{"to-do"}},
		{Number: 3, Labels: []string{"good first issue", "help wanted"}},
	}
	selected := SelectWorkItems(items, nil, state.WatchTarget{Repo: "owner/repo", Labels: []string{"to-do", "good first issue"}}, 5)
	if len(selected) != 2 || selected[0].Number != 2 || selected[1].Number != 3 {
		t.Fatalf("unexpected selection: %#v", selected)
	}
}

func TestSelectWorkItemsEmptyAllowlistMatchesAll(t *testing.T) {
	items := []WorkItem{
		{Number: 1, Labels: []string{"bug"}},
		{Number: 2},
	}
	selected := SelectWorkItems(items, nil, state.WatchTarget{Repo: "owner/repo"}, 5)
	if len(selected) != 2 {
		t.Fatalf("expected all items, got %d", len(selected))
	}
}

func TestSelectNextWorkItem(t *testing.T) {
	items := []WorkItem{
		{Number: 1, Labels: []string{"to-do"}},
		{Number: 2, Labels: []string{"to-do"}},
	}
	next := SelectNextWorkItem(items, nil, state.WatchTarget{Repo: "owner/repo"})
	if next == nil || next.Number != 1 {
		t.Fatalf("expected issue 1, got %#v", next)
	}
}

func TestSelectNextWorkItemReturnsNilWhenEmpty(t *testing.T) {
	next := SelectNextWorkItem(nil, nil, state.WatchTarget{Repo: "owner/repo"})
	if next != nil {
		t.Fatalf("expected nil, got %#v", next)
	}
}

func TestActiveSessionCount(t *testing.T) {
	sessions := []state.Session{
		{Repo: "owner/repo", IssueNumber: 1, Status: state.SessionStatusRunning},
		{Repo: "owner/repo", IssueNumber: 2, Status: state.SessionStatusSuccess},
		{Repo: "owner/repo", IssueNumber: 3, Status: state.SessionStatusResuming},
		{Repo: "other/repo", IssueNumber: 4, Status: state.SessionStatusRunning},
	}
	count := ActiveSessionCount(sessions, state.WatchTarget{Repo: "owner/repo"})
	if count != 2 {
		t.Fatalf("expected 2, got %d", count)
	}
}

func TestHasLabel(t *testing.T) {
	labels := []string{"bug", "to-do", "vigilante:running"}
	if !HasLabel(labels, "to-do") {
		t.Fatal("expected to find to-do")
	}
	if HasLabel(labels, "not-found") {
		t.Fatal("expected not to find not-found")
	}
	if !HasLabel(labels, "missing", "to-do") {
		t.Fatal("expected to find to-do via multi-match")
	}
}

func TestPlanLabelSync(t *testing.T) {
	current := []string{"vigilante:running", "bug"}
	desired := []string{"vigilante:done"}
	managed := []string{"vigilante:running", "vigilante:done", "vigilante:blocked"}

	toAdd, toRemove := PlanLabelSync(current, desired, managed)
	if len(toAdd) != 1 || toAdd[0] != "vigilante:done" {
		t.Fatalf("unexpected toAdd: %#v", toAdd)
	}
	if len(toRemove) != 1 || toRemove[0] != "vigilante:running" {
		t.Fatalf("unexpected toRemove: %#v", toRemove)
	}
}

func TestPlanLabelSyncNoChanges(t *testing.T) {
	current := []string{"vigilante:done"}
	desired := []string{"vigilante:done"}
	managed := []string{"vigilante:done"}
	toAdd, toRemove := PlanLabelSync(current, desired, managed)
	if len(toAdd) != 0 || len(toRemove) != 0 {
		t.Fatalf("expected no changes, got add=%#v remove=%#v", toAdd, toRemove)
	}
}

func TestFindResumeComment(t *testing.T) {
	comments := []Comment{
		{ID: 1, Body: "test comment"},
		{ID: 2, Body: "@vigilanteai resume"},
		{ID: 3, Body: "another comment"},
	}
	found := FindResumeComment(comments, 0)
	if found == nil || found.ID != 2 {
		t.Fatalf("expected comment 2, got %#v", found)
	}
}

func TestFindResumeCommentSkipsAcknowledged(t *testing.T) {
	comments := []Comment{
		{ID: 2, Body: "@vigilanteai resume"},
	}
	found := FindResumeComment(comments, 2)
	if found != nil {
		t.Fatalf("expected nil for acknowledged comment, got %#v", found)
	}
}

func TestFindCleanupComment(t *testing.T) {
	comments := []Comment{
		{ID: 1, Body: "@vigilanteai cleanup"},
	}
	found := FindCleanupComment(comments, 0)
	if found == nil || found.ID != 1 {
		t.Fatalf("expected comment 1, got %#v", found)
	}
}

func TestFindRecreateComment(t *testing.T) {
	comments := []Comment{
		{ID: 5, Body: "@vigilanteai recreate"},
	}
	found := FindRecreateComment(comments, 0)
	if found == nil || found.ID != 5 {
		t.Fatalf("expected comment 5, got %#v", found)
	}
}

func TestFindIterationComment(t *testing.T) {
	comments := []Comment{
		{ID: 1, Body: "@vigilanteai resume"},
		{ID: 2, Body: "@vigilanteai please fix the tests"},
		{ID: 3, Body: "regular comment"},
	}
	found := FindIterationComment(comments, 0)
	if found == nil || found.ID != 2 {
		t.Fatalf("expected comment 2, got %#v", found)
	}
}

func TestIsIterationComment(t *testing.T) {
	cases := []struct {
		body string
		want bool
	}{
		{"@vigilanteai resume", false},
		{"@vigilanteai cleanup", false},
		{"@vigilanteai recreate", false},
		{"@vigilanteai please fix the build", true},
		{"@vigilanteai", false},
		{"regular comment", false},
	}
	for _, tc := range cases {
		got := IsIterationComment(Comment{Body: tc.body})
		if got != tc.want {
			t.Errorf("IsIterationComment(%q) = %v, want %v", tc.body, got, tc.want)
		}
	}
}

func TestIsKnownVigilanteCommand(t *testing.T) {
	if !IsKnownVigilanteCommand("@vigilanteai resume") {
		t.Error("expected resume to be known")
	}
	if !IsKnownVigilanteCommand("@vigilanteai cleanup") {
		t.Error("expected cleanup to be known")
	}
	if !IsKnownVigilanteCommand("@vigilanteai recreate") {
		t.Error("expected recreate to be known")
	}
	if IsKnownVigilanteCommand("@vigilanteai fix this") {
		t.Error("expected 'fix this' to not be known")
	}
}

func TestAssigneeIterationComments(t *testing.T) {
	comments := []Comment{
		{ID: 1, Body: "@vigilanteai fix tests", Author: "alice"},
		{ID: 2, Body: "@vigilanteai resume", Author: "alice"},
		{ID: 3, Body: "@vigilanteai add logging", Author: "bob"},
		{ID: 4, Body: "@vigilanteai refactor", Author: "alice"},
	}
	selected := AssigneeIterationComments(comments, []string{"alice"})
	if len(selected) != 2 {
		t.Fatalf("expected 2, got %d: %#v", len(selected), selected)
	}
	if selected[0].ID != 1 || selected[1].ID != 4 {
		t.Fatalf("unexpected selection: %#v", selected)
	}
}

func TestLatestUserCommentTime(t *testing.T) {
	now := time.Now().UTC()
	comments := []Comment{
		{Body: "## Progress\nProgress: [####------] 40%\n`ETA: ~5 minutes`", CreatedAt: now.Add(-2 * time.Minute)},
		{Body: "@vigilanteai fix this", CreatedAt: now.Add(-1 * time.Minute)},
		{Body: "## Done\nProgress: [##########] 100%\n`ETA: ~1 minute`", CreatedAt: now},
	}
	got := LatestUserCommentTime(comments)
	if !got.Equal(now.Add(-1 * time.Minute)) {
		t.Fatalf("expected %v, got %v", now.Add(-1*time.Minute), got)
	}
}

func TestIsUserComment(t *testing.T) {
	cases := []struct {
		body string
		want bool
	}{
		{"", false},
		{"@vigilanteai fix this", true},
		{"## Progress\nProgress: [####------] 40%\n`ETA: ~5 minutes`", false},
		{"a regular comment", true},
	}
	for _, tc := range cases {
		got := IsUserComment(Comment{Body: tc.body})
		if got != tc.want {
			t.Errorf("IsUserComment(%q) = %v, want %v", tc.body, got, tc.want)
		}
	}
}

func TestResolveIssueProviderLabel(t *testing.T) {
	providerIDs := []string{"codex", "claude", "gemini"}

	selected, err := ResolveIssueProviderLabel([]string{"claude"}, providerIDs)
	if err != nil {
		t.Fatal(err)
	}
	if selected != "claude" {
		t.Fatalf("expected claude, got %q", selected)
	}

	selected, err = ResolveIssueProviderLabel([]string{"bug"}, providerIDs)
	if err != nil {
		t.Fatal(err)
	}
	if selected != "" {
		t.Fatalf("expected empty, got %q", selected)
	}

	_, err = ResolveIssueProviderLabel([]string{"codex", "claude"}, providerIDs)
	if err == nil {
		t.Fatal("expected error for multiple provider labels")
	}
}

func TestDefaultBackendID(t *testing.T) {
	if DefaultBackendID != "github" {
		t.Fatalf("expected github, got %q", DefaultBackendID)
	}
}

func TestGitHubBackendRegistered(t *testing.T) {
	factory, ok := Lookup(GitHubBackendID)
	if !ok {
		t.Fatal("expected github backend to be registered")
	}
	b := factory(nil)
	if b.ID() != "github" {
		t.Fatalf("expected github, got %q", b.ID())
	}
}

func TestCapabilityHelpers(t *testing.T) {
	b := NewGitHubBackend(nil, nil)

	if _, ok := AsLabelManager(b); !ok {
		t.Error("expected GitHub backend to implement LabelManager")
	}
	if _, ok := AsPullRequestManager(b); !ok {
		t.Error("expected GitHub backend to implement PullRequestManager")
	}
	if _, ok := AsRateLimitChecker(b); !ok {
		t.Error("expected GitHub backend to implement RateLimitChecker")
	}
}

func TestSelectWorkItemsSkipsBlockedSessions(t *testing.T) {
	items := []WorkItem{
		{Number: 1, Labels: []string{"to-do"}},
		{Number: 2, Labels: []string{"to-do"}},
	}
	sessions := []state.Session{
		{Repo: "owner/repo", IssueNumber: 1, Status: state.SessionStatusBlocked},
	}
	selected := SelectWorkItems(items, sessions, state.WatchTarget{Repo: "owner/repo"}, 5)
	if len(selected) != 1 || selected[0].Number != 2 {
		t.Fatalf("expected only issue 2, got %#v", selected)
	}
}

func TestSelectWorkItemsAllowsCompletedCleanedUpSessions(t *testing.T) {
	items := []WorkItem{
		{Number: 1, Labels: []string{"to-do"}},
	}
	sessions := []state.Session{
		{Repo: "owner/repo", IssueNumber: 1, Status: state.SessionStatusSuccess, CleanupCompletedAt: "2026-01-01T00:00:00Z"},
	}
	selected := SelectWorkItems(items, sessions, state.WatchTarget{Repo: "owner/repo"}, 5)
	if len(selected) != 1 {
		t.Fatalf("expected issue 1 to be eligible after cleanup, got %#v", selected)
	}
}
