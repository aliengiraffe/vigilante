package app

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	ghcli "github.com/nicobistolfi/vigilante/internal/github"
	"github.com/nicobistolfi/vigilante/internal/state"
	"github.com/nicobistolfi/vigilante/internal/testutil"
)

func TestGroupSessionsActiveWork(t *testing.T) {
	now := time.Date(2026, 3, 19, 12, 0, 0, 0, time.UTC)
	sessions := []state.Session{
		{Repo: "owner/repo", IssueNumber: 1, Status: state.SessionStatusRunning, StartedAt: now.Add(-5 * time.Minute).Format(time.RFC3339)},
		{Repo: "owner/repo", IssueNumber: 2, Status: state.SessionStatusResuming, StartedAt: now.Add(-2 * time.Minute).Format(time.RFC3339)},
	}
	groups := groupSessions(sessions, now, 20*time.Minute)
	if len(groups) != 1 {
		t.Fatalf("expected 1 group, got %d", len(groups))
	}
	if groups[0].Label != "Actively working" {
		t.Fatalf("expected 'Actively working', got %q", groups[0].Label)
	}
	if len(groups[0].Sessions) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(groups[0].Sessions))
	}
}

func TestGroupSessionsPRTracking(t *testing.T) {
	now := time.Date(2026, 3, 19, 12, 0, 0, 0, time.UTC)
	sessions := []state.Session{
		{Repo: "owner/repo", IssueNumber: 10, Status: state.SessionStatusBlocked, BlockedStage: "pr_maintenance", PullRequestNumber: 11, PullRequestState: "OPEN", UpdatedAt: now.Add(-5 * time.Minute).Format(time.RFC3339)},
		{Repo: "owner/repo", IssueNumber: 20, Status: state.SessionStatusBlocked, BlockedStage: "ci_remediation", PullRequestNumber: 21, PullRequestState: "OPEN", UpdatedAt: now.Add(-3 * time.Minute).Format(time.RFC3339)},
	}
	groups := groupSessions(sessions, now, 20*time.Minute)
	if len(groups) != 1 {
		t.Fatalf("expected 1 group, got %d", len(groups))
	}
	if groups[0].Label != "Paused, tracking PRs" {
		t.Fatalf("expected 'Paused, tracking PRs', got %q", groups[0].Label)
	}
	if len(groups[0].Sessions) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(groups[0].Sessions))
	}
}

func TestGroupSessionsIssueTracking(t *testing.T) {
	now := time.Date(2026, 3, 19, 12, 0, 0, 0, time.UTC)
	sessions := []state.Session{
		{Repo: "owner/repo", IssueNumber: 30, Status: state.SessionStatusBlocked, BlockedStage: "issue_execution", UpdatedAt: now.Add(-5 * time.Minute).Format(time.RFC3339)},
	}
	groups := groupSessions(sessions, now, 20*time.Minute)
	if len(groups) != 1 {
		t.Fatalf("expected 1 group, got %d", len(groups))
	}
	if groups[0].Label != "Paused, tracking issues" {
		t.Fatalf("expected 'Paused, tracking issues', got %q", groups[0].Label)
	}
}

func TestGroupSessionsStaleRunning(t *testing.T) {
	now := time.Date(2026, 3, 19, 12, 0, 0, 0, time.UTC)
	sessions := []state.Session{
		{Repo: "owner/repo", IssueNumber: 40, Status: state.SessionStatusRunning, StartedAt: now.Add(-60 * time.Minute).Format(time.RFC3339)},
	}
	groups := groupSessions(sessions, now, 20*time.Minute)
	if len(groups) != 1 {
		t.Fatalf("expected 1 group, got %d", len(groups))
	}
	if groups[0].Label != "Stale sessions" {
		t.Fatalf("expected 'Stale sessions', got %q", groups[0].Label)
	}
}

