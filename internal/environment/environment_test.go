package environment

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/nicobistolfi/vigilante/internal/testutil"
)

func newTestLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			if a.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return a
		},
	}))
}

func TestLoggingRunnerLogsCommandsWithoutSuccessOutputByDefault(t *testing.T) {
	var buf bytes.Buffer
	runner := LoggingRunner{
		Base: testutil.FakeRunner{
			Outputs: map[string]string{
				"gh issue list": "[]",
			},
		},
		Logger: newTestLogger(&buf),
	}

	if _, err := runner.Run(context.Background(), "/tmp/repo", "gh", "issue", "list"); err != nil {
		t.Fatal(err)
	}
	logOutput := buf.String()
	if !strings.Contains(logOutput, `msg="command start"`) {
		t.Fatalf("expected command start log, got: %s", logOutput)
	}
	if !strings.Contains(logOutput, `dir=/tmp/repo`) {
		t.Fatalf("expected dir attribute, got: %s", logOutput)
	}
	if !strings.Contains(logOutput, `cmd="gh issue list"`) {
		t.Fatalf("expected cmd attribute, got: %s", logOutput)
	}
	if !strings.Contains(logOutput, `msg="command ok"`) {
		t.Fatalf("expected command ok log, got: %s", logOutput)
	}
	if strings.Contains(logOutput, "output=") {
		t.Fatalf("expected no output attribute when LogSuccessOutput is false, got: %s", logOutput)
	}
}

func TestLoggingRunnerCanLogSuccessOutputWhenEnabled(t *testing.T) {
	var buf bytes.Buffer
	runner := LoggingRunner{
		Base: testutil.FakeRunner{
			Outputs: map[string]string{
				"gh issue list": "[]",
			},
		},
		Logger:           newTestLogger(&buf),
		LogSuccessOutput: true,
	}

	if _, err := runner.Run(context.Background(), "/tmp/repo", "gh", "issue", "list"); err != nil {
		t.Fatal(err)
	}
	logOutput := buf.String()
	if !strings.Contains(logOutput, `msg="command ok"`) {
		t.Fatalf("expected command ok log, got: %s", logOutput)
	}
	if !strings.Contains(logOutput, "output=") {
		t.Fatalf("expected output attribute, got: %s", logOutput)
	}
}

func TestLoggingRunnerLogsFailures(t *testing.T) {
	var buf bytes.Buffer
	runner := LoggingRunner{
		Base: testutil.FakeRunner{
			Errors: map[string]error{
				"git status": fmt.Errorf("boom"),
			},
		},
		Logger: newTestLogger(&buf),
	}

	if _, err := runner.Run(context.Background(), "", "git", "status"); err == nil {
		t.Fatal("expected error")
	}
	logOutput := buf.String()
	if !strings.Contains(logOutput, `msg="command failed"`) {
		t.Fatalf("expected command failed log, got: %s", logOutput)
	}
	if !strings.Contains(logOutput, `cmd="git status"`) {
		t.Fatalf("expected cmd attribute, got: %s", logOutput)
	}
	if !strings.Contains(logOutput, "err=boom") {
		t.Fatalf("expected err attribute, got: %s", logOutput)
	}
}

func TestLoggingRunnerEmitsTelemetryForTargetedInternalCommandsOnly(t *testing.T) {
	var captured []capturedCommand

	runner := LoggingRunner{
		Base: testutil.FakeRunner{
			Outputs: map[string]string{
				"git -C /tmp/repo status --short": "M internal/environment/environment.go\n",
				"gh issue list":                   "[]",
			},
		},
		CaptureCommand: func(_ context.Context, name string, args []string, exitCode int, durationMs int64) {
			if strings.TrimSpace(name) == "git" {
				captured = append(captured, capturedCommand{
					Name:       name,
					Args:       append([]string(nil), args...),
					ExitCode:   exitCode,
					DurationMs: durationMs,
				})
			}
		},
	}

	if _, err := runner.Run(context.Background(), "", "git", "-C", "/tmp/repo", "status", "--short"); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(context.Background(), "", "gh", "issue", "list"); err != nil {
		t.Fatal(err)
	}
	if len(captured) != 1 {
		t.Fatalf("expected 1 captured command, got %d", len(captured))
	}
	if got, want := captured[0].Name, "git"; got != want {
		t.Fatalf("name = %q, want %q", got, want)
	}
	if got, want := strings.Join(captured[0].Args, " "), "-C /tmp/repo status --short"; got != want {
		t.Fatalf("args = %q, want %q", got, want)
	}
	if captured[0].ExitCode != 0 {
		t.Fatalf("exitCode = %d, want 0", captured[0].ExitCode)
	}
	if captured[0].DurationMs < 0 {
		t.Fatalf("durationMs = %d, want non-negative", captured[0].DurationMs)
	}
}

func TestLoggingRunnerAccessLogDefaultsToDaemonContext(t *testing.T) {
	var entries []AccessLogEntry
	runner := LoggingRunner{
		Base: testutil.FakeRunner{
			Outputs: map[string]string{
				"gh api /user -H Authorization: Bearer super-secret --jq .login": "octocat\n",
			},
		},
		AccessLog: func(entry AccessLogEntry) {
			entries = append(entries, entry)
		},
	}

	if _, err := runner.Run(context.Background(), "/tmp/repo", "gh", "api", "/user", "-H", "Authorization: Bearer super-secret", "--jq", ".login"); err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 access log entry, got %d", len(entries))
	}
	if got, want := entries[0].ExecutionContext, "daemon"; got != want {
		t.Fatalf("context = %q, want %q", got, want)
	}
	if got := strings.Join(entries[0].Argv, " "); strings.Contains(got, "super-secret") {
		t.Fatalf("expected sanitized argv, got %q", got)
	}
	if got := strings.Join(entries[0].Argv, " "); !strings.Contains(got, "-H <redacted>") {
		t.Fatalf("expected redacted header, got %q", got)
	}
}

func TestLoggingRunnerAccessLogIncludesSessionContext(t *testing.T) {
	var entries []AccessLogEntry
	runner := LoggingRunner{
		Base: testutil.FakeRunner{
			Outputs: map[string]string{
				"git status --short": "M README.md\n",
			},
		},
		AccessLog: func(entry AccessLogEntry) {
			entries = append(entries, entry)
		},
	}
	ctx := WithAccessLogContext(context.Background(), AccessLogContext{
		ExecutionContext: "session",
		Repo:             "owner/repo",
		IssueNumber:      7,
		Branch:           "vigilante/issue-7",
		WorktreePath:     "/tmp/worktree",
	})

	if _, err := runner.Run(ctx, "/tmp/worktree", "git", "status", "--short"); err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 access log entry, got %d", len(entries))
	}
	if got, want := entries[0].ExecutionContext, "session"; got != want {
		t.Fatalf("context = %q, want %q", got, want)
	}
	if got, want := entries[0].Repo, "owner/repo"; got != want {
		t.Fatalf("repo = %q, want %q", got, want)
	}
	if got, want := entries[0].IssueNumber, 7; got != want {
		t.Fatalf("issue = %d, want %d", got, want)
	}
}

type capturedCommand struct {
	Name       string
	Args       []string
	ExitCode   int
	DurationMs int64
}
