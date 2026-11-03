package app

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMain roots the whole package at a throwaway state directory. Several
// tests build an App without calling t.Setenv("VIGILANTE_HOME", ...), and the
// store imports and renames a legacy watchlist.json the first time it opens,
// so a test that falls through to the developer's real ~/.vigilante is
// destructive rather than merely noisy. Tests that set VIGILANTE_HOME
// themselves still override this per test.
//
// It also enables comment commands so the existing command-handling tests keep
// exercising that path while it is switched off in production. Tests for the
// disabled behavior use disableCommentCommands.
func TestMain(m *testing.M) {
	commentCommandsEnabled = true
	root, err := os.MkdirTemp("", "vigilante-app-test-")
	if err != nil {
		panic(err)
	}
	if err := os.Setenv("VIGILANTE_HOME", filepath.Join(root, ".vigilante")); err != nil {
		panic(err)
	}
	code := m.Run()
	_ = os.RemoveAll(root)
	os.Exit(code)
}