func TestGroupSessionsStaleRunningWithHeartbeat(t *testing.T) {
	now := time.Date(2026, 3, 19, 12, 0, 0, 0, time.UTC)
	sessions := []state.Session{
		{Repo: "owner/repo", IssueNumber: 41, Status: state.SessionStatusRunning, StartedAt: now.Add(-60 * time.Minute).Format(time.RFC3339), LastHeartbeatAt: now.Add(-5 * time.Minute).Format(time.RFC3339)},
	}
	groups := groupSessions(sessions, now, 20*time.Minute)
	if len(groups) != 1 || groups[0].Label != "Actively working" {
		t.Fatalf("expected session with recent heartbeat to be active, got groups: %v", groups)
	}
}

func TestGroupSessionsStaleBlocked(t *testing.T) {
	now := time.Date(2026, 3, 19, 12, 0, 0, 0, time.UTC)
	sessions := []state.Session{
		{Repo: "owner/repo", IssueNumber: 50, Status: state.SessionStatusBlocked, BlockedStage: "pr_maintenance", UpdatedAt: now.Add(-50 * time.Minute).Format(time.RFC3339)},
	}
	groups := groupSessions(sessions, now, 20*time.Minute)
	if len(groups) != 1 {
		t.Fatalf("expected 1 group, got %d", len(groups))
	}
	if groups[0].Label != "Stale sessions" {
		t.Fatalf("expected 'Stale sessions', got %q", groups[0].Label)
	}
}

func TestGroupSessionsCompletedAndFailed(t *testing.T) {
	now := time.Date(2026, 3, 19, 12, 0, 0, 0, time.UTC)
	sessions := []state.Session{
		{Repo: "owner/repo", IssueNumber: 60, Status: state.SessionStatusSuccess},
		{Repo: "owner/repo", IssueNumber: 61, Status: state.SessionStatusFailed},
	}
	groups := groupSessions(sessions, now, 20*time.Minute)
	if len(groups) != 1 {
		t.Fatalf("expected 1 group, got %d", len(groups))
	}
	if groups[0].Label != "Completed / failed" {
		t.Fatalf("expected 'Completed / failed', got %q", groups[0].Label)
	}
	if len(groups[0].Sessions) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(groups[0].Sessions))
	}
}

func TestGroupSessionsClosedInCompletedGroup(t *testing.T) {
	now := time.Date(2026, 3, 19, 12, 0, 0, 0, time.UTC)
	sessions := []state.Session{
		{Repo: "owner/repo", IssueNumber: 70, Status: state.SessionStatusClosed},
		{Repo: "owner/repo", IssueNumber: 71, Status: state.SessionStatusSuccess},
	}
	groups := groupSessions(sessions, now, 20*time.Minute)
	if len(groups) != 1 {
		t.Fatalf("expected 1 group, got %d", len(groups))
	}
	if groups[0].Label != "Completed / failed" {
		t.Fatalf("expected 'Completed / failed', got %q", groups[0].Label)
	}
	if len(groups[0].Sessions) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(groups[0].Sessions))
	}
}

func TestGroupSessionsMixedPopulation(t *testing.T) {
	now := time.Date(2026, 3, 19, 12, 0, 0, 0, time.UTC)
	sessions := []state.Session{
		{Repo: "owner/repo", IssueNumber: 1, Status: state.SessionStatusRunning, StartedAt: now.Add(-5 * time.Minute).Format(time.RFC3339)},
		{Repo: "owner/repo", IssueNumber: 2, Status: state.SessionStatusBlocked, BlockedStage: "pr_maintenance", PullRequestNumber: 3, PullRequestState: "OPEN", UpdatedAt: now.Add(-5 * time.Minute).Format(time.RFC3339)},
		{Repo: "owner/repo", IssueNumber: 4, Status: state.SessionStatusBlocked, BlockedStage: "issue_execution", UpdatedAt: now.Add(-5 * time.Minute).Format(time.RFC3339)},
		{Repo: "owner/repo", IssueNumber: 5, Status: state.SessionStatusBlocked, BlockedStage: "pr_maintenance", UpdatedAt: now.Add(-50 * time.Minute).Format(time.RFC3339)},
		{Repo: "owner/repo", IssueNumber: 6, Status: state.SessionStatusSuccess},
		{Repo: "owner/repo", IssueNumber: 7, Status: state.SessionStatusFailed},
	}
	groups := groupSessions(sessions, now, 20*time.Minute)
	if len(groups) != 5 {
		t.Fatalf("expected 5 groups, got %d: %v", len(groups), groupLabels(groups))
	}
	expected := []string{
		"Actively working",
		"Paused, tracking PRs",
		"Paused, tracking issues",
		"Stale sessions",
		"Completed / failed",
	}
	for i, g := range groups {
		if g.Label != expected[i] {
			t.Errorf("group %d: expected %q, got %q", i, expected[i], g.Label)
		}
	}
}

