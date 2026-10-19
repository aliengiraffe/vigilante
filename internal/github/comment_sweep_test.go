package ghcli

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nicobistolfi/vigilante/internal/environment"
	"github.com/nicobistolfi/vigilante/internal/state"
)

type sweepFakeRunner struct {
	mu        sync.Mutex
	calls     []string
	pages     map[int]string // page number -> JSON body for the issue surface
	review    string
	etag      string
	withBody  bool // return headers only when --include is present (always true for the sweep)
	failAfter int
}

func (r *sweepFakeRunner) LookPath(s string) (string, error) { return s, nil }
func (r *sweepFakeRunner) Run(_ context.Context, _ string, _ string, args ...string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	joined := strings.Join(args, " ")
	r.calls = append(r.calls, joined)
	if r.failAfter > 0 && len(r.calls) > r.failAfter {
		return "", fmt.Errorf("boom")
	}
	if args[1] == "/rate_limit" {
		return `{"resources":{"core":{"limit":5000,"remaining":5000,"reset":0}}}`, nil
	}
	for i, a := range args {
		if a == "-H" && i+1 < len(args) && args[i+1] == "If-None-Match: "+r.etag && r.etag != "" {
			return "HTTP/1.1 304 Not Modified\r\nETag: " + r.etag + "\r\n\r\ngh: HTTP 304\n", fmt.Errorf("gh [api]: exit status 1")
		}
	}
	body := "[]"
	if strings.Contains(args[1], "/pulls/comments?") {
		body = r.review
	} else {
		page := 1
		if idx := strings.Index(args[1], "&page="); idx >= 0 {
			fmt.Sscanf(args[1][idx:], "&page=%d", &page)
		}
		if p, ok := r.pages[page]; ok {
			body = p
		}
	}
	if body == "" {
		body = "[]"
	}
	etag := r.etag
	if etag == "" {
		etag = `W/"v1"`
	}
	return "HTTP/1.1 200 OK\r\nETag: " + etag + "\r\nX-RateLimit-Resource: core\r\nX-RateLimit-Remaining: 4999\r\n\r\n" + body, nil
}

func sweepComment(id int, body, stamp, issue string) string {
	return fmt.Sprintf(`{"id":%d,"body":%q,"created_at":%q,"updated_at":%q,"user":{"login":"alice"},"issue_url":"https://api.github.com/repos/o/r/issues/%s"}`, id, body, stamp, stamp, issue)
}

