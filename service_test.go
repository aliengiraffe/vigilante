package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderLaunchdPlist(t *testing.T) {
	state := &StateStore{root: filepath.Join(t.TempDir(), ".vigilante")}
	plist := renderLaunchdPlist(state)
	if !strings.Contains(plist, "<string>daemon</string>") || !strings.Contains(plist, state.LogsDir()) {
		t.Fatalf("unexpected plist: %s", plist)
	}
}

func TestRenderSystemdUnit(t *testing.T) {
	state := &StateStore{root: filepath.Join(t.TempDir(), ".vigilante")}
	unit := renderSystemdUnit(state)
	if !strings.Contains(unit, "ExecStart=") || !strings.Contains(unit, state.LogsDir()) {
		t.Fatalf("unexpected unit: %s", unit)
	}
}
