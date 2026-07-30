package app

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nicobistolfi/vigilante/internal/environment"
	"github.com/nicobistolfi/vigilante/internal/logging"
	"github.com/nicobistolfi/vigilante/internal/state"
	"github.com/nicobistolfi/vigilante/internal/testutil"
)

// newRotationTestApp points the app at an isolated VIGILANTE_HOME with a tiny
// rotation size, so tests exercise rotation without writing 500MB.
func newRotationTestApp(t *testing.T, limits logging.Limits) *App {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))

	app := New()
	if err := app.state.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	logging.Configure(limits, app.state.LiveLogPaths, nil)
	t.Cleanup(func() {
		_ = logging.OpenWriter(app.state.DaemonLogPath()).Close()
		_ = logging.OpenWriter(app.state.AccessLogPath()).Close()
		logging.Configure(logging.DefaultLimits(), nil, nil)
	})
	app.stderr = testutil.IODiscard{}
	return app
}

func TestConfigureLogRotationUsesConfiguredLimits(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, ".vigilante")
	t.Setenv("HOME", home)
	t.Setenv("VIGILANTE_HOME", root)

	store := state.NewStore()
	if err := store.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	backups := 1
	if err := store.SaveServiceConfig(state.ServiceConfig{
		LogMaxTotalSize: "4MB",
		LogMaxFileSize:  "1MB",
		LogMaxBackups:   &backups,
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { logging.Configure(logging.DefaultLimits(), nil, nil) })

	limits := configureLogRotation(store)
	if limits.MaxTotalSize != 4*1024*1024 || limits.MaxFileSize != 1024*1024 || limits.MaxBackups != 1 {
		t.Fatalf("unexpected limits: %+v", limits)
	}
	if got := logging.CurrentLimits(); got != limits {
		t.Fatalf("CurrentLimits() = %+v, want %+v", got, limits)
	}
}

// A malformed size must not stop the daemon from starting or from logging.
func TestConfigureLogRotationFallsBackAndKeepsLogging(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, ".vigilante")
	t.Setenv("HOME", home)
	t.Setenv("VIGILANTE_HOME", root)

	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	malformed := `{"log_max_total_size": "definitely not a size", "log_max_file_size": "-7"}` + "\n"
	if err := os.WriteFile(filepath.Join(root, "config.json"), []byte(malformed), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { logging.Configure(logging.DefaultLimits(), nil, nil) })

	app := New()
	if got := logging.CurrentLimits(); got != logging.DefaultLimits() {
		t.Fatalf("CurrentLimits() = %+v, want defaults %+v", got, logging.DefaultLimits())
	}

	app.logger.Info("daemon still logging")
	data, err := os.ReadFile(app.state.DaemonLogPath())
	if err != nil {
		t.Fatalf("read daemon log: %v", err)
	}
	if !strings.Contains(string(data), "daemon still logging") {
		t.Fatalf("daemon log missing line: %q", data)
	}
}

// The daemon logger is created once for the process lifetime; it must keep
// writing to the current file after a rotation instead of the renamed inode.
func TestDaemonLoggerKeepsLoggingAcrossRotation(t *testing.T) {
	app := newRotationTestApp(t, logging.Limits{MaxFileSize: 400, MaxBackups: 3, MaxTotalSize: 1 << 20})

	for i := range 25 {
		app.logger.Info("scan tick", "iteration", i, "padding", strings.Repeat("q", 20))
	}
	app.logger.Info("line after rotation")

	if _, err := os.Stat(app.state.DaemonLogPath() + ".1"); err != nil {
		t.Fatalf("expected a rotated daemon log: %v", err)
	}
	current, err := os.ReadFile(app.state.DaemonLogPath())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(current), "line after rotation") {
		t.Fatalf("daemon stopped logging into the current file: %q", current)
	}
}

func TestLogsListingIncludesRotatedFiles(t *testing.T) {
	app := newRotationTestApp(t, logging.Limits{MaxFileSize: 300, MaxBackups: 3, MaxTotalSize: 1 << 20})

	for i := range 20 {
		app.state.AppendDaemonLog("daemon line %d with padding to force a rotation", i)
	}

	var stdout bytes.Buffer
	app.stdout = &stdout
	if code := app.Run(context.Background(), []string{"logs"}); code != 0 {
		t.Fatalf("logs exited with %d", code)
	}

	output := stdout.String()
	if !strings.Contains(output, "vigilante.log\n") && !strings.Contains(output, "vigilante.log ") {
		t.Fatalf("listing missing current log: %q", output)
	}
	if !strings.Contains(output, "vigilante.log.1") {
		t.Fatalf("listing missing rotated log: %q", output)
	}
}

func TestLogsAccessReadsCurrentFileAfterRotation(t *testing.T) {
	app := newRotationTestApp(t, logging.Limits{MaxFileSize: 300, MaxBackups: 3, MaxTotalSize: 1 << 20})

	for i := range 20 {
		app.state.AppendAccessLogEntry(newTestAccessLogEntry(i))
	}
	if _, err := os.Stat(app.state.AccessLogPath() + ".1"); err != nil {
		t.Fatalf("expected the access log to have rotated: %v", err)
	}

	var stdout bytes.Buffer
	app.stdout = &stdout
	if code := app.Run(context.Background(), []string{"logs", "--access"}); code != 0 {
		t.Fatalf("logs --access exited with %d: %q", code, stdout.String())
	}
	if !strings.Contains(stdout.String(), "git status") {
		t.Fatalf("expected rendered access entries, got %q", stdout.String())
	}
}

