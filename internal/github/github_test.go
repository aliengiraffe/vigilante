package ghcli

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nicobistolfi/vigilante/internal/state"
	"github.com/nicobistolfi/vigilante/internal/testutil"
)

func TestListOpenIssuesAndSelectNext(t *testing.T) {
	runner := testutil.FakeRunner{
		Outputs: map[string]string{
			"gh api user --jq .login": "nicobistolfi\n",
			"gh issue list --repo owner/repo --state open --assignee nicobistolfi --json number,title,createdAt,url,labels": `[{"number":2,"title":"newer","createdAt":"2026-03-10T12:00:00Z","url":"u2","labels":[{"name":"to-do"}]},{"number":1,"title":"older","createdAt":"2026-03-09T12:00:00Z","url":"u1","labels":[{"name":"bug"}]}]`,
		},
	}
	issues, err := ListOpenIssues(context.Background(), runner, "owner/repo", "me")
	if err != nil {
		t.Fatal(err)
	}
	if issues[0].Number != 1 {
		t.Fatalf("expected oldest issue first: %#v", issues)
	}
	next := SelectNextIssue(issues, []state.Session{{Repo: "owner/repo", IssueNumber: 1, Status: state.SessionStatusRunning}}, state.WatchTarget{Repo: "owner/repo", Labels: []string{"to-do"}})
	if next == nil || next.Number != 2 {
		t.Fatalf("unexpected next issue: %#v", next)
	}
}

func TestSelectNextIssueSkipsSessionWithOpenPullRequestUnderMaintenance(t *testing.T) {
	issues := []Issue{
		{Number: 1, Labels: []Label{{Name: "to-do"}}},
		{Number: 2, Labels: []Label{{Name: "to-do"}}},
	}

	next := SelectNextIssue(issues, []state.Session{{
		Repo:             "owner/repo",
		IssueNumber:      1,
		Status:           state.SessionStatusSuccess,
		Branch:           "vigilante/issue-1",
		WorktreePath:     "/tmp/repo/.worktrees/vigilante/issue-1",
		PullRequestState: "OPEN",
	}}, state.WatchTarget{Repo: "owner/repo", Labels: []string{"to-do"}})
	if next == nil || next.Number != 2 {
		t.Fatalf("unexpected next issue: %#v", next)
	}
}

func TestSelectNextIssueSkipsSessionWithExistingIssueWorktree(t *testing.T) {
	issues := []Issue{
		{Number: 1, Labels: []Label{{Name: "to-do"}}},
		{Number: 2, Labels: []Label{{Name: "to-do"}}},
	}

	next := SelectNextIssue(issues, []state.Session{{
		Repo:         "owner/repo",
		IssueNumber:  1,
		Status:       state.SessionStatusSuccess,
		Branch:       "vigilante/issue-1",
		WorktreePath: "/tmp/repo/.worktrees/vigilante/issue-1",
	}}, state.WatchTarget{Repo: "owner/repo", Labels: []string{"to-do"}})
	if next == nil || next.Number != 2 {
		t.Fatalf("unexpected next issue: %#v", next)
	}
}

func TestSelectNextIssueRespectsConfiguredLabels(t *testing.T) {
	issues := []Issue{
		{Number: 1, Labels: []Label{{Name: "bug"}}},
		{Number: 2, Labels: []Label{{Name: "to-do"}}},
		{Number: 3, Labels: []Label{{Name: "good first issue"}, {Name: "help wanted"}}},
	}

	next := SelectNextIssue(issues, nil, state.WatchTarget{Repo: "owner/repo", Labels: []string{"to-do", "good first issue"}})
	if next == nil || next.Number != 2 {
		t.Fatalf("unexpected next issue: %#v", next)
	}

	next = SelectNextIssue(issues, nil, state.WatchTarget{Repo: "owner/repo", Labels: []string{"good first issue"}})
	if next == nil || next.Number != 3 {
		t.Fatalf("unexpected next issue for second label match: %#v", next)
	}

	next = SelectNextIssue(issues, nil, state.WatchTarget{Repo: "owner/repo", Labels: []string{"vibe-code"}})
	if next != nil {
		t.Fatalf("expected no matching issue, got %#v", next)
	}
}

func TestSelectIssuesHonorsRequestedLimit(t *testing.T) {
	issues := []Issue{
		{Number: 1, Labels: []Label{{Name: "to-do"}}},
		{Number: 2, Labels: []Label{{Name: "to-do"}}},
		{Number: 3, Labels: []Label{{Name: "to-do"}}},
	}

	selected := SelectIssues(issues, nil, state.WatchTarget{Repo: "owner/repo", Labels: []string{"to-do"}}, 2)
	if len(selected) != 2 || selected[0].Number != 1 || selected[1].Number != 2 {
		t.Fatalf("unexpected selected issues: %#v", selected)
	}
}