func TestGroupSessionsEmptyInput(t *testing.T) {
	now := time.Date(2026, 3, 19, 12, 0, 0, 0, time.UTC)
	groups := groupSessions(nil, now, 20*time.Minute)
	if len(groups) != 0 {
		t.Fatalf("expected 0 groups, got %d", len(groups))
	}
}

func TestFormatSessionRowWithPR(t *testing.T) {
	s := state.Session{
		Repo:              "owner/repo",
		IssueNumber:       42,
		Status:            state.SessionStatusBlocked,
		PullRequestNumber: 43,
		PullRequestState:  "OPEN",
		BlockedStage:      "pr_maintenance",
	}
	row := formatSessionRow(s)
	if !strings.Contains(row, "Issue #42") {
		t.Errorf("expected issue number in row: %s", row)
	}
	if !strings.Contains(row, "PR #43 OPEN") {
		t.Errorf("expected PR info in row: %s", row)
	}
	if !strings.Contains(row, "stage pr_maintenance") {
		t.Errorf("expected stage in row: %s", row)
	}
}

func TestFormatSessionRowWithoutPR(t *testing.T) {
	s := state.Session{
		Repo:        "owner/repo",
		IssueNumber: 10,
		Status:      state.SessionStatusRunning,
	}
	row := formatSessionRow(s)
	if strings.Contains(row, "PR") {
		t.Errorf("did not expect PR info in row: %s", row)
	}
}

func TestIsPRTrackingOpenPR(t *testing.T) {
	s := state.Session{PullRequestNumber: 1, PullRequestState: "OPEN"}
	if !isPRTracking(s) {
		t.Error("expected open PR session to be PR tracking")
	}
}

func TestIsPRTrackingByStage(t *testing.T) {
	for _, stage := range []string{"pr_maintenance", "ci_remediation", "conflict_resolution"} {
		s := state.Session{BlockedStage: stage}
		if !isPRTracking(s) {
			t.Errorf("expected stage %q to be PR tracking", stage)
		}
	}
}

func TestIsPRTrackingIssueExecution(t *testing.T) {
	s := state.Session{BlockedStage: "issue_execution"}
	if isPRTracking(s) {
		t.Error("expected issue_execution to not be PR tracking")
	}
}

func TestStatusCommandOutputPreservesServiceState(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))

	unitPath := filepath.Join(home, ".config", "systemd", "user", "vigilante.service")
	if err := os.MkdirAll(filepath.Dir(unitPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(unitPath, []byte("unit"), 0o644); err != nil {
		t.Fatal(err)
	}

	vigilanteHome := filepath.Join(home, ".vigilante")
	if err := os.MkdirAll(vigilanteHome, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vigilanteHome, "sessions.json"), []byte("[]"), 0o644); err != nil {
		t.Fatal(err)
	}

	app := New()
	var stdout bytes.Buffer
	app.stdout = &stdout
	app.stderr = testutil.IODiscard{}
	app.env.OS = "linux"
	app.env.Runner = testutil.FakeRunner{
		Outputs: map[string]string{
			"systemctl --user show --property=LoadState,ActiveState vigilante.service": "LoadState=loaded\nActiveState=active\n",
		},
		Errors: map[string]error{
			"gh api /rate_limit": nil,
		},
	}

	exitCode := app.Run(context.Background(), []string{"status"})
	if exitCode != 0 {
		t.Fatalf("expected success exit code, got %d; stderr: %s; stdout: %s", exitCode, "", stdout.String())
	}
	for _, want := range []string{
		"state: running",
		"manager: systemd",
		"service: vigilante.service",
		"installed: yes",
		"running: yes",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("expected output to contain %q, got:\n%s", want, stdout.String())
		}
	}
}

