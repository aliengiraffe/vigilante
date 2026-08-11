package state

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCoverageStateDefaultsAndHomeOverrides(t *testing.T) {
	if got := (WatchTarget{}).EffectiveIssueStage(); got != "" {
		t.Fatalf("github stage=%q", got)
	}
	if got := (WatchTarget{IssueBackend: "linear"}).EffectiveIssueStage(); got != "Todo" {
		t.Fatalf("linear stage=%q", got)
	}
	if got := (WatchTarget{IssueStage: " Doing "}).EffectiveIssueStage(); got != "Doing" {
		t.Fatalf("explicit stage=%q", got)
	}
	t.Setenv("VIGILANTE_HOME", t.TempDir())
	t.Setenv("CODEX_HOME", "/tmp/codex")
	t.Setenv("CLAUDE_HOME", "/tmp/claude")
	t.Setenv("GEMINI_HOME", "/tmp/gemini")
	t.Setenv("OPENCODE_HOME", "/tmp/opencode")
	s := NewStore()
	if s.CodexHome() != "/tmp/codex" || s.ClaudeHome() != "/tmp/claude" || s.GeminiHome() != "/tmp/gemini" || s.OpenCodeHome() != "/tmp/opencode" {
		t.Fatal("home overrides")
	}
}

func TestCoverageStateFileErrorPaths(t *testing.T) {
	dir := t.TempDir()
	rootFile := filepath.Join(dir, "root-file")
	if err := os.WriteFile(rootFile, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VIGILANTE_HOME", rootFile)
	s := NewStore()
	if err := s.EnsureLayout(); err == nil {
		t.Fatal("expected layout error")
	}
	if ok, err := s.TryWithScanLock(func() error { return nil }); ok || err == nil {
		t.Fatalf("lock=%v err=%v", ok, err)
	}
	missingParent := filepath.Join(dir, "missing", "array.json")
	if err := ensureJSONArrayFile(missingParent); err == nil {
		t.Fatal("expected array write error")
	}
	existing := filepath.Join(dir, "existing.json")
	if err := os.WriteFile(existing, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ensureJSONArrayFile(existing); err != nil {
		t.Fatal(err)
	}
	if err := ensureJSONFile(existing, map[string]string{"x": "y"}); err != nil {
		t.Fatal(err)
	}
	if err := ensureJSONFile(filepath.Join(dir, "new.json"), map[string]string{"x": "y"}); err != nil {
		t.Fatal(err)
	}
	var value map[string]string
	if err := readJSONFile(filepath.Join(dir, "missing.json"), &value); err == nil {
		t.Fatal("expected read error")
	}
	if err := writeJSONFile(filepath.Join(dir, "bad.json"), func() {}); err == nil {
		t.Fatal("expected marshal error")
	}
	if err := writeJSONFile(filepath.Join(rootFile, "bad.json"), map[string]string{"x": "y"}); err == nil {
		t.Fatal("expected mkdir error")
	}
	appendJSONLine(filepath.Join(dir, "ignored.log"), func() {})
}

func TestCoverageStateLoadErrorsAndLivePaths(t *testing.T) {
	root := t.TempDir()
	t.Setenv("VIGILANTE_HOME", root)
	s := NewStore()
	if err := s.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path string
		load func() error
	}{
		{s.watchlistPath(), func() error { _, err := s.LoadWatchTargets(); return err }},
		{s.sessionsPath(), func() error { _, err := s.LoadSessions(); return err }},
		{s.serviceConfigPath(), func() error { _, err := s.LoadServiceConfig(); return err }},
	} {
		if err := os.WriteFile(tc.path, []byte("bad"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := tc.load(); err == nil {
			t.Errorf("expected load error for %s", tc.path)
		}
	}
	paths := s.LiveLogPaths()
	if len(paths) != 2 {
		t.Fatalf("fallback live paths=%#v", paths)
	}
	want := errors.New("callback failed")
	ok, err := s.TryWithScanLock(func() error { return want })
	if !ok || !errors.Is(err, want) {
		t.Fatalf("lock=%v err=%v", ok, err)
	}
}