func TestSelectIssuesHonorsConfiguredStageForLinearTargets(t *testing.T) {
	issues := []Issue{
		{Number: 1, Labels: []Label{{Name: "to-do"}}, Stage: "Todo"},
		{Number: 2, Labels: []Label{{Name: "to-do"}}, Stage: "In Progress"},
	}

	selected := SelectIssues(issues, nil, state.WatchTarget{
		Repo:         "owner/repo",
		IssueBackend: "linear",
		IssueStage:   "todo",
		Labels:       []string{"to-do"},
	}, 2)
	if len(selected) != 1 || selected[0].Number != 1 {
		t.Fatalf("unexpected selected issues: %#v", selected)
	}
}

func TestActiveSessionCountCountsOnlyActiveExecutionSessions(t *testing.T) {
	count := ActiveSessionCount([]state.Session{
		{Repo: "owner/repo", IssueNumber: 1, Status: state.SessionStatusRunning},
		{Repo: "owner/repo", IssueNumber: 5, Status: state.SessionStatusResuming},
		{Repo: "owner/repo", IssueNumber: 2, Status: state.SessionStatusSuccess, PullRequestState: "OPEN"},
		{Repo: "owner/repo", IssueNumber: 6, Status: state.SessionStatusBlocked},
		{Repo: "owner/repo", IssueNumber: 3, Status: state.SessionStatusSuccess, CleanupCompletedAt: "2026-03-10T15:00:00Z"},
		{Repo: "owner/other", IssueNumber: 4, Status: state.SessionStatusRunning},
	}, state.WatchTarget{Repo: "owner/repo"})
	if count != 2 {
		t.Fatalf("unexpected active session count: %d", count)
	}
}

func TestSelectIssuesSkipsBlockedAndOpenPullRequestSessionsWithoutConsumingCapacity(t *testing.T) {
	issues := []Issue{
		{Number: 1, Labels: []Label{{Name: "to-do"}}},
		{Number: 2, Labels: []Label{{Name: "to-do"}}},
		{Number: 3, Labels: []Label{{Name: "to-do"}}},
	}

	selected := SelectIssues(issues, []state.Session{
		{Repo: "owner/repo", IssueNumber: 1, Status: state.SessionStatusBlocked},
		{Repo: "owner/repo", IssueNumber: 2, Status: state.SessionStatusSuccess, PullRequestState: "OPEN"},
	}, state.WatchTarget{Repo: "owner/repo", Labels: []string{"to-do"}}, 2)
	if len(selected) != 1 || selected[0].Number != 3 {
		t.Fatalf("unexpected selected issues: %#v", selected)
	}
}

func TestSelectIssuesSkipsSessionAfterStaleAutoRestartLimitReached(t *testing.T) {
	issues := []Issue{
		{Number: 1, Labels: []Label{{Name: "to-do"}}},
		{Number: 2, Labels: []Label{{Name: "to-do"}}},
	}

	selected := SelectIssues(issues, []state.Session{{
		Repo:                      "owner/repo",
		IssueNumber:               1,
		Status:                    state.SessionStatusFailed,
		StaleAutoRestartStoppedAt: "2026-03-10T15:00:00Z",
	}}, state.WatchTarget{Repo: "owner/repo", Labels: []string{"to-do"}}, 2)
	if len(selected) != 1 || selected[0].Number != 2 {
		t.Fatalf("unexpected selected issues: %#v", selected)
	}
}

func cleanedUpBlockedMaintenanceSession(issueNumber int, lastStage, prState string, prNumber int) state.Session {
	return state.Session{
		Repo:               "owner/repo",
		IssueNumber:        issueNumber,
		Status:             state.SessionStatusFailed,
		Branch:             "vigilante/issue-1",
		CleanupCompletedAt: "2026-04-22T18:00:00Z",
		LastCleanupSource:  "blocked_inactivity_timeout",
		LastBlockedStage:   lastStage,
		PullRequestNumber:  prNumber,
		PullRequestState:   prState,
	}
}

func TestSelectIssuesSkipsCleanedUpBlockedPRMaintenanceSessionWithOpenPR(t *testing.T) {
	issues := []Issue{
		{Number: 1, Labels: []Label{{Name: "to-do"}}},
		{Number: 2, Labels: []Label{{Name: "to-do"}}},
	}

	selected := SelectIssues(issues, []state.Session{
		cleanedUpBlockedMaintenanceSession(1, "pr_maintenance", "OPEN", 351),
	}, state.WatchTarget{Repo: "owner/repo", Labels: []string{"to-do"}}, 2)
	if len(selected) != 1 || selected[0].Number != 2 {
		t.Fatalf("expected issue 1 to be suppressed while PR #351 is open, got %#v", selected)
	}
}

func TestSelectIssuesSkipsCleanedUpBlockedCIRemediationSessionWithOpenPR(t *testing.T) {
	issues := []Issue{
		{Number: 1, Labels: []Label{{Name: "to-do"}}},
		{Number: 2, Labels: []Label{{Name: "to-do"}}},
	}

	selected := SelectIssues(issues, []state.Session{
		cleanedUpBlockedMaintenanceSession(1, "ci_remediation", "OPEN", 351),
	}, state.WatchTarget{Repo: "owner/repo", Labels: []string{"to-do"}}, 2)
	if len(selected) != 1 || selected[0].Number != 2 {
		t.Fatalf("expected ci_remediation cleanup to suppress redispatch, got %#v", selected)
	}
}