func TestStatusCommandShowsSessionGroups(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))

	unitPath := filepath.Join(home, ".config", "systemd", "user", "vigilante.service")
	if err := os.MkdirAll(filepath.Dir(unitPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(unitPath, []byte("unit"), 0o644); err != nil {
		t.Fatal(err)
	}

	vigilanteHome := filepath.Join(home, ".vigilante")
	if err := os.MkdirAll(vigilanteHome, 0o755); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 3, 19, 12, 0, 0, 0, time.UTC)
	sessions := []state.Session{
		{Repo: "owner/repo", IssueNumber: 1, Status: state.SessionStatusRunning, StartedAt: now.Add(-5 * time.Minute).Format(time.RFC3339)},
		{Repo: "owner/repo", IssueNumber: 2, Status: state.SessionStatusBlocked, BlockedStage: "pr_maintenance", PullRequestNumber: 3, PullRequestState: "OPEN", UpdatedAt: now.Add(-5 * time.Minute).Format(time.RFC3339)},
		{Repo: "other/repo", IssueNumber: 4, Status: state.SessionStatusBlocked, BlockedStage: "issue_execution", UpdatedAt: now.Add(-5 * time.Minute).Format(time.RFC3339)},
	}
	sessionData, _ := json.Marshal(sessions)
	if err := os.WriteFile(filepath.Join(vigilanteHome, "sessions.json"), sessionData, 0o644); err != nil {
		t.Fatal(err)
	}

	app := New()
	app.clock = func() time.Time { return now }
	var stdout bytes.Buffer
	app.stdout = &stdout
	app.stderr = testutil.IODiscard{}
	app.env.OS = "linux"
	app.env.Runner = testutil.FakeRunner{
		Outputs: map[string]string{
			"systemctl --user show --property=LoadState,ActiveState vigilante.service": "LoadState=loaded\nActiveState=active\n",
		},
		Errors: map[string]error{
			"gh api /rate_limit": nil,
		},
	}

	exitCode := app.Run(context.Background(), []string{"status"})
	if exitCode != 0 {
		t.Fatalf("expected success, got %d; stdout: %s", exitCode, stdout.String())
	}
	output := stdout.String()
	for _, want := range []string{
		"Sessions: 3 total",
		"Actively working (1)",
		"Issue #1 in owner/repo: running",
		"Paused, tracking PRs (1)",
		"Issue #2 in owner/repo: blocked, PR #3 OPEN, stage pr_maintenance",
		"Paused, tracking issues (1)",
		"Issue #4 in other/repo: blocked, stage issue_execution",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("expected output to contain %q, got:\n%s", want, output)
		}
	}
}

