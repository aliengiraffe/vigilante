package app

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nicobistolfi/vigilante/internal/environment"
	"github.com/nicobistolfi/vigilante/internal/testutil"
)

func TestFormatAccessLogEntry(t *testing.T) {
	entry := environment.AccessLogEntry{
		Timestamp:        "2026-03-26T17:42:36.123456789Z",
		CompletedAt:      "2026-03-26T17:42:36.200000000Z",
		ExecutionContext: "daemon",
		Repo:             "owner/repo",
		IssueNumber:      42,
		Tool:             "gh",
		Argv:             []string{"api", "repos/owner/repo/issues"},
		ExitCode:         0,
		DurationMs:       77,
		Success:          true,
	}
	out := formatAccessLogEntry(entry)
	if !strings.Contains(out, "[daemon]") {
		t.Errorf("expected context in output, got %q", out)
	}
	if !strings.Contains(out, "gh api repos/owner/repo/issues") {
		t.Errorf("expected command in output, got %q", out)
	}
	if !strings.Contains(out, "77ms") {
		t.Errorf("expected duration in output, got %q", out)
	}
	if !strings.Contains(out, "✓") {
		t.Errorf("expected success indicator, got %q", out)
	}
	if !strings.Contains(out, "repo: owner/repo #42") {
		t.Errorf("expected repo and issue detail, got %q", out)
	}
}

func TestFormatAccessLogEntryFailure(t *testing.T) {
	entry := environment.AccessLogEntry{
		Timestamp:        "2026-03-26T10:00:00Z",
		CompletedAt:      "2026-03-26T10:00:01Z",
		ExecutionContext: "session",
		Tool:             "go",
		Argv:             []string{"test", "./..."},
		ExitCode:         1,
		DurationMs:       1200,
		Success:          false,
	}
	out := formatAccessLogEntry(entry)
	if !strings.Contains(out, "✗") {
		t.Errorf("expected failure indicator, got %q", out)
	}
	if !strings.Contains(out, "[session]") {
		t.Errorf("expected session context, got %q", out)
	}
	if !strings.Contains(out, "1.2s") {
		t.Errorf("expected duration in seconds, got %q", out)
	}
	if !strings.Contains(out, "exit: 1") {
		t.Errorf("expected exit code detail, got %q", out)
	}
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		ms   int64
		want string
	}{
		{42, "42ms"},
		{999, "999ms"},
		{1000, "1.0s"},
		{1500, "1.5s"},
		{59999, "60.0s"},
		{60000, "1.0m"},
		{120000, "2.0m"},
	}
	for _, tt := range tests {
		got := formatDuration(tt.ms)
		if got != tt.want {
			t.Errorf("formatDuration(%d) = %q, want %q", tt.ms, got, tt.want)
		}
	}
}

func TestRenderAccessLogLines(t *testing.T) {
	data := `{"timestamp":"2026-03-26T10:00:00Z","completed_at":"2026-03-26T10:00:00.050Z","context":"daemon","tool":"gh","argv":["api"],"exit_code":0,"duration_ms":50,"success":true}
{"timestamp":"2026-03-26T10:00:01Z","completed_at":"2026-03-26T10:00:02Z","context":"session","repo":"owner/repo","issue_number":7,"tool":"git","argv":["status"],"exit_code":0,"duration_ms":120,"success":true}
`
	var buf bytes.Buffer
	if err := renderAccessLogLines(&buf, []byte(data)); err != nil {
		t.Fatal(err)
	}
	output := buf.String()
	if !strings.Contains(output, "[daemon]") {
		t.Errorf("expected daemon context, got %q", output)
	}
	if !strings.Contains(output, "[session]") {
		t.Errorf("expected session context, got %q", output)
	}
	if !strings.Contains(output, "gh api") {
		t.Errorf("expected gh command, got %q", output)
	}
	if !strings.Contains(output, "git status") {
		t.Errorf("expected git command, got %q", output)
	}
}

func TestRenderAccessLogLinesMalformed(t *testing.T) {
	data := `{"timestamp":"2026-03-26T10:00:00Z","context":"daemon","tool":"gh","argv":[],"exit_code":0,"duration_ms":10,"success":true}
not valid json at all
{"timestamp":"2026-03-26T10:00:01Z","context":"daemon","tool":"git","argv":["status"],"exit_code":0,"duration_ms":20,"success":true}
`
	var buf bytes.Buffer
	if err := renderAccessLogLines(&buf, []byte(data)); err != nil {
		t.Fatal(err)
	}
	output := buf.String()
	if !strings.Contains(output, "[malformed]") {
		t.Errorf("expected malformed marker for bad line, got %q", output)
	}
	if !strings.Contains(output, "not valid json at all") {
		t.Errorf("expected malformed line content preserved, got %q", output)
	}
	if !strings.Contains(output, "git status") {
		t.Errorf("expected valid entries after malformed line, got %q", output)
	}
}

