package runner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nicobistolfi/vigilante/internal/state"
	"github.com/nicobistolfi/vigilante/internal/testutil"
)

// emptyStatusHash is the fingerprint suffix for a clean worktree.
const emptyStatusHash = "e3b0c44298fc1c14"

func TestParseBaseBranchDirective(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "plain", body: "Base branch: feature/x\n\n## Summary", want: "feature/x"},
		{name: "case and backticks", body: "## Summary\nbase BRANCH:   `stack/one`  \r\nmore", want: "stack/one"},
		{name: "none", body: "## Summary\nNo directive here.", want: ""},
		{name: "inline mention is not a directive", body: "Use a `Base branch: x` line to stack.", want: ""},
		{name: "spaces in name rejected", body: "Base branch: two words", want: ""},
		{name: "option-like name rejected", body: "Base branch: --upload-pack=x", want: ""},
		{name: "range rejected", body: "Base branch: a..b", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := ParseBaseBranchDirective(tt.body); got != tt.want {
				t.Fatalf("ParseBaseBranchDirective() = %q, want %q", got, tt.want)
			}
		})
	}
}

// The rhizome#136 loop counted commits inherited from a stacked base as new
// work. Progress must be measured against the stacked base.
func TestDetectNewCommitsUsesStackedBase(t *testing.T) {
	t.Parallel()

	runner := testutil.FakeRunner{Outputs: map[string]string{
		"git rev-list --count origin/stack/base..HEAD": "0",
		"git rev-list --count origin/main..HEAD":       "4",
	}}
	session := state.Session{WorktreePath: "/wt", BaseBranch: "main", StackedBaseBranch: "stack/base"}
	if detectNewCommits(context.Background(), runner, session) {
		t.Fatal("commits inherited from the stacked base must not count as new work")
	}
	session.StackedBaseBranch = ""
	if !detectNewCommits(context.Background(), runner, session) {
		t.Fatal("without a stacked base the session base is used")
	}
}

func TestProgressBaseBranchOrder(t *testing.T) {
	t.Parallel()

	session := state.Session{StackedBaseBranch: "stack", PullRequestBaseBranch: "pr-base", BaseBranch: "main"}
	if got := ProgressBaseBranch(session); got != "stack" {
		t.Fatalf("got %q, want stacked base", got)
	}
	session.StackedBaseBranch = ""
	if got := ProgressBaseBranch(session); got != "pr-base" {
		t.Fatalf("got %q, want PR base", got)
	}
	session.PullRequestBaseBranch = ""
	if got := ProgressBaseBranch(session); got != "main" {
		t.Fatalf("got %q, want session base", got)
	}
	if got := ProgressBaseBranch(state.Session{}); got != "main" {
		t.Fatalf("got %q, want main fallback", got)
	}
}

func TestEvaluateSessionProgressFingerprint(t *testing.T) {
	t.Parallel()

	runner := testutil.FakeRunner{Outputs: map[string]string{
		"git rev-list --count origin/main..HEAD": "0",
		"git status --porcelain":                 "",
		"git rev-parse HEAD":                     "abc123\n",
	}}
	signal := EvaluateSessionProgress(context.Background(), runner, state.Session{WorktreePath: "/wt", BaseBranch: "main"})
	if want := "abc123:" + emptyStatusHash; signal.Fingerprint != want {
		t.Fatalf("fingerprint = %q, want %q", signal.Fingerprint, want)
	}

	runner.Outputs["git status --porcelain"] = " M a.go\n"
	dirty := EvaluateSessionProgress(context.Background(), runner, state.Session{WorktreePath: "/wt", BaseBranch: "main"})
	if dirty.Fingerprint == signal.Fingerprint || !strings.HasPrefix(dirty.Fingerprint, "abc123:") {
		t.Fatalf("worktree changes must change the fingerprint: %q vs %q", dirty.Fingerprint, signal.Fingerprint)
	}
}

func TestRecordIncompleteAttempt(t *testing.T) {
	t.Parallel()

	t.Run("first attempt records and continues", func(t *testing.T) {
		session := state.Session{}
		if blocked := RecordIncompleteAttempt(&session, ProgressSignal{Fingerprint: "a:1"}); blocked != nil {
			t.Fatalf("first attempt must not block: %#v", blocked)
		}
		if session.LastAttemptFingerprint != "a:1" {
			t.Fatalf("fingerprint not recorded: %q", session.LastAttemptFingerprint)
		}
	})
	t.Run("no progress repeat blocks", func(t *testing.T) {
		session := state.Session{LastAttemptFingerprint: "a:1"}
		blocked := RecordIncompleteAttempt(&session, ProgressSignal{Fingerprint: "a:1"})
		if blocked == nil || blocked.Kind != BlockedKindNoProgressRepeated {
			t.Fatalf("expected no_progress_repeated, got %#v", blocked)
		}
	})
	t.Run("commits advancing continue", func(t *testing.T) {
		session := state.Session{LastAttemptFingerprint: "a:1"}
		if blocked := RecordIncompleteAttempt(&session, ProgressSignal{HasNewCommits: true, Fingerprint: "b:1"}); blocked != nil {
			t.Fatalf("progress must not block: %#v", blocked)
		}
	})
	t.Run("unknown fingerprint never blocks", func(t *testing.T) {
		session := state.Session{}
		if blocked := RecordIncompleteAttempt(&session, ProgressSignal{}); blocked != nil {
			t.Fatalf("unknown state must not block: %#v", blocked)
		}
	})
}