func TestStatusCommandShowsRateLimits(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))

	unitPath := filepath.Join(home, ".config", "systemd", "user", "vigilante.service")
	if err := os.MkdirAll(filepath.Dir(unitPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(unitPath, []byte("unit"), 0o644); err != nil {
		t.Fatal(err)
	}

	vigilanteHome := filepath.Join(home, ".vigilante")
	if err := os.MkdirAll(vigilanteHome, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vigilanteHome, "sessions.json"), []byte("[]"), 0o644); err != nil {
		t.Fatal(err)
	}

	resetTime := time.Now().Add(30 * time.Minute).Unix()
	rateLimitJSON := rateLimitResponse(5000, 4800, resetTime, 5000, 4500, resetTime)

	app := New()
	var stdout bytes.Buffer
	app.stdout = &stdout
	app.stderr = testutil.IODiscard{}
	app.env.OS = "linux"
	app.env.Runner = testutil.FakeRunner{
		Outputs: map[string]string{
			"systemctl --user show --property=LoadState,ActiveState vigilante.service": "LoadState=loaded\nActiveState=active\n",
			"gh api /rate_limit": rateLimitJSON,
		},
	}

	exitCode := app.Run(context.Background(), []string{"status"})
	if exitCode != 0 {
		t.Fatalf("expected success, got %d", exitCode)
	}
	output := stdout.String()
	if !strings.Contains(output, "GitHub rate limits") {
		t.Errorf("expected rate limits section, got:\n%s", output)
	}
	if !strings.Contains(output, "core: 4800/5000 remaining") {
		t.Errorf("expected core rate limit info, got:\n%s", output)
	}
	if !strings.Contains(output, "graphql: 4500/5000 remaining") {
		t.Errorf("expected graphql rate limit info, got:\n%s", output)
	}
}

func TestStatusCommandRateLimitFailureGraceful(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))

	unitPath := filepath.Join(home, ".config", "systemd", "user", "vigilante.service")
	if err := os.MkdirAll(filepath.Dir(unitPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(unitPath, []byte("unit"), 0o644); err != nil {
		t.Fatal(err)
	}

	vigilanteHome := filepath.Join(home, ".vigilante")
	if err := os.MkdirAll(vigilanteHome, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vigilanteHome, "sessions.json"), []byte("[]"), 0o644); err != nil {
		t.Fatal(err)
	}

	app := New()
	var stdout bytes.Buffer
	app.stdout = &stdout
	app.stderr = testutil.IODiscard{}
	app.env.OS = "linux"
	app.env.Runner = testutil.FakeRunner{
		Outputs: map[string]string{
			"systemctl --user show --property=LoadState,ActiveState vigilante.service": "LoadState=loaded\nActiveState=active\n",
		},
	}

	exitCode := app.Run(context.Background(), []string{"status"})
	if exitCode != 0 {
		t.Fatalf("expected success even when rate limit fails, got %d", exitCode)
	}
	output := stdout.String()
	if !strings.Contains(output, "GitHub rate limits: unavailable") {
		t.Errorf("expected unavailable notice, got:\n%s", output)
	}
	if !strings.Contains(output, "Sessions: 0 total") {
		t.Errorf("expected session count, got:\n%s", output)
	}
}

func TestStatusCommandStaleSessionsShown(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))

	unitPath := filepath.Join(home, ".config", "systemd", "user", "vigilante.service")
	if err := os.MkdirAll(filepath.Dir(unitPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(unitPath, []byte("unit"), 0o644); err != nil {
		t.Fatal(err)
	}

	vigilanteHome := filepath.Join(home, ".vigilante")
	if err := os.MkdirAll(vigilanteHome, 0o755); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 3, 19, 12, 0, 0, 0, time.UTC)
	sessions := []state.Session{
		{Repo: "owner/repo", IssueNumber: 99, Status: state.SessionStatusRunning, StartedAt: now.Add(-2 * time.Hour).Format(time.RFC3339)},
	}
	sessionData, _ := json.Marshal(sessions)
	if err := os.WriteFile(filepath.Join(vigilanteHome, "sessions.json"), sessionData, 0o644); err != nil {
		t.Fatal(err)
	}

	app := New()
	app.clock = func() time.Time { return now }
	var stdout bytes.Buffer
	app.stdout = &stdout
	app.stderr = testutil.IODiscard{}
	app.env.OS = "linux"
	app.env.Runner = testutil.FakeRunner{
		Outputs: map[string]string{
			"systemctl --user show --property=LoadState,ActiveState vigilante.service": "LoadState=loaded\nActiveState=active\n",
		},
	}

	exitCode := app.Run(context.Background(), []string{"status"})
	if exitCode != 0 {
		t.Fatalf("expected success, got %d", exitCode)
	}
	output := stdout.String()
	if !strings.Contains(output, "Stale sessions (1)") {
		t.Errorf("expected stale sessions section, got:\n%s", output)
	}
	if !strings.Contains(output, "Issue #99") {
		t.Errorf("expected stale session row, got:\n%s", output)
	}
}

