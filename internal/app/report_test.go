package app

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nicobistolfi/vigilante/internal/backend"
	"github.com/nicobistolfi/vigilante/internal/state"
	"github.com/nicobistolfi/vigilante/internal/testutil"
)

func makeComment(id int64, body string, createdAt time.Time) backend.WorkItemComment {
	return backend.WorkItemComment{
		ID:        id,
		Body:      body,
		CreatedAt: createdAt,
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestFindTimingMarkers_BothPresent(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	comments := []backend.WorkItemComment{
		makeComment(1, "## Vigilante Session Start\nWorking branch: `feat`", t0),
		makeComment(2, "Progress update", t0.Add(5*time.Minute)),
		makeComment(3, "## PR Opened\nhttps://github.com/owner/repo/pull/42", t0.Add(10*time.Minute)),
	}

	start, pr := findTimingMarkers(comments)
	if start == nil {
		t.Fatal("expected session start marker")
	}
	if pr == nil {
		t.Fatal("expected PR opened marker")
	}
	if start.ID != 1 {
		t.Errorf("expected session start comment ID 1, got %d", start.ID)
	}
	if pr.ID != 3 {
		t.Errorf("expected PR opened comment ID 3, got %d", pr.ID)
	}
}

func TestFindTimingMarkers_MissingSessionStart(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	comments := []backend.WorkItemComment{
		makeComment(1, "Some comment", t0),
		makeComment(2, "## PR Opened\nhttps://github.com/owner/repo/pull/42", t0.Add(10*time.Minute)),
	}

	start, pr := findTimingMarkers(comments)
	if start != nil {
		t.Error("expected no session start marker")
	}
	if pr != nil {
		t.Error("expected no PR opened marker when session start is missing")
	}
}

func TestFindTimingMarkers_MissingPROpened(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	comments := []backend.WorkItemComment{
		makeComment(1, "## Vigilante Session Start\nWorking branch: `feat`", t0),
		makeComment(2, "Some progress", t0.Add(5*time.Minute)),
	}

	start, pr := findTimingMarkers(comments)
	if start == nil {
		t.Fatal("expected session start marker")
	}
	if pr != nil {
		t.Error("expected no PR opened marker")
	}
}

func TestFindTimingMarkers_BothAbsent(t *testing.T) {
	comments := []backend.WorkItemComment{
		makeComment(1, "Hello", time.Now()),
		makeComment(2, "World", time.Now()),
	}
	start, pr := findTimingMarkers(comments)
	if start != nil || pr != nil {
		t.Error("expected no markers")
	}
}

func TestResolveCodingAgent_LabelOverridesDefault(t *testing.T) {
	labels := []backend.Label{{Name: "claude"}}
	agent := resolveCodingAgent(labels, "codex")
	if agent != "claude" {
		t.Errorf("expected claude, got %s", agent)
	}
}

func TestResolveCodingAgent_FallsBackToDefault(t *testing.T) {
	labels := []backend.Label{{Name: "bug"}, {Name: "enhancement"}}
	agent := resolveCodingAgent(labels, "gemini")
	if agent != "gemini" {
		t.Errorf("expected gemini, got %s", agent)
	}
}

func TestResolveCodingAgent_NoLabelsNoDefault(t *testing.T) {
	agent := resolveCodingAgent(nil, "")
	if agent != "" {
		t.Errorf("expected empty, got %s", agent)
	}
}

func TestResolveCodingAgent_AmbiguousLabelsUsesDefault(t *testing.T) {
	labels := []backend.Label{{Name: "claude"}, {Name: "codex"}}
	agent := resolveCodingAgent(labels, "gemini")
	if agent != "gemini" {
		t.Errorf("expected gemini fallback on ambiguous labels, got %s", agent)
	}
}

func TestCountFirstWordVigilanteComments(t *testing.T) {
	comments := []backend.WorkItemComment{
		makeComment(1, "@vigilanteai resume", time.Now()),
		makeComment(2, "@vigilanteai cleanup", time.Now()),
		makeComment(3, "Please check @vigilanteai", time.Now()),
		makeComment(4, "   @vigilanteai something", time.Now()),
		makeComment(5, "", time.Now()),
		makeComment(6, "No mention here", time.Now()),
	}
	count := countFirstWordVigilanteComments(comments)
	if count != 3 {
		t.Errorf("expected 3 first-word @vigilanteai comments, got %d", count)
	}
}

func TestExtractPRNumberFromComment(t *testing.T) {
	tests := []struct {
		body string
		repo string
		want int
	}{
		{"PR: https://github.com/owner/repo/pull/42", "owner/repo", 42},
		{"owner/repo/pull/99 opened", "owner/repo", 99},
		{"no PR here", "owner/repo", 0},
		{"other/repo/pull/10", "owner/repo", 0},
	}
	for _, tt := range tests {
		got := extractPRNumberFromComment(tt.body, tt.repo)
		if got != tt.want {
			t.Errorf("extractPRNumberFromComment(%q, %q) = %d, want %d", tt.body, tt.repo, got, tt.want)
		}
	}
}

func TestReportCSVHeaders(t *testing.T) {
	expected := []string{
		"repository",
		"issue_number",
		"issue_title",
		"issue_url",
		"issue_closed_at",
		"coding_agent",
		"session_start_comment_timestamp",
		"pr_opened_comment_timestamp",
		"session_start_to_pr_opened_seconds",
		"linked_pr_number",
		"linked_pr_url",
		"pr_commit_count",
		"pr_files_changed",
		"pr_lines_added",
		"first_word_vigilante_comment_count",
	}
	if len(reportCSVHeaders) != len(expected) {
		t.Fatalf("header count mismatch: got %d, want %d", len(reportCSVHeaders), len(expected))
	}
	for i, h := range expected {
		if reportCSVHeaders[i] != h {
			t.Errorf("header[%d] = %q, want %q", i, reportCSVHeaders[i], h)
		}
	}
}

func TestBuildReportRow_BothMarkers(t *testing.T) {
	t0 := time.Date(2026, 3, 15, 12, 0, 0, 0, time.UTC)
	comments := []backend.WorkItemComment{
		makeComment(1, "## Vigilante Session Start\nWorking branch: `feat`", t0),
		makeComment(2, "@vigilanteai resume", t0.Add(2*time.Minute)),
		makeComment(3, "## PR Opened\nhttps://github.com/owner/repo/pull/10", t0.Add(10*time.Minute)),
	}
	issue := closedIssue{
		Number:   7,
		Title:    "Test issue",
		URL:      "https://github.com/owner/repo/issues/7",
		ClosedAt: "2026-03-15T12:30:00Z",
		Labels:   []backend.Label{{Name: "claude"}},
	}

	prJSON := mustJSON(t, map[string]any{"commits": 3, "changed_files": 5, "additions": 100})
	timelineJSON := mustJSON(t, []linkedPREvent{{
		Event: "cross-referenced",
		Source: struct {
			Issue struct {
				Number      int    `json:"number"`
				URL         string `json:"html_url"`
				PullRequest *struct {
					URL string `json:"html_url"`
				} `json:"pull_request"`
			} `json:"issue"`
		}{
			Issue: struct {
				Number      int    `json:"number"`
				URL         string `json:"html_url"`
				PullRequest *struct {
					URL string `json:"html_url"`
				} `json:"pull_request"`
			}{
				Number: 10,
				URL:    "https://github.com/owner/repo/pull/10",
				PullRequest: &struct {
					URL string `json:"html_url"`
				}{URL: "https://github.com/owner/repo/pull/10"},
			},
		},
	}})

	runner := testutil.FakeRunner{
		Outputs: map[string]string{
			"gh api repos/owner/repo/issues/7/comments?per_page=100&page=1":                                                     mustJSON(t, comments),
			"gh api -H Accept: application/vnd.github.mockingbird-preview+json repos/owner/repo/issues/7/timeline?per_page=100": timelineJSON,
			"gh api repos/owner/repo/pulls/10": prJSON,
		},
	}

	row, err := buildReportRow(context.Background(), runner, "owner/repo", issue, "codex")
	if err != nil {
		t.Fatal(err)
	}

	if len(row) != len(reportCSVHeaders) {
		t.Fatalf("row length %d != headers %d", len(row), len(reportCSVHeaders))
	}

	// Verify key fields.
	if row[0] != "owner/repo" {
		t.Errorf("repository = %q", row[0])
	}
	if row[1] != "7" {
		t.Errorf("issue_number = %q", row[1])
	}
	if row[5] != "claude" {
		t.Errorf("coding_agent = %q, want claude", row[5])
	}
	if row[6] == "" {
		t.Error("session_start_comment_timestamp should not be empty")
	}
	if row[7] == "" {
		t.Error("pr_opened_comment_timestamp should not be empty")
	}
	if row[8] != "600" {
		t.Errorf("session_start_to_pr_opened_seconds = %q, want 600", row[8])
	}
	if row[9] != "10" {
		t.Errorf("linked_pr_number = %q, want 10", row[9])
	}
	if row[11] != "3" {
		t.Errorf("pr_commit_count = %q, want 3", row[11])
	}
	if row[12] != "5" {
		t.Errorf("pr_files_changed = %q, want 5", row[12])
	}
	if row[13] != "100" {
		t.Errorf("pr_lines_added = %q, want 100", row[13])
	}
	if row[14] != "1" {
		t.Errorf("first_word_vigilante_comment_count = %q, want 1", row[14])
	}
}

func TestBuildReportRow_MissingTimingData(t *testing.T) {
	comments := []backend.WorkItemComment{
		makeComment(1, "Just a normal comment", time.Now()),
	}
	issue := closedIssue{
		Number:   42,
		Title:    "No timing",
		URL:      "https://github.com/owner/repo/issues/42",
		ClosedAt: "2026-03-15T12:30:00Z",
	}

	runner := testutil.FakeRunner{
		Outputs: map[string]string{
			"gh api repos/owner/repo/issues/42/comments?per_page=100&page=1":                                                                  mustJSON(t, comments),
			fmt.Sprintf("gh api -H Accept: application/vnd.github.mockingbird-preview+json repos/owner/repo/issues/42/timeline?per_page=100"): "[]",
		},
	}

	row, err := buildReportRow(context.Background(), runner, "owner/repo", issue, "codex")
	if err != nil {
		t.Fatal(err)
	}

	if row[6] != "" {
		t.Errorf("session_start should be empty, got %q", row[6])
	}
	if row[7] != "" {
		t.Errorf("pr_opened should be empty, got %q", row[7])
	}
	if row[8] != "" {
		t.Errorf("duration should be empty, got %q", row[8])
	}
	if row[9] != "" {
		t.Errorf("linked_pr_number should be empty, got %q", row[9])
	}
}

func TestBuildReportRow_NoPR(t *testing.T) {
	t0 := time.Date(2026, 3, 15, 12, 0, 0, 0, time.UTC)
	comments := []backend.WorkItemComment{
		makeComment(1, "## Vigilante Session Start\nWorking branch: `feat`", t0),
	}
	issue := closedIssue{
		Number:   5,
		Title:    "No PR issue",
		URL:      "https://github.com/owner/repo/issues/5",
		ClosedAt: "2026-03-15T12:30:00Z",
	}

	runner := testutil.FakeRunner{
		Outputs: map[string]string{
			"gh api repos/owner/repo/issues/5/comments?per_page=100&page=1":                                                     mustJSON(t, comments),
			"gh api -H Accept: application/vnd.github.mockingbird-preview+json repos/owner/repo/issues/5/timeline?per_page=100": "[]",
		},
	}

	row, err := buildReportRow(context.Background(), runner, "owner/repo", issue, "codex")
	if err != nil {
		t.Fatal(err)
	}

	if row[9] != "" {
		t.Errorf("linked_pr_number should be empty, got %q", row[9])
	}
	if row[10] != "" {
		t.Errorf("linked_pr_url should be empty, got %q", row[10])
	}
	if row[11] != "" {
		t.Errorf("pr_commit_count should be empty, got %q", row[11])
	}
}

func TestGenerateReport_FullCSV(t *testing.T) {
	t.Setenv("VIGILANTE_HOME", t.TempDir())
	store := state.NewStore()
	if err := store.EnsureLayout(); err != nil {
		t.Fatal(err)
	}

	// Save a watch target so the default provider is resolved.
	if err := store.SaveWatchTargets([]state.WatchTarget{
		{Repo: "owner/repo", Provider: "codex", Path: "/tmp/repo"},
	}); err != nil {
		t.Fatal(err)
	}

	t0 := time.Date(2026, 3, 15, 12, 0, 0, 0, time.UTC)
	closedIssues := []closedIssue{
		{Number: 1, Title: "First", URL: "https://github.com/owner/repo/issues/1", ClosedAt: "2026-03-15T13:00:00Z", Labels: []backend.Label{{Name: "claude"}}},
		{Number: 2, Title: "Second", URL: "https://github.com/owner/repo/issues/2", ClosedAt: "2026-03-15T14:00:00Z"},
	}
	issue1Comments := []backend.WorkItemComment{
		makeComment(10, "## Vigilante Session Start\nWorking branch: `feat`", t0),
		makeComment(11, "## PR Opened\nhttps://github.com/owner/repo/pull/5", t0.Add(8*time.Minute)),
	}
	issue2Comments := []backend.WorkItemComment{
		makeComment(20, "@vigilanteai resume", t0),
		makeComment(21, "Some comment", t0.Add(1*time.Minute)),
	}

	runner := testutil.FakeRunner{
		Outputs: map[string]string{
			"gh api repos/owner/repo/issues?state=closed&per_page=100&page=1": mustJSON(t, closedIssues),
			// Issue 1 comments and timeline.
			"gh api repos/owner/repo/issues/1/comments?per_page=100&page=1": mustJSON(t, issue1Comments),
			"gh api -H Accept: application/vnd.github.mockingbird-preview+json repos/owner/repo/issues/1/timeline?per_page=100": mustJSON(t, []linkedPREvent{{
				Event: "cross-referenced",
				Source: struct {
					Issue struct {
						Number      int    `json:"number"`
						URL         string `json:"html_url"`
						PullRequest *struct {
							URL string `json:"html_url"`
						} `json:"pull_request"`
					} `json:"issue"`
				}{
					Issue: struct {
						Number      int    `json:"number"`
						URL         string `json:"html_url"`
						PullRequest *struct {
							URL string `json:"html_url"`
						} `json:"pull_request"`
					}{
						Number: 5,
						URL:    "https://github.com/owner/repo/pull/5",
						PullRequest: &struct {
							URL string `json:"html_url"`
						}{URL: "https://github.com/owner/repo/pull/5"},
					},
				},
			}}),
			"gh api repos/owner/repo/pulls/5": mustJSON(t, map[string]any{"commits": 2, "changed_files": 3, "additions": 50}),
			// Issue 2 comments and timeline.
			"gh api repos/owner/repo/issues/2/comments?per_page=100&page=1":                                                     mustJSON(t, issue2Comments),
			"gh api -H Accept: application/vnd.github.mockingbird-preview+json repos/owner/repo/issues/2/timeline?per_page=100": "[]",
		},
	}

	app := &App{
		stdout: &bytes.Buffer{},
		stderr: &bytes.Buffer{},
		state:  store,
		env:    nil,
	}

	var buf bytes.Buffer
	app.env = nil
	// We need to set up the runner through env. Since the app uses resolveRunner()
	// which checks a.env, let's test generateReport directly.
	err := generateReportWithRunner(context.Background(), runner, "owner/repo", "codex", &buf)
	if err != nil {
		t.Fatal(err)
	}

	r := csv.NewReader(strings.NewReader(buf.String()))
	records, err := r.ReadAll()
	if err != nil {
		t.Fatal(err)
	}

	// Header + 2 data rows.
	if len(records) != 3 {
		t.Fatalf("expected 3 CSV rows (header + 2), got %d", len(records))
	}

	// Verify header.
	for i, h := range reportCSVHeaders {
		if records[0][i] != h {
			t.Errorf("header[%d] = %q, want %q", i, records[0][i], h)
		}
	}

	// Issue 1: has timing, claude label, linked PR.
	row1 := records[1]
	if row1[1] != "1" {
		t.Errorf("issue_number = %q", row1[1])
	}
	if row1[5] != "claude" {
		t.Errorf("coding_agent = %q, want claude", row1[5])
	}
	if row1[8] != "480" {
		t.Errorf("duration = %q, want 480", row1[8])
	}
	if row1[9] != "5" {
		t.Errorf("linked_pr_number = %q, want 5", row1[9])
	}

	// Issue 2: no timing, default codex, no PR, 1 @vigilanteai comment.
	row2 := records[2]
	if row2[5] != "codex" {
		t.Errorf("coding_agent = %q, want codex", row2[5])
	}
	if row2[6] != "" {
		t.Errorf("session_start should be empty, got %q", row2[6])
	}
	if row2[14] != "1" {
		t.Errorf("first_word_vigilante_comment_count = %q, want 1", row2[14])
	}
}

func TestRunReportCommand_Help(t *testing.T) {
	t.Setenv("VIGILANTE_HOME", t.TempDir())
	store := state.NewStore()
	_ = store.EnsureLayout()

	var stdout bytes.Buffer
	app := &App{
		stdout: &stdout,
		stderr: &bytes.Buffer{},
		state:  store,
	}

	err := app.runReportCommand(context.Background(), []string{"--help"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "vigilante report --repo") {
		t.Error("help output should contain usage")
	}
}

func TestRunReportCommand_MissingRepo(t *testing.T) {
	t.Setenv("VIGILANTE_HOME", t.TempDir())
	store := state.NewStore()
	_ = store.EnsureLayout()

	app := &App{
		stdout: &bytes.Buffer{},
		stderr: &bytes.Buffer{},
		state:  store,
	}

	err := app.runReportCommand(context.Background(), []string{})
	if err == nil {
		t.Fatal("expected error for missing repo")
	}
	if !strings.Contains(err.Error(), "--repo") {
		t.Errorf("error should mention --repo, got: %s", err)
	}
}

func TestPRStats_EmptyWhenNotFound(t *testing.T) {
	s := prStats{found: false}
	if s.commitsStr() != "" {
		t.Error("expected empty commits")
	}
	if s.filesStr() != "" {
		t.Error("expected empty files")
	}
	if s.linesAddedStr() != "" {
		t.Error("expected empty lines added")
	}
}

func TestPRStats_ValuesWhenFound(t *testing.T) {
	s := prStats{found: true, commits: 5, files: 10, linesAdded: 200}
	if s.commitsStr() != "5" {
		t.Errorf("commits = %q", s.commitsStr())
	}
	if s.filesStr() != "10" {
		t.Errorf("files = %q", s.filesStr())
	}
	if s.linesAddedStr() != "200" {
		t.Errorf("linesAdded = %q", s.linesAddedStr())
	}
}
