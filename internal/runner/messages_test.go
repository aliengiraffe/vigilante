package runner

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/nicobistolfi/vigilante/internal/state"
	"github.com/nicobistolfi/vigilante/internal/testutil"
)

// Every blocked-session message ends up in a GitHub comment an operator reads to
// decide what to do. Quota blocks get distinct wording because the action differs
// (wait or upgrade) from a failure that needs investigating, so the split is what
// these tests pin.

func TestBlockedPreflightMessage(t *testing.T) {
	t.Parallel()

	quota := blockedPreflightMessage(state.BlockedReason{Kind: "provider_quota"}, "claude")
	if !strings.Contains(quota, "usage or subscription limit") || !strings.Contains(quota, "claude") {
		t.Fatalf("quota message = %q", quota)
	}

	other := blockedPreflightMessage(state.BlockedReason{Kind: "validation_failed"}, "claude")
	if !strings.Contains(other, "baseline validation failed") {
		t.Fatalf("non-quota message = %q", other)
	}
	if strings.Contains(other, "usage or subscription") {
		t.Fatalf("non-quota message must not mention quota: %q", other)
	}
}

func TestBlockedExecutionMessage(t *testing.T) {
	t.Parallel()

	quota := blockedExecutionMessage(state.BlockedReason{Kind: "provider_quota"}, "codex")
	if !strings.Contains(quota, "usage or subscription limit") || !strings.Contains(quota, "codex") {
		t.Fatalf("quota message = %q", quota)
	}

	other := blockedExecutionMessage(state.BlockedReason{Kind: "git_auth"}, "codex")
	if !strings.Contains(other, "stopped before the issue implementation completed") {
		t.Fatalf("non-quota message = %q", other)
	}
	if strings.Contains(other, "usage or subscription") {
		t.Fatalf("non-quota message must not mention quota: %q", other)
	}
}

func TestBlockedConflictMessage(t *testing.T) {
	t.Parallel()

	quota := blockedConflictMessage(state.BlockedReason{Kind: "provider_quota"}, 12, "br", "gemini")
	for _, want := range []string{"#12", "br", "gemini", "usage or subscription limit"} {
		if !strings.Contains(quota, want) {
			t.Errorf("quota message %q missing %q", quota, want)
		}
	}

	other := blockedConflictMessage(state.BlockedReason{Kind: "network_unreachable"}, 12, "br", "gemini")
	for _, want := range []string{"#12", "br", "gemini", "did not complete"} {
		if !strings.Contains(other, want) {
			t.Errorf("non-quota message %q missing %q", other, want)
		}
	}
	if strings.Contains(other, "usage or subscription") {
		t.Fatalf("non-quota message must not mention quota: %q", other)
	}
}

func TestBlockedCIRemediationMessage(t *testing.T) {
	t.Parallel()

	quota := blockedCIRemediationMessage(state.BlockedReason{Kind: "provider_quota"}, 5, "br", "claude")
	for _, want := range []string{"#5", "br", "claude", "usage or subscription limit"} {
		if !strings.Contains(quota, want) {
			t.Errorf("quota message %q missing %q", quota, want)
		}
	}

	other := blockedCIRemediationMessage(state.BlockedReason{}, 5, "br", "claude")
	for _, want := range []string{"#5", "br", "did not complete automatically"} {
		if !strings.Contains(other, want) {
			t.Errorf("non-quota message %q missing %q", other, want)
		}
	}
}