func TestRenderAccessLogLinesEmpty(t *testing.T) {
	var buf bytes.Buffer
	if err := renderAccessLogLines(&buf, []byte("")); err != nil {
		t.Fatal(err)
	}
	if buf.Len() != 0 {
		t.Errorf("expected empty output for empty input, got %q", buf.String())
	}
}

func TestWatchAccessLogStopsOnContextCancellation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	logsDir := filepath.Join(home, ".vigilante", "logs")
	if err := os.MkdirAll(logsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	logContent := `{"timestamp":"2026-03-26T10:00:00Z","context":"daemon","tool":"gh","argv":["api"],"exit_code":0,"duration_ms":50,"success":true}
`
	logPath := filepath.Join(logsDir, "access.jsonl")
	if err := os.WriteFile(logPath, []byte(logContent), 0o644); err != nil {
		t.Fatal(err)
	}

	app := New()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var stdout bytes.Buffer
	app.stdout = &stdout
	app.stderr = testutil.IODiscard{}

	if err := app.watchAccessLog(ctx, logPath); err != nil {
		t.Fatalf("expected watch mode to stop cleanly, got %v", err)
	}
	if !strings.Contains(stdout.String(), "[daemon]") {
		t.Errorf("expected existing entries to be rendered, got %q", stdout.String())
	}
}

func TestWatchAccessLogMissingFile(t *testing.T) {
	app := New()
	var stdout bytes.Buffer
	app.stdout = &stdout

	err := app.watchAccessLog(context.Background(), "/nonexistent/path/access.jsonl")
	if err == nil || !strings.Contains(err.Error(), "no access log found") {
		t.Fatalf("expected 'no access log found' error, got %v", err)
	}
}

func TestLogsWatchFlagRequiresAccess(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	app := New()
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	app.stdout = &stdout
	app.stderr = &stderr

	exitCode := app.Run(context.Background(), []string{"logs", "-w"})
	if exitCode != 1 {
		t.Fatalf("expected failure exit code, got %d", exitCode)
	}
	if !strings.Contains(stderr.String(), "-w is only supported with --access") {
		t.Fatalf("expected flag validation error, got %q", stderr.String())
	}
}

