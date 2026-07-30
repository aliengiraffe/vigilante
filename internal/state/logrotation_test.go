package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nicobistolfi/vigilante/internal/environment"
	"github.com/nicobistolfi/vigilante/internal/logging"
)

func newAccessLogEntry(i int) environment.AccessLogEntry {
	return environment.AccessLogEntry{
		Timestamp:        "2026-07-30T12:00:00Z",
		ExecutionContext: "scan",
		Tool:             "git",
		Argv:             []string{"git", "status", strings.Repeat("p", 20)},
		ExitCode:         i % 2,
		Success:          i%2 == 0,
	}
}

func TestLogRotationLimitsDefaultsWhenUnset(t *testing.T) {
	limits := ServiceConfig{}.LogRotationLimits()
	if limits.MaxTotalSize != 500*1024*1024 {
		t.Fatalf("MaxTotalSize = %d, want 500MB", limits.MaxTotalSize)
	}
	if limits.MaxFileSize != logging.DefaultMaxFileSize {
		t.Fatalf("MaxFileSize = %d, want %d", limits.MaxFileSize, logging.DefaultMaxFileSize)
	}
	if limits.MaxBackups != logging.DefaultMaxBackups {
		t.Fatalf("MaxBackups = %d, want %d", limits.MaxBackups, logging.DefaultMaxBackups)
	}
}

func TestLogRotationLimitsFromConfig(t *testing.T) {
	backups := 5
	limits := ServiceConfig{
		LogMaxTotalSize: "1GB",
		LogMaxFileSize:  "8MB",
		LogMaxBackups:   &backups,
	}.LogRotationLimits()

	if limits.MaxTotalSize != 1024*1024*1024 {
		t.Fatalf("MaxTotalSize = %d, want 1GB", limits.MaxTotalSize)
	}
	if limits.MaxFileSize != 8*1024*1024 {
		t.Fatalf("MaxFileSize = %d, want 8MB", limits.MaxFileSize)
	}
	if limits.MaxBackups != 5 {
		t.Fatalf("MaxBackups = %d, want 5", limits.MaxBackups)
	}
}

func TestLogRotationLimitsFallsBackOnMalformedValues(t *testing.T) {
	negative := -3
	limits := ServiceConfig{
		LogMaxTotalSize: "five hundred megabytes",
		LogMaxFileSize:  "",
		LogMaxBackups:   &negative,
	}.LogRotationLimits()

	if limits.MaxTotalSize != logging.DefaultMaxTotalSize {
		t.Fatalf("MaxTotalSize = %d, want default %d", limits.MaxTotalSize, logging.DefaultMaxTotalSize)
	}
	if limits.MaxFileSize != logging.DefaultMaxFileSize {
		t.Fatalf("MaxFileSize = %d, want default %d", limits.MaxFileSize, logging.DefaultMaxFileSize)
	}
	if limits.MaxBackups != logging.DefaultMaxBackups {
		t.Fatalf("MaxBackups = %d, want default %d", limits.MaxBackups, logging.DefaultMaxBackups)
	}
}

func TestServiceConfigLogFieldsRoundTrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))

	store := NewStore()
	if err := store.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	backups := 4
	if err := store.SaveServiceConfig(ServiceConfig{
		LogMaxTotalSize: "250MB",
		LogMaxFileSize:  "10MB",
		LogMaxBackups:   &backups,
	}); err != nil {
		t.Fatal(err)
	}

	config, err := store.LoadServiceConfig()
	if err != nil {
		t.Fatal(err)
	}
	if config.LogMaxTotalSize != "250MB" || config.LogMaxFileSize != "10MB" {
		t.Fatalf("unexpected sizes after round-trip: %+v", config)
	}
	if config.LogMaxBackups == nil || *config.LogMaxBackups != 4 {
		t.Fatalf("unexpected backup count after round-trip: %+v", config.LogMaxBackups)
	}
	limits := config.LogRotationLimits()
	if limits.MaxTotalSize != 250*1024*1024 {
		t.Fatalf("MaxTotalSize = %d, want 250MB", limits.MaxTotalSize)
	}
}

// A config file that predates this feature must keep working untouched and must
// not have the new keys written into it just because defaults were applied.
func TestServiceConfigWithoutLogFieldsUsesDefaults(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, ".vigilante")
	t.Setenv("VIGILANTE_HOME", root)

	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := `{"sandbox_enabled": true}` + "\n"
	if err := os.WriteFile(filepath.Join(root, "config.json"), []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}

	store := NewStore()
	config, err := store.LoadServiceConfig()
	if err != nil {
		t.Fatal(err)
	}
	if config.LogMaxTotalSize != "" || config.LogMaxFileSize != "" || config.LogMaxBackups != nil {
		t.Fatalf("legacy config gained log fields: %+v", config)
	}
	if got := config.LogRotationLimits().MaxTotalSize; got != 500*1024*1024 {
		t.Fatalf("MaxTotalSize = %d, want 500MB default", got)
	}

	raw, err := os.ReadFile(filepath.Join(root, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "log_max_total_size") {
		t.Fatalf("loading config rewrote the file: %s", raw)
	}
}