// The preflight item list is the body of the blocked comment. Validation failures
// get extra detail lines because that is the case where an operator can act on the
// output directly; other kinds would only add noise.
func TestBlockedPreflightItems(t *testing.T) {
	t.Parallel()

	t.Run("validation failure includes detail and output", func(t *testing.T) {
		t.Parallel()

		blocked := state.BlockedReason{
			Kind:    "validation_failed",
			Summary: "go test ./... failed",
			Detail:  "--- FAIL: TestThing",
		}
		items := blockedPreflightItems(blocked, "claude", "FAIL github.com/x/y 0.1s", "vigilante resume --repo o/r --issue 1")

		joined := strings.Join(items, "\n")
		if !strings.Contains(joined, "Failed validation:") {
			t.Errorf("expected a validation detail line, got:\n%s", joined)
		}
		if !strings.Contains(joined, "Relevant preflight output:") {
			t.Errorf("expected a preflight output line, got:\n%s", joined)
		}
		if !strings.Contains(joined, "vigilante resume --repo o/r --issue 1") {
			t.Errorf("expected the resume hint, got:\n%s", joined)
		}
	})

	t.Run("non-validation failure omits the detail lines", func(t *testing.T) {
		t.Parallel()

		blocked := state.BlockedReason{Kind: "provider_quota", Summary: "limit hit"}
		items := blockedPreflightItems(blocked, "claude", "some output", "hint")

		joined := strings.Join(items, "\n")
		if strings.Contains(joined, "Failed validation:") {
			t.Errorf("a quota block must not claim a validation failure:\n%s", joined)
		}
		if strings.Contains(joined, "Relevant preflight output:") {
			t.Errorf("a quota block must not attach preflight output:\n%s", joined)
		}
	})

	t.Run("validation failure with no usable detail still lists a next step", func(t *testing.T) {
		t.Parallel()

		items := blockedPreflightItems(state.BlockedReason{Kind: "validation_failed"}, "claude", "   ", "hint")

		joined := strings.Join(items, "\n")
		if strings.Contains(joined, "Failed validation:") {
			t.Errorf("no detail should mean no detail line:\n%s", joined)
		}
		if !strings.Contains(joined, "Next step:") {
			t.Errorf("expected a next step, got:\n%s", joined)
		}
	})
}

// The incomplete-session items tell the operator what durable progress survived,
// which determines whether a rerun can continue from existing work.
func TestIncompleteSessionItems(t *testing.T) {
	t.Parallel()

	session := state.Session{Branch: "vigilante/issue-1"}

	tests := []struct {
		name     string
		signal   ProgressSignal
		contains string
	}{
		{
			name:     "commits and worktree changes",
			signal:   ProgressSignal{HasNewCommits: true, HasWorktreeChanges: true},
			contains: "New commits were pushed to the branch and uncommitted changes remain",
		},
		{
			name:     "commits only",
			signal:   ProgressSignal{HasNewCommits: true},
			contains: "New commits were pushed to the branch but no PR was opened",
		},
		{
			name:     "worktree changes only",
			signal:   ProgressSignal{HasWorktreeChanges: true},
			contains: "Uncommitted changes exist in the worktree but no commits were made",
		},
		{
			name:     "no progress at all",
			signal:   ProgressSignal{},
			contains: "No durable progress was detected",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			items := incompleteSessionItems(session, tt.signal)
			joined := strings.Join(items, "\n")

			if !strings.Contains(joined, tt.contains) {
				t.Fatalf("items:\n%s\nwant it to contain %q", joined, tt.contains)
			}
			if !strings.Contains(joined, "vigilante/issue-1") {
				t.Errorf("items should name the branch:\n%s", joined)
			}
			if !strings.Contains(joined, "Next step:") {
				t.Errorf("items should end with a next step:\n%s", joined)
			}
		})
	}
}

func TestBlockedValidationDetail(t *testing.T) {
	t.Parallel()

	// Summary is preferred; Detail is the fallback when Summary is unusable.
	if got := blockedValidationDetail(state.BlockedReason{Summary: "from summary", Detail: "from detail"}); got != "from summary" {
		t.Errorf("got %q, want the summary", got)
	}
	if got := blockedValidationDetail(state.BlockedReason{Summary: "   ", Detail: "from detail"}); got != "from detail" {
		t.Errorf("got %q, want the detail fallback", got)
	}
	if got := blockedValidationDetail(state.BlockedReason{}); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

// The diagnostic text is embedded in a Markdown comment, so it is collapsed to a
// single line and length-capped. Trailing punctuation is stripped because the
// caller wraps it in its own sentence.
func TestSanitizeDiagnosticText(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		text  string
		limit int
		want  string
	}{
		{name: "empty", text: "", limit: 100, want: ""},
		{name: "whitespace only", text: "   \n\t ", limit: 100, want: ""},
		{
			name:  "newlines and runs of spaces collapse",
			text:  "line one\n\nline    two",
			limit: 100,
			want:  "line one line two",
		},
		{
			name:  "trailing sentence punctuation is stripped",
			text:  "something failed...",
			limit: 100,
			want:  "something failed",
		},
		{
			name:  "trailing bang and question are stripped",
			text:  "what?!",
			limit: 100,
			want:  "what",
		},
		{
			name:  "text at the limit is unchanged",
			text:  strings.Repeat("a", 10),
			limit: 10,
			want:  strings.Repeat("a", 10),
		},
		{
			name:  "over the limit is elided with an ellipsis",
			text:  strings.Repeat("b", 20),
			limit: 10,
			want:  strings.Repeat("b", 7) + "...",
		},
		{
			// With no room for an ellipsis the text is simply cut, rather than
			// producing a string longer than the limit.
			name:  "tiny limit truncates without an ellipsis",
			text:  strings.Repeat("c", 20),
			limit: 3,
			want:  "ccc",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := sanitizeDiagnosticText(tt.text, tt.limit)
			if got != tt.want {
				t.Fatalf("sanitizeDiagnosticText(%q, %d) = %q, want %q", tt.text, tt.limit, got, tt.want)
			}
			if len(got) > tt.limit && tt.limit > 0 {
				t.Fatalf("result %q exceeds the limit %d", got, tt.limit)
			}
		})
	}
}

