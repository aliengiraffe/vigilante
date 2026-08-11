package logging

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNewDaemonLoggerWritesStructuredRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "daemon.log")
	logger, err := NewDaemonLogger(path)
	if err != nil {
		t.Fatal(err)
	}
	logger.Info("started", "issue", 494)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "msg=started") || !strings.Contains(text, "issue=494") || !strings.Contains(text, "time=") {
		t.Fatalf("log record=%q", text)
	}
}

func TestNewDaemonLoggerRejectsInvalidParent(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(parent, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewDaemonLogger(filepath.Join(parent, "daemon.log")); err == nil {
		t.Fatal("expected parent error")
	}
}

func TestDiscardHandlerContract(t *testing.T) {
	h := discardHandler{}
	if h.Enabled(context.Background(), slog.LevelInfo) {
		t.Fatal("discard handler enabled")
	}
	if err := h.Handle(context.Background(), slog.NewRecord(time.Now(), slog.LevelInfo, "ignored", 0)); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.WithAttrs([]slog.Attr{slog.String("key", "value")}).(discardHandler); !ok {
		t.Fatal("WithAttrs changed handler")
	}
	if _, ok := h.WithGroup("group").(discardHandler); !ok {
		t.Fatal("WithGroup changed handler")
	}
	Discard().Info("ignored")
}

func TestConfigureRetunesExistingWriter(t *testing.T) {
	original := CurrentLimits()
	t.Cleanup(func() { Configure(original, nil, nil) })
	path := filepath.Join(t.TempDir(), "configured.log")
	w := OpenWriter(path)
	want := Limits{MaxFileSize: 8, MaxBackups: 1, MaxTotalSize: 64}
	Configure(want, func() []string { return []string{path} }, nil)
	if CurrentLimits() != want || w.limits != want {
		t.Fatalf("limits current=%#v writer=%#v", CurrentLimits(), w.limits)
	}
	if got := livePaths(); len(got) != 1 || got[0] != path {
		t.Fatalf("live paths=%#v", got)
	}
	if w.Path() != path {
		t.Fatalf("path=%q", w.Path())
	}
	if n, err := Append(path, []byte("hello")); err != nil || n != 5 {
		t.Fatalf("Append=%d, %v", n, err)
	}
}