func TestLogsAccessWatchFlag(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	logsDir := filepath.Join(home, ".vigilante", "logs")
	if err := os.MkdirAll(logsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	logContent := `{"timestamp":"2026-03-26T10:00:00Z","context":"daemon","tool":"gh","argv":["api"],"exit_code":0,"duration_ms":50,"success":true}
`
	if err := os.WriteFile(filepath.Join(logsDir, "access.jsonl"), []byte(logContent), 0o644); err != nil {
		t.Fatal(err)
	}

	app := New()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var stdout bytes.Buffer
	app.stdout = &stdout
	app.stderr = testutil.IODiscard{}

	exitCode := app.Run(ctx, []string{"logs", "--access", "-w"})
	if exitCode != 0 {
		t.Fatalf("expected success exit code, got %d", exitCode)
	}
	if !strings.Contains(stdout.String(), "[daemon]") {
		t.Errorf("expected formatted output, got %q", stdout.String())
	}
}

func TestWatchAccessLogPicksUpNewEntries(t *testing.T) {
	home := t.TempDir()
	logsDir := filepath.Join(home, ".vigilante", "logs")
	if err := os.MkdirAll(logsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(logsDir, "access.jsonl")
	if err := os.WriteFile(logPath, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}

	app := New()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	var stdout bytes.Buffer
	app.stdout = &stdout
	app.stderr = testutil.IODiscard{}

	done := make(chan error, 1)
	go func() {
		done <- app.watchAccessLog(ctx, logPath)
	}()

	// Give the watcher time to start.
	time.Sleep(100 * time.Millisecond)

	// Append a new entry to the log file.
	entry := `{"timestamp":"2026-03-26T12:00:00Z","context":"session","tool":"make","argv":["build"],"exit_code":0,"duration_ms":300,"success":true}` + "\n"
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(entry); err != nil {
		f.Close()
		t.Fatal(err)
	}
	f.Close()

	// Wait for the watcher to pick up the new entry.
	time.Sleep(700 * time.Millisecond)
	cancel()
	<-done

	output := stdout.String()
	if !strings.Contains(output, "make build") {
		t.Errorf("expected appended entry to appear in output, got %q", output)
	}
}

func TestLogsFollowFlagRequiresRepoAndIssue(t *testing.T) {
	app := New()
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	app.stdout = &stdout
	app.stderr = &stderr

	exitCode := app.Run(context.Background(), []string{"logs", "-f"})
	if exitCode != 1 {
		t.Fatalf("expected failure exit code, got %d", exitCode)
	}
	if !strings.Contains(stderr.String(), "-f requires --repo and --issue") {
		t.Fatalf("expected flag validation error, got %q", stderr.String())
	}
}

func TestLogsFollowFlagCannotCombineWithAccess(t *testing.T) {
	app := New()
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	app.stdout = &stdout
	app.stderr = &stderr

	exitCode := app.Run(context.Background(), []string{"logs", "-f", "--access", "--repo", "owner/repo", "--issue", "7"})
	if exitCode != 1 {
		t.Fatalf("expected failure exit code, got %d", exitCode)
	}
	if !strings.Contains(stderr.String(), "-f cannot be combined with --access") {
		t.Fatalf("expected flag validation error, got %q", stderr.String())
	}
}

func TestLogsFollowFlagWaitsForMissingFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	logsDir := filepath.Join(home, ".vigilante", "logs")
	if err := os.MkdirAll(logsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	app := New()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	app.stdout = &stdout
	app.stderr = &stderr

	done := make(chan int, 1)
	go func() {
		done <- app.Run(ctx, []string{"logs", "--repo", "owner/repo", "--issue", "7", "-f"})
	}()

	// Give the watcher time to start waiting.
	time.Sleep(100 * time.Millisecond)

	// Create the log file.
	logPath := filepath.Join(logsDir, "owner-repo-issue-7.log")
	if err := os.WriteFile(logPath, []byte("session log content\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Wait for the watcher to pick up the file.
	time.Sleep(500 * time.Millisecond)
	cancel()

	exitCode := <-done
	if exitCode != 0 {
		t.Fatalf("expected success exit code, got %d", exitCode)
	}
	if !strings.Contains(stdout.String(), "session log content") {
		t.Errorf("expected session log content in output, got %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "waiting for session log to appear") {
		t.Errorf("expected waiting message on stderr, got %q", stderr.String())
	}
}

func TestWatchSessionLogWaitsThenTails(t *testing.T) {
	home := t.TempDir()
	logsDir := filepath.Join(home, ".vigilante", "logs")
	if err := os.MkdirAll(logsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(logsDir, "owner-repo-issue-7.log")

	app := New()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	var stdout bytes.Buffer
	app.stdout = &stdout
	app.stderr = testutil.IODiscard{}

	done := make(chan error, 1)
	go func() {
		done <- app.watchSessionLog(ctx, logPath)
	}()

	// Give watcher time to start waiting.
	time.Sleep(100 * time.Millisecond)

	// Create the file with initial content.
	if err := os.WriteFile(logPath, []byte("line one\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Wait for watcher to pick up the file and print content.
	time.Sleep(500 * time.Millisecond)
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("expected clean exit, got %v", err)
	}

	if !strings.Contains(stdout.String(), "line one") {
		t.Errorf("expected file content in output, got %q", stdout.String())
	}
}

func TestWatchSessionLogCancelDuringWait(t *testing.T) {
	home := t.TempDir()
	logsDir := filepath.Join(home, ".vigilante", "logs")
	if err := os.MkdirAll(logsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(logsDir, "nonexistent.log")

	app := New()
	ctx, cancel := context.WithCancel(context.Background())

	var stdout bytes.Buffer
	app.stdout = &stdout
	app.stderr = testutil.IODiscard{}

	done := make(chan error, 1)
	go func() {
		done <- app.watchSessionLog(ctx, logPath)
	}()

	// Give watcher time to start waiting, then cancel.
	time.Sleep(100 * time.Millisecond)
	cancel()

	err := <-done
	if err == nil || err != context.Canceled {
		t.Fatalf("expected context.Canceled error, got %v", err)
	}
}

func TestLogsNonFollowMissingFileStillErrors(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	logsDir := filepath.Join(home, ".vigilante", "logs")
	if err := os.MkdirAll(logsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	app := New()
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	app.stdout = &stdout
	app.stderr = &stderr

	exitCode := app.Run(context.Background(), []string{"logs", "--repo", "owner/repo", "--issue", "7"})
	if exitCode != 1 {
		t.Fatalf("expected failure exit code for non-follow missing file, got %d", exitCode)
	}
	if !strings.Contains(stderr.String(), "no log found for") {
		t.Fatalf("expected 'no log found' error, got %q", stderr.String())
	}
}

func TestWatchSessionLogPrintsExistingAndNewContent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	logsDir := filepath.Join(home, ".vigilante", "logs")
	if err := os.MkdirAll(logsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(logsDir, "owner-repo-issue-7.log")
	if err := os.WriteFile(logPath, []byte("[vigilante] session started\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	app := New()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	var stdout bytes.Buffer
	app.stdout = &stdout
	app.stderr = testutil.IODiscard{}

	done := make(chan error, 1)
	go func() {
		done <- app.watchSessionLog(ctx, logPath)
	}()

	// Give watcher time to start and print existing content.
	time.Sleep(100 * time.Millisecond)

	// Append new content.
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("[vigilante] implementation starting\n"); err != nil {
		f.Close()
		t.Fatal(err)
	}
	f.Close()

	// Wait for poll to pick up the new content.
	time.Sleep(500 * time.Millisecond)
	cancel()
	<-done

	output := stdout.String()
	if !strings.Contains(output, "session started") {
		t.Errorf("expected existing content, got %q", output)
	}
	if !strings.Contains(output, "implementation starting") {
		t.Errorf("expected appended content, got %q", output)
	}
}