func TestSummarizePreflightOutput(t *testing.T) {
	t.Parallel()

	if got := summarizePreflightOutput("  FAIL\n\ngithub.com/x  "); got != "FAIL github.com/x" {
		t.Fatalf("got %q", got)
	}
	long := summarizePreflightOutput(strings.Repeat("z", 400))
	if len(long) > 280 {
		t.Fatalf("output length %d exceeds the 280 cap", len(long))
	}
}

func TestSummarizeError(t *testing.T) {
	t.Parallel()

	if got := summarizeError(errors.New("  boom  ")); got != "boom" {
		t.Fatalf("got %q, want %q", got, "boom")
	}
	if got := summarizeError(errors.New(strings.Repeat("x", 500))); len(got) != 400 {
		t.Fatalf("length = %d, want the 400 cap", len(got))
	}
}

// Exit 137 inside a sandbox almost always means the kernel OOM-killed the agent
// container. Saying so turns a baffling exit code into an actionable one, but only
// when the session was actually sandboxed.
func TestDescribeProviderFailure(t *testing.T) {
	t.Parallel()

	if got := describeProviderFailure(state.Session{}, nil); got != "" {
		t.Fatalf("nil error = %q, want empty", got)
	}

	sandboxed := describeProviderFailure(state.Session{SandboxMode: true}, errors.New("exit status 137"))
	if !strings.Contains(sandboxed, "OOM") {
		t.Fatalf("sandboxed 137 = %q, want an OOM annotation", sandboxed)
	}

	unsandboxed := describeProviderFailure(state.Session{}, errors.New("exit status 137"))
	if strings.Contains(unsandboxed, "OOM") {
		t.Fatalf("unsandboxed 137 = %q, must not claim a container OOM", unsandboxed)
	}

	otherCode := describeProviderFailure(state.Session{SandboxMode: true}, errors.New("exit status 1"))
	if strings.Contains(otherCode, "OOM") {
		t.Fatalf("exit 1 = %q, must not claim OOM", otherCode)
	}
}

func TestDescribeExitError(t *testing.T) {
	t.Parallel()

	// A non-exit error is passed through verbatim.
	if got := describeExitError(errors.New("plain failure")); got != "plain failure" {
		t.Fatalf("got %q", got)
	}

	cmd := exec.Command("sh", "-c", "exit 4")
	err := cmd.Run()
	if err == nil {
		t.Skip("could not produce an ExitError")
	}
	if got := describeExitError(err); got != "exit code 4" {
		t.Fatalf("got %q, want %q", got, "exit code 4")
	}

	oom := exec.Command("sh", "-c", "exit 137")
	oomErr := oom.Run()
	if oomErr == nil {
		t.Skip("could not produce a 137 ExitError")
	}
	got := describeExitError(oomErr)
	if !strings.Contains(got, "137") || !strings.Contains(got, "OOM") {
		t.Fatalf("exit 137 = %q, want an OOM annotation", got)
	}
}