func TestWriteRateLimitSection(t *testing.T) {
	var buf bytes.Buffer
	snapshot := ghcli.RateLimitSnapshot{
		Core:    ghcli.RateLimitResource{Limit: 5000, Remaining: 4800, ResetAt: time.Now().Add(30 * time.Minute)},
		GraphQL: ghcli.RateLimitResource{Limit: 5000, Remaining: 4500, ResetAt: time.Now().Add(30 * time.Minute)},
		Search:  ghcli.RateLimitResource{Limit: 30, Remaining: 28, ResetAt: time.Now().Add(30 * time.Minute)},
	}
	writeRateLimitSection(&buf, snapshot)
	output := buf.String()
	if !strings.Contains(output, "core: 4800/5000 remaining") {
		t.Errorf("expected core info, got: %s", output)
	}
	if !strings.Contains(output, "graphql: 4500/5000 remaining") {
		t.Errorf("expected graphql info, got: %s", output)
	}
	if !strings.Contains(output, "search: 28/30 remaining") {
		t.Errorf("expected search info, got: %s", output)
	}
}

func TestWriteRateLimitUnavailable(t *testing.T) {
	var buf bytes.Buffer
	writeRateLimitUnavailable(&buf)
	if !strings.Contains(buf.String(), "GitHub rate limits: unavailable") {
		t.Errorf("unexpected output: %s", buf.String())
	}
}

func TestIsStaleBlockedUsesConfiguredTimeout(t *testing.T) {
	now := time.Date(2026, 3, 19, 12, 0, 0, 0, time.UTC)
	s := state.Session{
		Status:    state.SessionStatusBlocked,
		UpdatedAt: now.Add(-25 * time.Minute).Format(time.RFC3339),
	}
	// isStale receives the already-multiplied threshold.
	// groupSessions passes 2*inactivityTimeout as the staleBlockedThreshold.
	// 25 min > 20min threshold => stale
	if !isStale(s, now, 20*time.Minute) {
		t.Error("expected session to be stale with 20m threshold")
	}
	// 25 min < 40min threshold => not stale
	if isStale(s, now, 40*time.Minute) {
		t.Error("expected session to NOT be stale with 40m threshold")
	}
}

func TestPRTrackingDistinguishedFromIssueTracking(t *testing.T) {
	prSession := state.Session{
		Status:            state.SessionStatusBlocked,
		BlockedStage:      "pr_maintenance",
		PullRequestNumber: 5,
		PullRequestState:  "OPEN",
	}
	issueSession := state.Session{
		Status:       state.SessionStatusBlocked,
		BlockedStage: "issue_execution",
	}
	if !isPRTracking(prSession) {
		t.Error("expected PR session to be classified as PR tracking")
	}
	if isPRTracking(issueSession) {
		t.Error("expected issue session to NOT be classified as PR tracking")
	}
}

func groupLabels(groups []sessionGroup) []string {
	labels := make([]string, len(groups))
	for i, g := range groups {
		labels[i] = g.Label
	}
	return labels
}

func rateLimitResponse(coreLimit, coreRemaining int, coreReset int64, gqlLimit, gqlRemaining int, gqlReset int64) string {
	resp := map[string]interface{}{
		"resources": map[string]interface{}{
			"core": map[string]interface{}{
				"limit":     coreLimit,
				"remaining": coreRemaining,
				"reset":     coreReset,
			},
			"graphql": map[string]interface{}{
				"limit":     gqlLimit,
				"remaining": gqlRemaining,
				"reset":     gqlReset,
			},
			"search": map[string]interface{}{
				"limit":     30,
				"remaining": 28,
				"reset":     coreReset,
			},
		},
	}
	data, _ := json.Marshal(resp)
	return string(data)
}