func TestSelectIssuesSkipsCleanedUpBlockedConflictResolutionSessionWithOpenPR(t *testing.T) {
	issues := []Issue{
		{Number: 1, Labels: []Label{{Name: "to-do"}}},
		{Number: 2, Labels: []Label{{Name: "to-do"}}},
	}

	selected := SelectIssues(issues, []state.Session{
		cleanedUpBlockedMaintenanceSession(1, "conflict_resolution", "open", 351),
	}, state.WatchTarget{Repo: "owner/repo", Labels: []string{"to-do"}}, 2)
	if len(selected) != 1 || selected[0].Number != 2 {
		t.Fatalf("expected conflict_resolution cleanup to suppress redispatch, got %#v", selected)
	}
}

func TestSelectIssuesAllowsRedispatchForCleanedUpBlockedPRMaintenanceSessionWhenPRMerged(t *testing.T) {
	issues := []Issue{
		{Number: 1, Labels: []Label{{Name: "to-do"}}},
	}

	selected := SelectIssues(issues, []state.Session{
		cleanedUpBlockedMaintenanceSession(1, "pr_maintenance", "MERGED", 351),
	}, state.WatchTarget{Repo: "owner/repo", Labels: []string{"to-do"}}, 2)
	if len(selected) != 1 || selected[0].Number != 1 {
		t.Fatalf("expected issue 1 eligible after PR merged, got %#v", selected)
	}
}

func TestSelectIssuesAllowsRedispatchForCleanedUpBlockedPRMaintenanceSessionWhenPRClosed(t *testing.T) {
	issues := []Issue{
		{Number: 1, Labels: []Label{{Name: "to-do"}}},
	}

	selected := SelectIssues(issues, []state.Session{
		cleanedUpBlockedMaintenanceSession(1, "pr_maintenance", "CLOSED", 351),
	}, state.WatchTarget{Repo: "owner/repo", Labels: []string{"to-do"}}, 2)
	if len(selected) != 1 || selected[0].Number != 1 {
		t.Fatalf("expected issue 1 eligible after PR closed, got %#v", selected)
	}
}

func TestSelectIssuesAllowsRedispatchForCleanedUpBlockedPRMaintenanceSessionWithoutPR(t *testing.T) {
	issues := []Issue{
		{Number: 1, Labels: []Label{{Name: "to-do"}}},
	}

	selected := SelectIssues(issues, []state.Session{
		cleanedUpBlockedMaintenanceSession(1, "pr_maintenance", "", 0),
	}, state.WatchTarget{Repo: "owner/repo", Labels: []string{"to-do"}}, 2)
	if len(selected) != 1 || selected[0].Number != 1 {
		t.Fatalf("expected issue 1 eligible when no PR is tracked, got %#v", selected)
	}
}

func TestSelectIssuesAllowsRedispatchForCleanedUpBlockedNonMaintenanceSession(t *testing.T) {
	issues := []Issue{
		{Number: 1, Labels: []Label{{Name: "to-do"}}},
	}

	selected := SelectIssues(issues, []state.Session{
		cleanedUpBlockedMaintenanceSession(1, "issue_execution", "OPEN", 99),
	}, state.WatchTarget{Repo: "owner/repo", Labels: []string{"to-do"}}, 2)
	if len(selected) != 1 || selected[0].Number != 1 {
		t.Fatalf("expected non-maintenance cleanup to remain eligible, got %#v", selected)
	}
}

func TestSelectIssuesAllowsRedispatchForCleanedUpFailedSessionWithoutBlockedStageHistory(t *testing.T) {
	issues := []Issue{
		{Number: 1, Labels: []Label{{Name: "to-do"}}},
	}

	selected := SelectIssues(issues, []state.Session{{
		Repo:               "owner/repo",
		IssueNumber:        1,
		Status:             state.SessionStatusFailed,
		CleanupCompletedAt: "2026-04-22T18:00:00Z",
	}}, state.WatchTarget{Repo: "owner/repo", Labels: []string{"to-do"}}, 2)
	if len(selected) != 1 || selected[0].Number != 1 {
		t.Fatalf("expected plain failed+cleaned session to remain eligible, got %#v", selected)
	}
}

func TestListOpenIssuesSupportsExplicitAssignee(t *testing.T) {
	runner := testutil.FakeRunner{
		Outputs: map[string]string{
			"gh issue list --repo owner/repo --state open --assignee nicobistolfi --json number,title,createdAt,url,labels": `[{"number":3,"title":"mine","createdAt":"2026-03-08T12:00:00Z","url":"u3","labels":[]}]`,
		},
	}

	issues, err := ListOpenIssues(context.Background(), runner, "owner/repo", "nicobistolfi")
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 1 || issues[0].Number != 3 {
		t.Fatalf("unexpected issues: %#v", issues)
	}
}