func TestRerunDecision(t *testing.T) {
	t.Parallel()

	ended := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	base := state.Session{Status: state.SessionStatusIncomplete, IncompleteReason: "no_durable_progress", EndedAt: ended.Format(time.RFC3339)}
	tests := []struct {
		name    string
		reruns  int
		now     time.Time
		status  state.SessionStatus
		want    RerunAction
		wantDue time.Time
	}{
		{name: "waits for first backoff", now: ended.Add(time.Minute), want: RerunWait, wantDue: ended.Add(5 * time.Minute)},
		{name: "launches after backoff", now: ended.Add(5 * time.Minute), want: RerunLaunch},
		{name: "backoff doubles", reruns: 2, now: ended.Add(15 * time.Minute), want: RerunWait, wantDue: ended.Add(20 * time.Minute)},
		{name: "cap reached blocks", reruns: 3, now: ended.Add(time.Hour), want: RerunBlock},
		{name: "not incomplete skipped", now: ended.Add(time.Hour), status: state.SessionStatusBlocked, want: RerunSkip},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			session := base
			session.IncompleteRerunCount = tt.reruns
			if tt.status != "" {
				session.Status = tt.status
			}
			got, due := RerunDecision(session, tt.now, 3, 5*time.Minute)
			if got != tt.want || !due.Equal(tt.wantDue) {
				t.Fatalf("RerunDecision() = (%v, %v), want (%v, %v)", got, due, tt.want, tt.wantDue)
			}
		})
	}
}

func TestConsumeAgentBlockedSignal(t *testing.T) {
	t.Parallel()

	write := func(t *testing.T, content string) string {
		t.Helper()
		dir := t.TempDir()
		path := filepath.Join(dir, filepath.FromSlash(AgentBlockedSignalPath))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return dir
	}

	t.Run("absent", func(t *testing.T) {
		t.Parallel()
		if got := ConsumeAgentBlockedSignal(t.TempDir()); got != nil {
			t.Fatalf("expected no signal, got %#v", got)
		}
	})
	t.Run("dependency missing", func(t *testing.T) {
		t.Parallel()
		dir := write(t, `{"kind":"dependency_missing","summary":"PR #12 must merge first.","detail":"The base branch lacks the store package."}`)
		got := ConsumeAgentBlockedSignal(dir)
		if got == nil || got.Kind != BlockedKindDependencyMissing || got.Summary != "PR #12 must merge first." || !strings.Contains(got.Detail, "store package") {
			t.Fatalf("unexpected reason: %#v", got)
		}
		if _, err := os.Stat(filepath.Join(dir, ".vigilante")); !os.IsNotExist(err) {
			t.Fatalf("signal file and its directory must be removed, stat err = %v", err)
		}
	})
	t.Run("unknown kind falls back", func(t *testing.T) {
		t.Parallel()
		got := ConsumeAgentBlockedSignal(write(t, `{"kind":"provider_auth","summary":"stuck"}`))
		if got == nil || got.Kind != BlockedKindAgentDeclared {
			t.Fatalf("unexpected reason: %#v", got)
		}
	})
	t.Run("malformed still blocks", func(t *testing.T) {
		t.Parallel()
		got := ConsumeAgentBlockedSignal(write(t, `not json`))
		if got == nil || got.Kind != BlockedKindAgentDeclared || !strings.Contains(got.Detail, "unreadable") {
			t.Fatalf("unexpected reason: %#v", got)
		}
	})
}

func TestSessionBlockedComment(t *testing.T) {
	t.Parallel()

	session := state.Session{Branch: "vigilante/issue-7", ResumeHint: "vigilante resume --repo o/r --issue 7"}
	session.BlockedReason = RerunLimitBlockedReason(3)
	body := SessionBlockedComment(session)
	for _, want := range []string{"stopped after 3 automatic reruns", "@vigilanteai resume", "incomplete_rerun_limit"} {
		if !strings.Contains(body, want) {
			t.Fatalf("comment missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "Incomplete") {
		t.Fatalf("blocked comment must not read as incomplete:\n%s", body)
	}
}