func TestSweepRepoCommentsPagesRoutesAndAdvancesCursor(t *testing.T) {
	now := time.Date(2026, 10, 16, 11, 0, 0, 0, time.UTC)
	var first []string
	for i := 0; i < repoSweepPageSize; i++ {
		first = append(first, sweepComment(i+1, "chatter", now.Add(time.Duration(i)*time.Second).Format(time.RFC3339), "7"))
	}
	second := []string{sweepComment(500, "@vigilanteai cleanup", now.Add(10*time.Minute).Format(time.RFC3339), "9")}
	review := fmt.Sprintf(`[{"id":900,"body":"@vigilanteai resume","created_at":%q,"updated_at":%q,"user":{"login":"bob"},"pull_request_url":"https://api.github.com/repos/o/r/pulls/42"}]`, now.Add(5*time.Minute).Format(time.RFC3339), now.Add(5*time.Minute).Format(time.RFC3339))
	runner := &sweepFakeRunner{pages: map[int]string{1: "[" + strings.Join(first, ",") + "]", 2: "[" + strings.Join(second, ",") + "]"}, review: review}
	result, err := SweepRepoComments(context.Background(), runner, "o/r", state.CommentSweepCursor{}, now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if result.Requests != 3 || result.NotModified != 0 {
		t.Fatalf("requests=%d not_modified=%d", result.Requests, result.NotModified)
	}
	if len(result.Comments) != repoSweepPageSize+2 {
		t.Fatalf("comments=%d", len(result.Comments))
	}
	if !strings.Contains(runner.calls[0], "since="+now.Add(-time.Hour).Format(time.RFC3339)) || !strings.Contains(runner.calls[0], "per_page=100") || !strings.Contains(runner.calls[0], "--include") {
		t.Fatalf("first request: %s", runner.calls[0])
	}
	if !strings.Contains(runner.calls[1], "&page=2") {
		t.Fatalf("second page request: %s", runner.calls[1])
	}
	var routed []string
	for _, c := range result.Comments {
		if strings.HasPrefix(c.Body, "@vigilanteai") {
			routed = append(routed, fmt.Sprintf("%s:%d:%s", c.Surface, c.Number, c.Body))
		}
	}
	if strings.Join(routed, ",") != "review:42:@vigilanteai resume,issue:9:@vigilanteai cleanup" {
		t.Fatalf("routing: %v", routed)
	}
	// The cursor advances to the newest updated_at and drops ETags for the old URL.
	if result.Cursor.Since != now.Add(10*time.Minute).Format(time.RFC3339) || result.Cursor.IssueCommentsETag != "" || result.Cursor.ReviewCommentsETag != "" {
		t.Fatalf("cursor: %+v", result.Cursor)
	}
}

func TestSweepRepoCommentsUsesConditionalRequestsAndKeepsCursorOn304(t *testing.T) {
	now := time.Date(2026, 10, 16, 11, 0, 0, 0, time.UTC)
	runner := &sweepFakeRunner{etag: `W/"stable"`}
	cursor := state.CommentSweepCursor{Since: now.Format(time.RFC3339)}
	result, err := SweepRepoComments(context.Background(), runner, "o/r", cursor, now)
	if err != nil {
		t.Fatal(err)
	}
	if result.Cursor.IssueCommentsETag != `W/"stable"` || result.Cursor.ReviewCommentsETag != `W/"stable"` || result.Cursor.Since != cursor.Since {
		t.Fatalf("cursor after empty 200: %+v", result.Cursor)
	}
	result, err = SweepRepoComments(context.Background(), runner, "o/r", result.Cursor, now)
	if err != nil {
		t.Fatal(err)
	}
	if result.NotModified != 2 || result.Requests != 2 || len(result.Comments) != 0 {
		t.Fatalf("conditional sweep: %+v", result)
	}
	if result.Cursor != cursor || result.Cursor.IssueCommentsETag == "" {
		// Since must remain stable so the ETag stays valid; ETags are preserved.
		if result.Cursor.Since != cursor.Since || result.Cursor.IssueCommentsETag != `W/"stable"` {
			t.Fatalf("cursor after 304: %+v", result.Cursor)
		}
	}
	for _, call := range runner.calls[2:] {
		if !strings.Contains(call, `If-None-Match: W/"stable"`) {
			t.Fatalf("missing conditional header: %s", call)
		}
	}
}

func TestSweepRepoCommentsReadsHeadersThroughBudgetRunnerSink(t *testing.T) {
	now := time.Date(2026, 10, 16, 11, 0, 0, 0, time.UTC)
	fake := &sweepFakeRunner{etag: `W/"budgeted"`}
	budget := &environment.GitHubBudgetRunner{Base: fake, Clock: func() time.Time { return now }}
	ctx := environment.WithAccessLogContext(context.Background(), environment.AccessLogContext{ExecutionContext: "scan"})
	result, err := SweepRepoComments(ctx, budget, "o/r", state.CommentSweepCursor{}, now)
	if err != nil {
		t.Fatal(err)
	}
	if result.Cursor.IssueCommentsETag != `W/"budgeted"` {
		t.Fatalf("ETag not recovered from the budget runner sink: %+v", result.Cursor)
	}
	if budget.Status().Used != 2 {
		t.Fatalf("two REST units expected for two 200 responses: %+v", budget.Status())
	}
	result, err = SweepRepoComments(ctx, budget, "o/r", result.Cursor, now)
	if err != nil {
		t.Fatal(err)
	}
	if result.NotModified != 2 || budget.Status().Used != 2 {
		t.Fatalf("304 responses must be refunded: result=%+v status=%+v", result, budget.Status())
	}
}

func TestSweepRepoCommentsPropagatesErrorsAndSkipsUnparseableSources(t *testing.T) {
	now := time.Date(2026, 10, 16, 11, 0, 0, 0, time.UTC)
	runner := &sweepFakeRunner{failAfter: 1}
	if _, err := SweepRepoComments(context.Background(), runner, "o/r", state.CommentSweepCursor{}, now); err == nil || !strings.Contains(err.Error(), "sweep review comments") {
		t.Fatalf("expected review surface failure, got %v", err)
	}
	if _, ok := toRepoComment(sweptIssueComment{IssueURL: "https://api.github.com/repos/o/r/issues/not-a-number"}, "issue"); ok {
		t.Fatal("accepted comment without a parsable issue number")
	}
	if status, _, body := splitResponse("plain body"); status != 0 || body != "plain body" {
		t.Fatalf("splitResponse(plain) = %d %q", status, body)
	}
	if trailingNumber("") != 0 || trailingNumber("https://x/issues/12") != 12 {
		t.Fatal("trailingNumber")
	}
	// A cursor with a corrupt since value falls back to the initial window.
	result, err := SweepRepoComments(context.Background(), &sweepFakeRunner{}, "o/r", state.CommentSweepCursor{Since: "garbage"}, now)
	if err != nil || result.Cursor.Since != now.Format(time.RFC3339) {
		t.Fatalf("corrupt cursor: %+v %v", result.Cursor, err)
	}
}
