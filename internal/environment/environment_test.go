package environment

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/nicobistolfi/vigilante/internal/testutil"
)

func TestLoggingRunnerLogsCommandsWithoutSuccessOutputByDefault(t *testing.T) {
	var entries []string
	runner := LoggingRunner{
		Base: testutil.FakeRunner{
			Outputs: map[string]string{
				"gh issue list": "[]",
			},
		},
		Logf: func(format string, args ...any) {
			entries = append(entries, sprintf(format, args...))
		},
	}

	if _, err := runner.Run(context.Background(), "/tmp/repo", "gh", "issue", "list"); err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("unexpected log entries: %#v", entries)
	}
	if !strings.Contains(entries[0], `command start dir="/tmp/repo" cmd=gh issue list`) {
		t.Fatalf("unexpected start log: %s", entries[0])
	}
	if entries[1] != "command ok cmd=gh issue list" {
		t.Fatalf("unexpected success log: %s", entries[1])
	}
}

func TestLoggingRunnerCanLogSuccessOutputWhenEnabled(t *testing.T) {
	var entries []string
	runner := LoggingRunner{
		Base: testutil.FakeRunner{
			Outputs: map[string]string{
				"gh issue list": "[]",
			},
		},
		Logf: func(format string, args ...any) {
			entries = append(entries, sprintf(format, args...))
		},
		LogSuccessOutput: true,
	}

	if _, err := runner.Run(context.Background(), "/tmp/repo", "gh", "issue", "list"); err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("unexpected log entries: %#v", entries)
	}
	if !strings.Contains(entries[1], "command ok cmd=gh issue list output=[]") {
		t.Fatalf("unexpected success log: %s", entries[1])
	}
}

func TestLoggingRunnerLogsFailures(t *testing.T) {
	var entries []string
	runner := LoggingRunner{
		Base: testutil.FakeRunner{
			Errors: map[string]error{
				"git status": fmt.Errorf("boom"),
			},
		},
		Logf: func(format string, args ...any) {
			entries = append(entries, sprintf(format, args...))
		},
	}

	if _, err := runner.Run(context.Background(), "", "git", "status"); err == nil {
		t.Fatal("expected error")
	}
	if len(entries) != 2 {
		t.Fatalf("unexpected log entries: %#v", entries)
	}
	if !strings.Contains(entries[1], "command failed cmd=git status err=boom") {
		t.Fatalf("unexpected failure log: %s", entries[1])
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

func sprintf(format string, args ...any) string {
	return fmt.Sprintf(format, args...)
}

type capturedCommand struct {
	Name       string
	Args       []string
	ExitCode   int
	DurationMs int64
}