func TestCombineLogDetails(t *testing.T) {
	t.Parallel()

	tests := []struct {
		output string
		errno  string
		want   string
	}{
		{output: "", errno: "err", want: "err"},
		{output: "out", errno: "", want: "out"},
		{output: "  ", errno: "err", want: "err"},
		{output: "out", errno: "  ", want: "out"},
		{output: "out", errno: "err", want: "out\nerr"},
		{output: "", errno: "", want: ""},
		{output: "  out  ", errno: "  err  ", want: "out\nerr"},
	}
	for _, tt := range tests {
		if got := combineLogDetails(tt.output, tt.errno); got != tt.want {
			t.Errorf("combineLogDetails(%q, %q) = %q, want %q", tt.output, tt.errno, got, tt.want)
		}
	}
}

func TestFilepathDir(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"/a/b/c.log": "/a/b",
		"a/b.log":    "a",
		"b.log":      ".",
		"":           ".",
		"/b.log":     ".",
	}
	for path, want := range tests {
		if got := filepathDir(path); got != want {
			t.Errorf("filepathDir(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestBuildResumeHint(t *testing.T) {
	t.Parallel()

	got := buildResumeHint(state.Session{Repo: "owner/repo", IssueNumber: 42})
	if got != "vigilante resume --repo owner/repo --issue 42" {
		t.Fatalf("got %q", got)
	}
}

// A blocked session must be parked, not left looking runnable: the daemon uses
// these fields to decide whether to pick the session up again.
func TestMarkSessionBlocked(t *testing.T) {
	t.Parallel()

	session := state.Session{
		Repo:        "owner/repo",
		IssueNumber: 3,
		Status:      state.SessionStatusRunning,
		ProcessID:   4242,
		RecoveredAt: "2026-03-10T00:00:00Z",
	}
	blocked := state.BlockedReason{Kind: "git_auth", Summary: "ssh key missing"}
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)

	markSessionBlocked(&session, "issue_execution", blocked, now)

	if session.Status != state.SessionStatusBlocked {
		t.Errorf("Status = %q, want blocked", session.Status)
	}
	if session.BlockedStage != "issue_execution" {
		t.Errorf("BlockedStage = %q", session.BlockedStage)
	}
	if session.BlockedReason.Kind != "git_auth" {
		t.Errorf("BlockedReason.Kind = %q", session.BlockedReason.Kind)
	}
	if session.RetryPolicy != "paused" {
		t.Errorf("RetryPolicy = %q, want paused", session.RetryPolicy)
	}
	if !session.ResumeRequired {
		t.Error("ResumeRequired should be true")
	}
	if session.ResumeHint == "" {
		t.Error("ResumeHint should be populated so the comment can quote it")
	}
	// A stale PID would make the daemon think the agent is still running.
	if session.ProcessID != 0 {
		t.Errorf("ProcessID = %d, want cleared", session.ProcessID)
	}
	if session.RecoveredAt != "" {
		t.Errorf("RecoveredAt = %q, want cleared", session.RecoveredAt)
	}
	if session.BlockedAt != "2026-08-09T12:00:00Z" {
		t.Errorf("BlockedAt = %q", session.BlockedAt)
	}
}

// Progress detection decides whether an incomplete session can be resumed in
// place. A git failure must read as "no progress" rather than propagating, so a
// broken worktree cannot wedge the scan.
func TestDetectNewCommits(t *testing.T) {
	t.Parallel()

	session := state.Session{WorktreePath: "/wt", BaseBranch: "develop"}

	t.Run("commits ahead", func(t *testing.T) {
		t.Parallel()

		runner := testutil.FakeRunner{
			Outputs: map[string]string{"git rev-list --count origin/develop..HEAD": "3\n"},
		}
		if !detectNewCommits(context.Background(), runner, session) {
			t.Fatal("expected new commits to be detected")
		}
	})

	t.Run("zero commits", func(t *testing.T) {
		t.Parallel()

		runner := testutil.FakeRunner{
			Outputs: map[string]string{"git rev-list --count origin/develop..HEAD": "0\n"},
		}
		if detectNewCommits(context.Background(), runner, session) {
			t.Fatal("zero commits must not count as progress")
		}
	})

	t.Run("empty output", func(t *testing.T) {
		t.Parallel()

		runner := testutil.FakeRunner{
			Outputs: map[string]string{"git rev-list --count origin/develop..HEAD": "  "},
		}
		if detectNewCommits(context.Background(), runner, session) {
			t.Fatal("empty output must not count as progress")
		}
	})

	t.Run("base branch defaults to main", func(t *testing.T) {
		t.Parallel()

		runner := testutil.FakeRunner{
			Outputs: map[string]string{"git rev-list --count origin/main..HEAD": "1\n"},
		}
		if !detectNewCommits(context.Background(), runner, state.Session{WorktreePath: "/wt"}) {
			t.Fatal("a blank base branch should default to main")
		}
	})

	t.Run("git failure reads as no progress", func(t *testing.T) {
		t.Parallel()

		runner := testutil.FakeRunner{
			Errors: map[string]error{"git rev-list --count origin/develop..HEAD": errors.New("boom")},
		}
		if detectNewCommits(context.Background(), runner, session) {
			t.Fatal("a git failure must not be reported as progress")
		}
	})

	t.Run("no worktree path short-circuits", func(t *testing.T) {
		t.Parallel()

		// An empty FakeRunner errors on any command, so false here proves no
		// command was attempted.
		if detectNewCommits(context.Background(), testutil.FakeRunner{}, state.Session{}) {
			t.Fatal("a session without a worktree has no commits to find")
		}
	})
}

func TestDetectWorktreeChanges(t *testing.T) {
	t.Parallel()

	session := state.Session{WorktreePath: "/wt"}

	t.Run("dirty worktree", func(t *testing.T) {
		t.Parallel()

		runner := testutil.FakeRunner{
			Outputs: map[string]string{"git status --porcelain": " M main.go\n"},
		}
		if !detectWorktreeChanges(context.Background(), runner, session) {
			t.Fatal("expected changes to be detected")
		}
	})

	t.Run("clean worktree", func(t *testing.T) {
		t.Parallel()

		runner := testutil.FakeRunner{
			Outputs: map[string]string{"git status --porcelain": "\n"},
		}
		if detectWorktreeChanges(context.Background(), runner, session) {
			t.Fatal("a clean worktree has no changes")
		}
	})

	t.Run("git failure reads as clean", func(t *testing.T) {
		t.Parallel()

		runner := testutil.FakeRunner{
			Errors: map[string]error{"git status --porcelain": errors.New("boom")},
		}
		if detectWorktreeChanges(context.Background(), runner, session) {
			t.Fatal("a git failure must not be reported as changes")
		}
	})

	t.Run("no worktree path short-circuits", func(t *testing.T) {
		t.Parallel()

		if detectWorktreeChanges(context.Background(), testutil.FakeRunner{}, state.Session{}) {
			t.Fatal("a session without a worktree has no changes")
		}
	})
}

// runStreaming prefers a StreamingRunner so provider output reaches the session
// log live, and must fall back cleanly when the runner cannot stream or there is
// nowhere to stream to.
func TestRunStreaming(t *testing.T) {
	t.Parallel()

	t.Run("uses the streaming path when a writer is present", func(t *testing.T) {
		t.Parallel()

		runner := testutil.FakeRunner{Outputs: map[string]string{"git status": "streamed"}}
		var sink strings.Builder

		output, err := runStreaming(context.Background(), runner, "/dir", &sink, "git", "status")
		if err != nil {
			t.Fatal(err)
		}
		if output != "streamed" {
			t.Fatalf("output = %q", output)
		}
		if sink.String() != "streamed" {
			t.Fatalf("writer got %q, want the streamed output", sink.String())
		}
	})

	t.Run("falls back to Run when there is no writer", func(t *testing.T) {
		t.Parallel()

		runner := testutil.FakeRunner{Outputs: map[string]string{"git status": "plain"}}

		output, err := runStreaming(context.Background(), runner, "/dir", nil, "git", "status")
		if err != nil {
			t.Fatal(err)
		}
		if output != "plain" {
			t.Fatalf("output = %q", output)
		}
	})

	t.Run("propagates errors", func(t *testing.T) {
		t.Parallel()

		runner := testutil.FakeRunner{Errors: map[string]error{"git status": errors.New("boom")}}
		if _, err := runStreaming(context.Background(), runner, "/dir", nil, "git", "status"); err == nil {
			t.Fatal("expected an error")
		}
	})
}