func TestLogsRepoIssueReadsCurrentFileAfterRotation(t *testing.T) {
	app := newRotationTestApp(t, logging.Limits{MaxFileSize: 200, MaxBackups: 3, MaxTotalSize: 1 << 20})

	path := app.state.SessionLogPath("owner/repo", 472)
	writer := logging.OpenWriter(path)
	t.Cleanup(func() { _ = writer.Close() })
	for i := range 20 {
		if _, err := fmt.Fprintf(writer, "session line %d with padding\n", i); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(path + ".1"); err != nil {
		t.Fatalf("expected the session log to have rotated: %v", err)
	}

	var stdout bytes.Buffer
	app.stdout = &stdout
	if code := app.Run(context.Background(), []string{"logs", "--repo", "owner/repo", "--issue", "472"}); code != 0 {
		t.Fatalf("logs --repo/--issue exited with %d: %q", code, stdout.String())
	}
	if !strings.Contains(stdout.String(), "session line 19") {
		t.Fatalf("expected the current session log contents, got %q", stdout.String())
	}
}

// Following a session log must survive a rotation: watchPlaintextLog resets its
// byte offset when the file shrinks, so new lines keep arriving.
func TestWatchSessionLogSurvivesRotation(t *testing.T) {
	app := newRotationTestApp(t, logging.Limits{MaxFileSize: 120, MaxBackups: 3, MaxTotalSize: 1 << 20})

	path := app.state.SessionLogPath("owner/repo", 8)
	writer := logging.OpenWriter(path)
	t.Cleanup(func() { _ = writer.Close() })
	if _, err := writer.Write([]byte("before rotation\n")); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// The follow runs in a goroutine while the test reads what it printed, so the
	// sink has to be safe for concurrent use.
	stdout := &syncBuffer{}
	app.stdout = stdout

	done := make(chan error, 1)
	go func() { done <- app.watchSessionLog(ctx, path) }()

	time.Sleep(150 * time.Millisecond)

	// Force a rotation, then write past it.
	if _, err := writer.Write([]byte(strings.Repeat("f", 130) + "\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + ".1"); err != nil {
		t.Fatalf("expected a rotation while following: %v", err)
	}
	if _, err := writer.Write([]byte("after rotation\n")); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(stdout.String(), "after rotation") {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil && !strings.Contains(err.Error(), context.Canceled.Error()) {
		t.Fatalf("watchSessionLog returned %v", err)
	}

	output := stdout.String()
	if !strings.Contains(output, "before rotation") {
		t.Fatalf("expected pre-rotation content, got %q", output)
	}
	if !strings.Contains(output, "after rotation") {
		t.Fatalf("follow stopped emitting after rotation, got %q", output)
	}
}

func TestSweepLogsDirEnforcesBudget(t *testing.T) {
	app := newRotationTestApp(t, logging.Limits{MaxFileSize: 1 << 20, MaxBackups: 3, MaxTotalSize: 900})

	daemonLog := app.state.DaemonLogPath()
	blob := strings.Repeat("x", 400)
	for _, path := range []string{
		daemonLog,
		daemonLog + ".1",
		daemonLog + ".2",
		app.state.AccessLogPath(),
		app.state.SessionLogPath("owner/repo", 1),
	} {
		if err := os.WriteFile(path, []byte(blob), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	before := logsDirSize(t, app.state.LogsDir())
	daemonBefore := fileSize(t, daemonLog)

	app.sweepLogsDir()

	// The sweep records its own audit line in the daemon log, so discount that
	// growth when checking convergence.
	after := logsDirSize(t, app.state.LogsDir()) - (fileSize(t, daemonLog) - daemonBefore)
	if before <= 900 {
		t.Fatalf("test setup did not exceed the budget: %d", before)
	}
	if after > 900 {
		t.Fatalf("logs directory size after sweep = %d, want <= 900", after)
	}
	for _, path := range []string{daemonLog, app.state.AccessLogPath()} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("live log %s was evicted: %v", path, err)
		}
	}
}

func logsDirSize(t *testing.T, dir string) int64 {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var total int64
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue
		}
		total += info.Size()
	}
	return total
}

func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Size()
}

// A sweep failure must be logged and swallowed, never returned up the daemon loop.
func TestSweepLogsDirIsNonFatalWhenLogsDirIsMissing(t *testing.T) {
	app := newRotationTestApp(t, logging.Limits{MaxFileSize: 1 << 20, MaxBackups: 3, MaxTotalSize: 100})

	if err := os.RemoveAll(app.state.LogsDir()); err != nil {
		t.Fatal(err)
	}
	app.sweepLogsDir()
}

func newTestAccessLogEntry(i int) environment.AccessLogEntry {
	return environment.AccessLogEntry{
		Timestamp:        "2026-07-30T12:00:00Z",
		ExecutionContext: "scan",
		Tool:             "git",
		Argv:             []string{"git", "status"},
		ExitCode:         0,
		DurationMs:       int64(i),
		Success:          true,
	}
}

// syncBuffer is a bytes.Buffer that is safe to write from a follow goroutine
// while the test reads it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
