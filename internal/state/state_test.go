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
