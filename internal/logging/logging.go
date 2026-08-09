// Package logging provides the daemon's structured logger and the size-bounded
// rotation that keeps long-running daemon logs from filling the disk.
//
// Rotation is enforced here rather than delegated to an external logrotate so a
// vigilante daemon has the same log-retention behavior on every platform it
// supports.
package logging

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

// NewDaemonLogger creates a *slog.Logger that writes human-readable text
// to the daemon log file at path. Writes go through the shared rotation-aware
// Writer for that path, so the logger keeps writing to the current file across
// rotations instead of holding a handle on a renamed inode. Timestamps use the
// local timezone in RFC3339 format to match the previous operator-facing log
// format.
func NewDaemonLogger(path string) (*slog.Logger, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	w := OpenWriter(path)
	handler := slog.NewTextHandler(w, &slog.HandlerOptions{
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			if a.Key == slog.TimeKey {
				a.Value = slog.StringValue(a.Value.Time().In(time.Local).Format(time.RFC3339))
			}
			return a
		},
	})
	return slog.New(handler), nil
}

// Discard returns a *slog.Logger that silently discards all log records.
func Discard() *slog.Logger {
	return slog.New(discardHandler{})
}

type discardHandler struct{}

func (discardHandler) Enabled(_ context.Context, _ slog.Level) bool  { return false }
func (discardHandler) Handle(_ context.Context, _ slog.Record) error { return nil }
func (discardHandler) WithAttrs(attrs []slog.Attr) slog.Handler      { return discardHandler{} }
func (discardHandler) WithGroup(name string) slog.Handler            { return discardHandler{} }
