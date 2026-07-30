package runner

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nicobistolfi/vigilante/internal/logging"
	"github.com/nicobistolfi/vigilante/internal/state"
)

// withSessionLogLimits narrows the process-wide rotation limits for the duration
// of a test so rotation can be exercised without writing megabytes.
func withSessionLogLimits(t *testing.T, path string, limits logging.Limits) {
	t.Helper()
	logging.Configure(limits, nil, nil)
	t.Cleanup(func() {
		_ = logging.OpenWriter(path).Close()
		logging.Configure(logging.DefaultLimits(), nil, nil)
	})
}

func TestAppendSessionLogRotates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "owner-repo-issue-472.log")
	withSessionLogLimits(t, path, logging.Limits{MaxFileSize: 300, MaxBackups: 2, MaxTotalSize: 1 << 20})

	session := state.Session{Repo: "owner/repo", IssueNumber: 472, Provider: "claude", Branch: "topic", Status: state.SessionStatusRunning}
	for i := range 20 {
		appendSessionLog(path, fmt.Sprintf("event %d", i), session, "")
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > 300 {
		t.Fatalf("session log size = %d, want <= 300", info.Size())
	}
	if _, err := os.Stat(path + ".1"); err != nil {
		t.Fatalf("expected rotated session log: %v", err)
	}
	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(current), "event 19") {
		t.Fatalf("latest event missing from current session log: %q", current)
	}
}

// The streaming writer from openSessionLogWriter and the open/append/close
// appendSessionLog path must agree on which file is current, so a rotation
// triggered by one is immediately visible to the other.
func TestSessionLogStreamingAndAppendStayConsistentAcrossRotation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "owner-repo-issue-472.log")
	withSessionLogLimits(t, path, logging.Limits{MaxFileSize: 400, MaxBackups: 3, MaxTotalSize: 1 << 20})

	writer, err := openSessionLogWriter(path)
	if err != nil {
		t.Fatalf("openSessionLogWriter: %v", err)
	}
	defer writer.Close()

	session := state.Session{Repo: "owner/repo", IssueNumber: 472, Provider: "claude", Branch: "topic", Status: state.SessionStatusRunning}

	// Interleave streamed provider output with lifecycle events until several
	// rotations have happened.
	for i := range 15 {
		writeLifecycleEvent(writer, fmt.Sprintf("streamed chunk %d %s", i, strings.Repeat("o", 40)))
		appendSessionLog(path, fmt.Sprintf("event %d", i), session, "")
	}
	writeLifecycleEvent(writer, "final streamed line")
	appendSessionLog(path, "final event", session, "")

	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(current)
	if !strings.Contains(got, "final streamed line") {
		t.Fatalf("streaming writer wrote to a stale file, current log = %q", got)
	}
	if !strings.Contains(got, "final event") {
		t.Fatalf("appendSessionLog wrote to a stale file, current log = %q", got)
	}
}

// Rotated session logs must not look like a session log for a different issue,
// or `vigilante logs` would list them as a new repo/issue pair.
func TestRotatedSessionLogNameIsNotASessionLogPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	store := state.NewStore()

	path := store.SessionLogPath("owner/repo", 472)
	rotated := path + ".1"
	for issue := 1; issue <= 500; issue++ {
		if store.SessionLogPath("owner/repo", issue) == rotated {
			t.Fatalf("rotated name %q collides with session log for issue %d", rotated, issue)
		}
	}
	if !strings.HasSuffix(path, ".log") {
		t.Fatalf("unexpected session log suffix in %q", path)
	}
	if strings.HasSuffix(rotated, ".log") {
		t.Fatalf("rotated name %q must not end in .log", rotated)
	}
}

// A rotation must never abort a session: the writers swallow filesystem errors
// the same way the original append paths did.
func TestSessionLogWritesSurviveUnwritableDirectory(t *testing.T) {
	dir := t.TempDir()
	blocked := filepath.Join(dir, "blocked")
	if err := os.WriteFile(blocked, []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(blocked, "owner-repo-issue-1.log")
	withSessionLogLimits(t, path, logging.Limits{MaxFileSize: 10, MaxBackups: 1, MaxTotalSize: 100})

	session := state.Session{Repo: "owner/repo", IssueNumber: 1, Status: state.SessionStatusRunning}
	appendSessionLog(path, "event", session, "details")

	if _, err := openSessionLogWriter(path); err == nil {
		t.Fatal("expected openSessionLogWriter to report the unusable directory")
	}
}