func TestServiceConfigOmitsUnsetLogFields(t *testing.T) {
	data, err := json.Marshal(ServiceConfig{})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"log_max_total_size", "log_max_file_size", "log_max_backups"} {
		if strings.Contains(string(data), key) {
			t.Fatalf("expected %s to be omitted, got %s", key, data)
		}
	}
}

func TestAppendDaemonLogRotates(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))

	store := NewStore()
	if err := store.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	logging.Configure(logging.Limits{MaxFileSize: 200, MaxBackups: 2, MaxTotalSize: 1 << 20}, store.LiveLogPaths, nil)
	t.Cleanup(func() {
		logging.OpenWriter(store.DaemonLogPath()).Close()
		logging.Configure(logging.DefaultLimits(), nil, nil)
	})

	for i := range 20 {
		store.AppendDaemonLog("daemon line %d with some padding to grow the file", i)
	}

	info, err := os.Stat(store.DaemonLogPath())
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > 200 {
		t.Fatalf("daemon log size = %d, want <= 200", info.Size())
	}
	if _, err := os.Stat(store.DaemonLogPath() + ".1"); err != nil {
		t.Fatalf("expected rotated daemon log: %v", err)
	}

	// The most recent line must be in the current file, not the rotated one.
	current, err := os.ReadFile(store.DaemonLogPath())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(current), "daemon line 19") {
		t.Fatalf("latest line missing from current daemon log: %q", current)
	}
}

func TestAppendAccessLogEntryRotates(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))

	store := NewStore()
	if err := store.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	logging.Configure(logging.Limits{MaxFileSize: 300, MaxBackups: 2, MaxTotalSize: 1 << 20}, store.LiveLogPaths, nil)
	t.Cleanup(func() {
		logging.OpenWriter(store.AccessLogPath()).Close()
		logging.Configure(logging.DefaultLimits(), nil, nil)
	})

	for i := range 20 {
		store.AppendAccessLogEntry(newAccessLogEntry(i))
	}

	info, err := os.Stat(store.AccessLogPath())
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > 300 {
		t.Fatalf("access log size = %d, want <= 300", info.Size())
	}
	if _, err := os.Stat(store.AccessLogPath() + ".1"); err != nil {
		t.Fatalf("expected rotated access log: %v", err)
	}

	// Every retained line must still be valid JSON so `logs --access` keeps working.
	current, err := os.ReadFile(store.AccessLogPath())
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(current)), "\n") {
		if line == "" {
			continue
		}
		var decoded map[string]any
		if err := json.Unmarshal([]byte(line), &decoded); err != nil {
			t.Fatalf("rotated access log holds a torn line %q: %v", line, err)
		}
	}
}

func TestLiveLogPathsProtectsActiveSessionsOnly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))

	store := NewStore()
	if err := store.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSessions([]Session{
		{Repo: "owner/live", IssueNumber: 1, Status: SessionStatusRunning},
		{Repo: "owner/blocked", IssueNumber: 2, Status: SessionStatusBlocked},
		{Repo: "owner/done", IssueNumber: 3, Status: SessionStatusSuccess},
	}); err != nil {
		t.Fatal(err)
	}

	paths := store.LiveLogPaths()
	want := map[string]bool{
		store.DaemonLogPath():                    true,
		store.AccessLogPath():                    true,
		store.SessionLogPath("owner/live", 1):    true,
		store.SessionLogPath("owner/blocked", 2): true,
	}
	if len(paths) != len(want) {
		t.Fatalf("LiveLogPaths() = %v, want %d entries", paths, len(want))
	}
	for _, path := range paths {
		if !want[path] {
			t.Fatalf("unexpected live path %q", path)
		}
	}
	for _, path := range paths {
		if path == store.SessionLogPath("owner/done", 3) {
			t.Fatal("completed session log must not be protected")
		}
	}
}

func TestSweepLogsKeepsRunningSessionLog(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))

	store := NewStore()
	if err := store.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSessions([]Session{
		{Repo: "owner/live", IssueNumber: 1, Status: SessionStatusRunning},
		{Repo: "owner/done", IssueNumber: 2, Status: SessionStatusSuccess},
	}); err != nil {
		t.Fatal(err)
	}

	running := store.SessionLogPath("owner/live", 1)
	completed := store.SessionLogPath("owner/done", 2)
	blob := strings.Repeat("x", 400)
	for _, path := range []string{running, completed, store.DaemonLogPath(), store.AccessLogPath()} {
		if err := os.WriteFile(path, []byte(blob), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	result, err := store.SweepLogs(logging.Limits{MaxTotalSize: 1300})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Removed) != 1 || result.Removed[0] != completed {
		t.Fatalf("expected only the completed session log to be evicted, got %v", result.Removed)
	}
	if _, err := os.Stat(running); err != nil {
		t.Fatalf("running session log evicted: %v", err)
	}
}
