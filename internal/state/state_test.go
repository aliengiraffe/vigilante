package state

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTryWithScanLockIsExclusive(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))

	stateA := NewStore()
	stateB := NewStore()
	if err := stateA.EnsureLayout(); err != nil {
		t.Fatal(err)
	}

	locked, err := stateA.TryWithScanLock(func() error {
		lockedInner, err := stateB.TryWithScanLock(func() error { return nil })
		if err != nil {
			return err
		}
		if lockedInner {
			t.Fatal("expected second scan lock acquisition to fail")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !locked {
		t.Fatal("expected first scan lock acquisition to succeed")
	}
}

func TestAppendLogFileUsesLocalTimezone(t *testing.T) {
	originalLocal := time.Local
	time.Local = time.FixedZone("TEST", -8*60*60)
	t.Cleanup(func() {
		time.Local = originalLocal
	})

	path := filepath.Join(t.TempDir(), "vigilante.log")
	appendLogFile(path, "daemon run start")

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "-08:00] daemon run start") {
		t.Fatalf("expected local timezone offset in log entry, got %q", text)
	}
	if strings.Contains(text, "Z] daemon run start") {
		t.Fatalf("expected local timezone log entry, got %q", text)
	}
}

func TestSessionLogPathUsesRepositoryQualifiedFilename(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))

	store := NewStore()

	got := store.SessionLogPath("aliengiraffe/vigilante", 179)
	want := filepath.Join(store.LogsDir(), "aliengiraffe-vigilante-issue-179.log")
	if got != want {
		t.Fatalf("expected repository-qualified session log path %q, got %q", want, got)
	}
}

func TestSessionLogPathSanitizesRepositorySlug(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))

	store := NewStore()

	got := store.SessionLogPath("Org Name/repo.name:beta", 7)
	want := filepath.Join(store.LogsDir(), "Org-Name-repo-name-beta-issue-7.log")
	if got != want {
		t.Fatalf("expected sanitized session log path %q, got %q", want, got)
	}
}

func TestSessionLogPathDoesNotCollideAcrossRepositories(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))

	store := NewStore()

	first := store.SessionLogPath("owner-one/repo", 7)
	second := store.SessionLogPath("owner-two/repo", 7)
	if first == second {
		t.Fatalf("expected distinct session log paths for shared issue number, got %q", first)
	}
}

func TestSessionLogPathFallsBackWhenRepositorySlugMissing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))

	store := NewStore()

	got := store.SessionLogPath(" / ", 7)
	want := filepath.Join(store.LogsDir(), "unknown-repo-issue-7.log")
	if got != want {
		t.Fatalf("expected fallback session log path %q, got %q", want, got)
	}
}

func TestWatchTargetMaxParallelDefaultsToSharedValue(t *testing.T) {
	if got := normalizeMaxParallelSessions(0); got != DefaultMaxParallelSessions {
		t.Fatalf("expected zero max_parallel_sessions to normalize to shared default %d, got %d", DefaultMaxParallelSessions, got)
	}
	if got := normalizeMaxParallelSessions(-1); got != 1 {
		t.Fatalf("expected negative max_parallel_sessions to normalize conservatively to 1, got %d", got)
	}
	if got := normalizeMaxParallelSessions(1); got != 1 {
		t.Fatalf("expected explicit max_parallel_sessions to be preserved, got %d", got)
	}
}

func TestEnsureLayoutCreatesDefaultServiceConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))

	store := NewStore()
	if err := store.EnsureLayout(); err != nil {
		t.Fatal(err)
	}

	config, err := store.LoadServiceConfig()
	if err != nil {
		t.Fatal(err)
	}
	if got := config.BlockedSessionInactivityTimeout; got != DefaultBlockedSessionInactivityTimeout.String() {
		t.Fatalf("expected default blocked-session timeout %q, got %q", DefaultBlockedSessionInactivityTimeout.String(), got)
	}
}

func TestSaveServiceConfigNormalizesInvalidBlockedSessionTimeout(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))

	store := NewStore()
	if err := store.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveServiceConfig(ServiceConfig{BlockedSessionInactivityTimeout: "not-a-duration"}); err != nil {
		t.Fatal(err)
	}

	config, err := store.LoadServiceConfig()
	if err != nil {
		t.Fatal(err)
	}
	if got := config.BlockedSessionInactivityTimeout; got != DefaultBlockedSessionInactivityTimeout.String() {
		t.Fatalf("expected invalid timeout to normalize to %q, got %q", DefaultBlockedSessionInactivityTimeout.String(), got)
	}
}
