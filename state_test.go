package main

import (
	"path/filepath"
	"testing"
)

func TestTryWithScanLockIsExclusive(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))

	stateA := NewStateStore()
	stateB := NewStateStore()
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