func TestListOpenIssuesAllowsNoAssigneeFilter(t *testing.T) {
	runner := testutil.FakeRunner{
		Outputs: map[string]string{
			"gh issue list --repo owner/repo --state open --json number,title,createdAt,url,labels": `[{"number":4,"title":"unassigned","createdAt":"2026-03-08T12:00:00Z","url":"u4","labels":[]}]`,
		},
	}

	issues, err := ListOpenIssues(context.Background(), runner, "owner/repo", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 1 || issues[0].Number != 4 {
		t.Fatalf("unexpected issues: %#v", issues)
	}
}

func TestListOpenIssuesReturnsErrorWhenResolvingMeFails(t *testing.T) {
	runner := testutil.FakeRunner{
		Errors: map[string]error{
			"gh api user --jq .login": context.DeadlineExceeded,
		},
	}

	_, err := ListOpenIssues(context.Background(), runner, "owner/repo", "me")
	if err == nil {
		t.Fatal("expected resolution error")
	}
	if got := err.Error(); got != `resolve assignee "me": context deadline exceeded` {
		t.Fatalf("unexpected error: %s", got)
	}
}

func TestGetRateLimitSnapshot(t *testing.T) {
	runner := testutil.FakeRunner{
		Outputs: map[string]string{
			"gh api /rate_limit": `{"resources":{"core":{"limit":5000,"remaining":95,"reset":1773961151},"rate":{"limit":5000,"remaining":95,"reset":1773961151},"graphql":{"limit":5000,"remaining":4557,"reset":1773961792},"search":{"limit":30,"remaining":30,"reset":1773961093}}}`,
		},
	}

	snapshot, err := GetRateLimitSnapshot(context.Background(), runner)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Core.Limit != 5000 || snapshot.Core.Remaining != 95 {
		t.Fatalf("unexpected core snapshot: %#v", snapshot.Core)
	}
	if snapshot.GraphQL.Remaining != 4557 || snapshot.Search.Limit != 30 {
		t.Fatalf("unexpected snapshot: %#v", snapshot)
	}
	if snapshot.Core.ResetAt.IsZero() || snapshot.Rate.ResetAt.IsZero() {
		t.Fatalf("expected reset timestamps: %#v", snapshot)
	}
}

func TestGetIssueDetailsPreservesUnavailableOutputOnCommandFailure(t *testing.T) {
	runner := testutil.FakeRunner{
		ErrorOutputs: map[string]string{
			"gh api repos/owner/repo/issues/7": "gh: HTTP 404: Not Found (https://api.github.com/repos/owner/repo/issues/7)\n",
		},
		Errors: map[string]error{
			"gh api repos/owner/repo/issues/7": errors.New("gh [api repos/owner/repo/issues/7]: exit status 1"),
		},
	}

	_, err := GetIssueDetails(context.Background(), runner, "owner/repo", 7)
	if err == nil {
		t.Fatal("expected issue details lookup to fail")
	}
	if !IsIssueUnavailableError(err) {
		t.Fatalf("expected unavailable detector to match wrapped command output, got %v", err)
	}
	if !strings.Contains(err.Error(), "HTTP 404") {
		t.Fatalf("expected error to retain command output, got %v", err)
	}
}

func TestSyncIssueLabelsAddsAndRemovesManagedLabels(t *testing.T) {
	runner := testutil.FakeRunner{
		Outputs: map[string]string{
			"gh issue edit --repo owner/repo 7 --add-label vigilante:blocked --add-label vigilante:needs-provider-fix --remove-label vigilante:running": "ok",
		},
	}

	err := SyncIssueLabels(
		context.Background(),
		runner,
		"owner/repo",
		7,
		[]Label{{Name: "bug"}, {Name: "vigilante:running"}},
		[]string{"vigilante:blocked", "vigilante:needs-provider-fix"},
		[]string{"vigilante:queued", "vigilante:running", "vigilante:blocked", "vigilante:needs-provider-fix"},
	)
	if err != nil {
		t.Fatal(err)
	}
}

func TestSyncIssueLabelsNoopsWhenManagedStateAlreadyMatches(t *testing.T) {
	runner := testutil.FakeRunner{}

	err := SyncIssueLabels(
		context.Background(),
		runner,
		"owner/repo",
		7,
		[]Label{{Name: "bug"}, {Name: "vigilante:running"}},
		[]string{"vigilante:running"},
		[]string{"vigilante:queued", "vigilante:running", "vigilante:blocked"},
	)
	if err != nil {
		t.Fatal(err)
	}
}

func TestEnsureRepositoryLabelsCreatesMissingLabelsFromManifest(t *testing.T) {
	runner := testutil.FakeRunner{
		Outputs: map[string]string{
			"gh api repos/owner/repo/labels?per_page=100": `[{"name":"bug"},{"name":"vigilante:queued"}]`,
			"gh api --method POST repos/owner/repo/labels -f name=vigilante:running -f color=0E8A16 -f description=A coding-agent session is currently executing for the issue.": "ok",
		},
	}

	err := EnsureRepositoryLabels(
		context.Background(),
		runner,
		"owner/repo",
		[]RepositoryLabelSpec{
			{Name: "vigilante:queued", Color: "BFDADC", Description: "The issue is eligible for dispatch and waiting for a worker slot."},
			{Name: "vigilante:running", Color: "0E8A16", Description: "A coding-agent session is currently executing for the issue."},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
}

func TestEnsureRepositoryLabelsNoopsWhenLabelsAlreadyExist(t *testing.T) {
	runner := testutil.FakeRunner{
		Outputs: map[string]string{
			"gh api repos/owner/repo/labels?per_page=100": `[{"name":"vigilante:queued"},{"name":"vigilante:running"}]`,
		},
	}

	err := EnsureRepositoryLabels(
		context.Background(),
		runner,
		"owner/repo",
		[]RepositoryLabelSpec{
			{Name: "vigilante:queued", Color: "BFDADC", Description: "The issue is eligible for dispatch and waiting for a worker slot."},
			{Name: "vigilante:running", Color: "0E8A16", Description: "A coding-agent session is currently executing for the issue."},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
}

func TestEnsureRepositoryLabelsSurfacesProvisioningFailure(t *testing.T) {
	runner := testutil.FakeRunner{
		Outputs: map[string]string{
			"gh api repos/owner/repo/labels?per_page=100": `[]`,
		},
		Errors: map[string]error{
			"gh api --method POST repos/owner/repo/labels -f name=vigilante:queued -f color=BFDADC -f description=The issue is eligible for dispatch and waiting for a worker slot.": context.DeadlineExceeded,
		},
	}

	err := EnsureRepositoryLabels(
		context.Background(),
		runner,
		"owner/repo",
		[]RepositoryLabelSpec{
			{Name: "vigilante:queued", Color: "BFDADC", Description: "The issue is eligible for dispatch and waiting for a worker slot."},
		},
	)
	if err == nil {
		t.Fatal("expected provisioning error")
	}
	if got := err.Error(); got != `create repository label "vigilante:queued": context deadline exceeded` {
		t.Fatalf("unexpected error: %s", got)
	}
}

func TestFindPullRequestForBranch(t *testing.T) {
	runner := testutil.FakeRunner{
		Outputs: map[string]string{
			"gh pr list --repo owner/repo --head vigilante/issue-7 --state all --json number,url,state,mergedAt": `[{"number":17,"url":"https://github.com/owner/repo/pull/17","state":"MERGED","mergedAt":"2026-03-10T14:00:00Z"}]`,
		},
	}

	pr, err := FindPullRequestForBranch(context.Background(), runner, "owner/repo", "vigilante/issue-7")
	if err != nil {
		t.Fatal(err)
	}
	if pr == nil {
		t.Fatal("expected pull request")
	}
	if pr.Number != 17 || pr.URL != "https://github.com/owner/repo/pull/17" {
		t.Fatalf("unexpected pull request: %#v", pr)
	}
	if pr.State != "MERGED" {
		t.Fatalf("unexpected pull request state: %#v", pr)
	}
	if pr.MergedAt == nil || !pr.MergedAt.Equal(time.Date(2026, 3, 10, 14, 0, 0, 0, time.UTC)) {
		t.Fatalf("unexpected merged time: %#v", pr.MergedAt)
	}
}

func TestGetPullRequestDetails(t *testing.T) {
	runner := testutil.FakeRunner{
		Outputs: map[string]string{
			"gh pr view --repo owner/repo 17 --json number,title,body,url,state,mergedAt,labels,isDraft,mergeable,mergeStateStatus,reviewDecision,statusCheckRollup,baseRefName": `{"number":17,"title":"Feature","body":"PR body","url":"https://github.com/owner/repo/pull/17","state":"OPEN","mergedAt":null,"labels":[{"name":"automerge"}],"isDraft":false,"mergeable":"MERGEABLE","mergeStateStatus":"CLEAN","reviewDecision":"APPROVED","statusCheckRollup":[{"context":"test","state":"COMPLETED","conclusion":"SUCCESS"}],"baseRefName":"develop"}`,
		},
	}

	pr, err := GetPullRequestDetails(context.Background(), runner, "owner/repo", 17)
	if err != nil {
		t.Fatal(err)
	}
	if pr.Number != 17 || pr.Title != "Feature" || pr.Mergeable != "MERGEABLE" || pr.MergeStateStatus != "CLEAN" || pr.ReviewDecision != "APPROVED" {
		t.Fatalf("unexpected pull request details: %#v", pr)
	}
	if pr.BaseRefName != "develop" {
		t.Fatalf("unexpected pull request base: %#v", pr)
	}
	if len(pr.Labels) != 1 || pr.Labels[0].Name != "automerge" {
		t.Fatalf("expected automerge label, got: %#v", pr.Labels)
	}
	if len(pr.StatusCheckRollup) != 1 || pr.StatusCheckRollup[0].Conclusion != "SUCCESS" {
		t.Fatalf("unexpected status checks: %#v", pr.StatusCheckRollup)
	}
}

func TestMergePullRequestSquash(t *testing.T) {
	runner := testutil.FakeRunner{
		Outputs: map[string]string{
			"gh pr merge --repo owner/repo 17 --squash --delete-branch": "ok",
		},
	}

	if err := MergePullRequestSquash(context.Background(), runner, "owner/repo", 17); err != nil {
		t.Fatal(err)
	}
}

func TestCommentOnIssueSanitizesAgentBranding(t *testing.T) {
	runner := testutil.FakeRunner{
		Outputs: map[string]string{
			"gh issue comment --repo owner/repo 17 --body Clean summary": "ok",
		},
	}

	body := "Clean summary\n\nGenerated with Claude Code"
	if err := CommentOnIssue(context.Background(), runner, "owner/repo", 17, body); err != nil {
		t.Fatal(err)
	}
}

func TestFindCleanupComment(t *testing.T) {
	now := time.Date(2026, 3, 12, 12, 0, 0, 0, time.UTC)
	comments := []IssueComment{
		{ID: 10, Body: "hello", CreatedAt: now.Add(-2 * time.Minute)},
		{ID: 11, Body: "@vigilanteai cleanup", CreatedAt: now.Add(-1 * time.Minute)},
	}

	comment := FindCleanupComment(comments, 0)
	if comment == nil || comment.ID != 11 {
		t.Fatalf("expected cleanup comment to be found, got: %#v", comment)
	}
	if comment := FindCleanupComment(comments, 11); comment != nil {
		t.Fatalf("expected claimed cleanup comment to be ignored, got: %#v", comment)
	}
}

func TestFindRecreateComment(t *testing.T) {
	now := time.Date(2026, 3, 12, 12, 0, 0, 0, time.UTC)
	comments := []IssueComment{
		{ID: 10, Body: "hello", CreatedAt: now.Add(-2 * time.Minute)},
		{ID: 11, Body: "@vigilanteai recreate", CreatedAt: now.Add(-1 * time.Minute)},
	}

	comment := FindRecreateComment(comments, 0)
	if comment == nil || comment.ID != 11 {
		t.Fatalf("expected recreate comment to be found, got: %#v", comment)
	}
	if comment := FindRecreateComment(comments, 11); comment != nil {
		t.Fatalf("expected claimed recreate comment to be ignored, got: %#v", comment)
	}
}

func TestIsKnownVigilanteCommandCommentIncludesRecreate(t *testing.T) {
	if !IsKnownVigilanteCommandComment("@vigilanteai recreate") {
		t.Fatal("expected @vigilanteai recreate to be a known command")
	}
	if !IsKnownVigilanteCommandComment("  @vigilanteai   recreate  ") {
		t.Fatal("expected whitespace-padded @vigilanteai recreate to be a known command")
	}
}

func TestFindIterationCommentSkipsKnownCommands(t *testing.T) {
	now := time.Date(2026, 3, 12, 12, 0, 0, 0, time.UTC)
	comments := []IssueComment{
		{ID: 10, Body: "@vigilanteai cleanup", CreatedAt: now.Add(-2 * time.Minute)},
		{ID: 11, Body: "@vigilanteai please adjust the copy", CreatedAt: now.Add(-1 * time.Minute)},
	}

	comment := FindIterationComment(comments, 0, "")
	if comment == nil || comment.ID != 11 {
		t.Fatalf("expected iteration comment to be found, got: %#v", comment)
	}
	if comment := FindIterationComment(comments, 11, now.Add(-1*time.Minute).Format(time.RFC3339)); comment != nil {
		t.Fatalf("expected claimed iteration comment to be ignored, got: %#v", comment)
	}
}

func TestFindIterationCommentTreatsOlderHistoryAsConsumed(t *testing.T) {
	now := time.Date(2026, 3, 12, 12, 0, 0, 0, time.UTC)
	comments := []IssueComment{
		{ID: 10, Body: "@vigilanteai older request", CreatedAt: now.Add(-3 * time.Minute)},
		{ID: 11, Body: "@vigilanteai claimed request", CreatedAt: now.Add(-2 * time.Minute)},
		{ID: 12, Body: "@vigilanteai newer request", CreatedAt: now.Add(-1 * time.Minute)},
	}

	comment := FindIterationComment(comments, 11, now.Add(-2*time.Minute).Format(time.RFC3339))
	if comment == nil || comment.ID != 12 {
		t.Fatalf("expected only the newer iteration comment to be eligible, got %#v", comment)
	}

	if comment := FindIterationComment(comments[:2], 11, now.Add(-2*time.Minute).Format(time.RFC3339)); comment != nil {
		t.Fatalf("expected historical iteration comments to stay consumed, got %#v", comment)
	}
}

func TestFindIterationCommentUsesIDTieBreakerAtClaimTimestamp(t *testing.T) {
	now := time.Date(2026, 3, 12, 12, 0, 0, 0, time.UTC)
	claimedAt := now.Add(-1 * time.Minute)
	comments := []IssueComment{
		{ID: 10, Body: "@vigilanteai already consumed", CreatedAt: claimedAt},
		{ID: 11, Body: "@vigilanteai newer same-second request", CreatedAt: claimedAt},
	}

	comment := FindIterationComment(comments, 10, claimedAt.Format(time.RFC3339))
	if comment == nil || comment.ID != 11 {
		t.Fatalf("expected newer same-timestamp iteration comment to be eligible, got %#v", comment)
	}
}

func TestAssigneeIterationCommentsFiltersByAuthor(t *testing.T) {
	now := time.Date(2026, 3, 12, 12, 0, 0, 0, time.UTC)
	comments := []IssueComment{
		{ID: 10, Body: "@vigilanteai first pass", CreatedAt: now.Add(-3 * time.Minute), User: struct {
			Login string `json:"login"`
		}{Login: "nicobistolfi"}},
		{ID: 11, Body: "@vigilanteai resume", CreatedAt: now.Add(-2 * time.Minute), User: struct {
			Login string `json:"login"`
		}{Login: "nicobistolfi"}},
		{ID: 12, Body: "@vigilanteai second pass", CreatedAt: now.Add(-1 * time.Minute), User: struct {
			Login string `json:"login"`
		}{Login: "someoneelse"}},
	}

	filtered := AssigneeIterationComments(comments, []string{"nicobistolfi"})
	if len(filtered) != 1 || filtered[0].ID != 10 {
		t.Fatalf("unexpected assignee iteration comments: %#v", filtered)
	}
}

func TestLatestUserCommentTimeIgnoresAutomationComments(t *testing.T) {
	now := time.Date(2026, 3, 12, 12, 0, 0, 0, time.UTC)
	comments := []IssueComment{
		{ID: 10, Body: FormatProgressComment(ProgressComment{Stage: "Blocked", Emoji: "🧱", Percent: 80, ETAMinutes: 5, Items: []string{"Agent update."}}), CreatedAt: now.Add(-3 * time.Minute)},
		{ID: 11, Body: "Can you pick this back up tomorrow?", CreatedAt: now.Add(-2 * time.Minute)},
		{ID: 12, Body: "## 🕹️ Coding Agent Launched: Codex\n\nWorking branch: `vigilante/issue-129`\n\nImplementation is in progress.", CreatedAt: now.Add(-1 * time.Minute)},
	}

	got := LatestUserCommentTime(comments)
	want := now.Add(-2 * time.Minute)
	if !got.Equal(want) {
		t.Fatalf("expected latest user comment at %s, got %s", want, got)
	}
}

func TestListPullRequestReviewComments(t *testing.T) {
	runner := testutil.FakeRunner{
		Outputs: map[string]string{
			"gh api repos/owner/repo/pulls/12/comments --paginate": `[{"id":100,"body":"@vigilanteai resume","created_at":"2026-03-12T12:00:00Z","user":{"login":"alice"}},{"id":101,"body":"looks good","created_at":"2026-03-12T12:01:00Z","user":{"login":"bob"}}]`,
		},
	}

	comments, err := ListPullRequestReviewComments(context.Background(), runner, "owner/repo", 12)
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != 2 {
		t.Fatalf("expected 2 review comments, got %d", len(comments))
	}
	if comments[0].ID != 100 || comments[0].User.Login != "alice" {
		t.Fatalf("unexpected first review comment: %#v", comments[0])
	}
	if comments[1].ID != 101 || comments[1].Body != "looks good" {
		t.Fatalf("unexpected second review comment: %#v", comments[1])
	}
}

func TestListPullRequestCommentsForPolling(t *testing.T) {
	runner := testutil.FakeRunner{
		Outputs: map[string]string{
			"gh api repos/owner/repo/issues/12/comments": `[{"id":200,"body":"@vigilanteai cleanup","created_at":"2026-03-12T12:00:00Z","user":{"login":"alice"}}]`,
		},
	}

	comments, err := ListPullRequestCommentsForPolling(context.Background(), runner, "owner/repo", 12, "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != 1 || comments[0].ID != 200 {
		t.Fatalf("unexpected PR comments: %#v", comments)
	}
}

func TestMergeCommentSurfacesDeduplicatesByID(t *testing.T) {
	now := time.Date(2026, 3, 12, 12, 0, 0, 0, time.UTC)
	issueComments := []IssueComment{
		{ID: 10, Body: "hello", CreatedAt: now.Add(-3 * time.Minute)},
		{ID: 11, Body: "@vigilanteai resume", CreatedAt: now.Add(-2 * time.Minute)},
	}
	prComments := []IssueComment{
		{ID: 11, Body: "@vigilanteai resume", CreatedAt: now.Add(-2 * time.Minute)}, // duplicate
		{ID: 20, Body: "@vigilanteai cleanup", CreatedAt: now.Add(-1 * time.Minute)},
	}
	reviewComments := []IssueComment{
		{ID: 30, Body: "inline feedback", CreatedAt: now.Add(-30 * time.Second)},
	}

	merged := MergeCommentSurfaces(issueComments, prComments, reviewComments)
	if len(merged) != 4 {
		t.Fatalf("expected 4 merged comments after dedup, got %d: %#v", len(merged), merged)
	}
	// Verify chronological order
	for i := 1; i < len(merged); i++ {
		if merged[i].CreatedAt.Before(merged[i-1].CreatedAt) {
			t.Fatalf("merged comments not in chronological order at index %d", i)
		}
	}
	// Verify IDs
	wantIDs := []int64{10, 11, 20, 30}
	for i, want := range wantIDs {
		if merged[i].ID != want {
			t.Fatalf("expected merged[%d].ID = %d, got %d", i, want, merged[i].ID)
		}
	}
}

func TestMergeCommentSurfacesHandlesEmptySurfaces(t *testing.T) {
	now := time.Date(2026, 3, 12, 12, 0, 0, 0, time.UTC)
	comments := []IssueComment{
		{ID: 10, Body: "hello", CreatedAt: now},
	}

	merged := MergeCommentSurfaces(comments, nil, nil)
	if len(merged) != 1 || merged[0].ID != 10 {
		t.Fatalf("unexpected merged result: %#v", merged)
	}

	merged = MergeCommentSurfaces(nil, nil)
	if len(merged) != 0 {
		t.Fatalf("expected empty merge result, got %#v", merged)
	}
}

func TestFindResumeCommentAcrossPRSurface(t *testing.T) {
	now := time.Date(2026, 3, 12, 12, 0, 0, 0, time.UTC)
	issueComments := []IssueComment{
		{ID: 10, Body: "working on it", CreatedAt: now.Add(-2 * time.Minute)},
	}
	prComments := []IssueComment{
		{ID: 20, Body: "@vigilanteai resume", CreatedAt: now.Add(-1 * time.Minute)},
	}

	merged := MergeCommentSurfaces(issueComments, prComments)
	comment := FindResumeComment(merged, 0)
	if comment == nil || comment.ID != 20 {
		t.Fatalf("expected resume command from PR comments to be found, got %#v", comment)
	}
}

func TestFindCleanupCommentAcrossPRReviewSurface(t *testing.T) {
	now := time.Date(2026, 3, 12, 12, 0, 0, 0, time.UTC)
	issueComments := []IssueComment{
		{ID: 10, Body: "looks good", CreatedAt: now.Add(-2 * time.Minute)},
	}
	reviewComments := []IssueComment{
		{ID: 30, Body: "@vigilanteai cleanup", CreatedAt: now.Add(-1 * time.Minute)},
	}

	merged := MergeCommentSurfaces(issueComments, nil, reviewComments)
	comment := FindCleanupComment(merged, 0)
	if comment == nil || comment.ID != 30 {
		t.Fatalf("expected cleanup command from review comments to be found, got %#v", comment)
	}
}

func TestFindIterationCommentAcrossMergedSurfaces(t *testing.T) {
	now := time.Date(2026, 3, 12, 12, 0, 0, 0, time.UTC)
	issueComments := []IssueComment{
		{ID: 10, Body: "@vigilanteai please fix the tests", CreatedAt: now.Add(-3 * time.Minute)},
	}
	prComments := []IssueComment{
		{ID: 20, Body: "@vigilanteai also update the docs", CreatedAt: now.Add(-1 * time.Minute)},
	}

	merged := MergeCommentSurfaces(issueComments, prComments)
	// With no claimed comment, should find the latest iteration comment (from PR)
	comment := FindIterationComment(merged, 0, "")
	if comment == nil || comment.ID != 20 {
		t.Fatalf("expected iteration comment from PR, got %#v", comment)
	}

	// After claiming the PR comment, should find the issue comment
	comment = FindIterationComment(merged, 20, now.Add(-1*time.Minute).Format(time.RFC3339))
	if comment != nil {
		t.Fatalf("expected no new iteration comment after claiming latest, got %#v", comment)
	}
}

func TestMergeCommentSurfacesPreservesIDOrderAtSameTimestamp(t *testing.T) {
	now := time.Date(2026, 3, 12, 12, 0, 0, 0, time.UTC)
	comments1 := []IssueComment{
		{ID: 20, Body: "second", CreatedAt: now},
	}
	comments2 := []IssueComment{
		{ID: 10, Body: "first", CreatedAt: now},
	}

	merged := MergeCommentSurfaces(comments1, comments2)
	if len(merged) != 2 || merged[0].ID != 10 || merged[1].ID != 20 {
		t.Fatalf("expected ID-ordered tie-breaking, got %#v", merged)
	}
}

func TestCommandsFromMultipleSessionsRouteCorrectly(t *testing.T) {
	now := time.Date(2026, 3, 12, 12, 0, 0, 0, time.UTC)

	session1Comments := []IssueComment{
		{ID: 10, Body: "@vigilanteai resume", CreatedAt: now.Add(-2 * time.Minute)},
	}
	session2Comments := []IssueComment{
		{ID: 20, Body: "@vigilanteai resume", CreatedAt: now.Add(-1 * time.Minute)},
	}

	// Session 1 already claimed comment 10
	comment1 := FindResumeComment(session1Comments, 10)
	if comment1 != nil {
		t.Fatalf("session 1 should not find new resume after claiming 10, got %#v", comment1)
	}

	// Session 2 has not claimed any comment yet
	comment2 := FindResumeComment(session2Comments, 0)
	if comment2 == nil || comment2.ID != 20 {
		t.Fatalf("session 2 should find resume comment 20, got %#v", comment2)
	}
}
