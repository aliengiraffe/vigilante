package app

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nicobistolfi/vigilante/internal/environment"
	"github.com/nicobistolfi/vigilante/internal/logging"
	"github.com/nicobistolfi/vigilante/internal/state"
)

// configureLogRotation applies the operator's configured rotation limits to
// every log write path in the process and records rotations and evictions in the
// daemon log, so operators can tell rotated logs apart from missing ones.
// Unreadable or malformed configuration falls back to the built-in defaults.
func configureLogRotation(store *state.Store) logging.Limits {
	config, err := store.LoadServiceConfig()
	if err != nil {
		config = state.ServiceConfig{}
	}
	limits := config.LogRotationLimits()
	logging.Configure(limits, store.LiveLogPaths, func(ev logging.Event) {
		switch ev.Kind {
		case logging.EventEvict:
			store.AppendDaemonLog("log budget enforced: evicted %d file(s), reclaimed %s from %s",
				len(ev.Removed), logging.FormatSize(ev.ReclaimedByte), ev.Path)
		default:
			store.AppendDaemonLog("rotated log file %s (max_file_size=%s)",
				ev.Path, logging.FormatSize(limits.MaxFileSize))
		}
	})
	return limits
}

// sweepLogsDir brings the logs directory back within the configured total
// budget. It is best-effort: a failed sweep is logged and otherwise ignored.
func (a *App) sweepLogsDir() {
	limits := logging.CurrentLimits()
	result, err := a.state.SweepLogs(limits)
	if err != nil {
		a.logger.Warn("log sweep failed", "err", err)
		return
	}
	if len(result.Removed) == 0 {
		return
	}
	a.logger.Info("log sweep evicted files",
		"files", len(result.Removed),
		"reclaimed_bytes", result.Reclaimed,
		"total_before_bytes", result.TotalBefore,
		"total_after_bytes", result.TotalAfter,
		"max_total_size", logging.FormatSize(limits.MaxTotalSize),
	)
}

func formatAccessLogEntry(e environment.AccessLogEntry) string {
	var b strings.Builder

	ts := e.Timestamp
	if t, err := time.Parse(time.RFC3339Nano, e.Timestamp); err == nil {
		ts = t.Local().Format("2006-01-02 15:04:05")
	}

	status := "✗"
	if e.Success {
		status = "✓"
	}

	cmd := e.Tool
	if len(e.Argv) > 0 {
		cmd += " " + strings.Join(e.Argv, " ")
	}

	dur := formatDuration(e.DurationMs)

	fmt.Fprintf(&b, "%s  %s  [%s]  %s  (%s)", ts, status, e.ExecutionContext, cmd, dur)

	var details []string
	if e.Repo != "" {
		detail := "repo: " + e.Repo
		if e.IssueNumber > 0 {
			detail += fmt.Sprintf(" #%d", e.IssueNumber)
		}
		details = append(details, detail)
	}
	if !e.Success {
		details = append(details, fmt.Sprintf("exit: %d", e.ExitCode))
	}

	if len(details) > 0 {
		b.WriteString("\n")
		padding := strings.Repeat(" ", len(ts)+2+len(status)+2)
		b.WriteString(padding)
		b.WriteString(strings.Join(details, "  "))
	}

	return b.String()
}

func formatDuration(ms int64) string {
	if ms < 1000 {
		return fmt.Sprintf("%dms", ms)
	}
	if ms < 60000 {
		return fmt.Sprintf("%.1fs", float64(ms)/1000)
	}
	return fmt.Sprintf("%.1fm", float64(ms)/60000)
}

func renderAccessLogLines(w io.Writer, data []byte) error {
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var entry environment.AccessLogEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			fmt.Fprintf(w, "[malformed] %s\n", line)
			continue
		}
		fmt.Fprintln(w, formatAccessLogEntry(entry))
	}
	return scanner.Err()
}

// waitForFile polls until the file at path exists or the context is canceled.
func (a *App) waitForFile(ctx context.Context, path string) error {
	fmt.Fprintf(a.stderr, "waiting for session log to appear: %s\n", path)
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if _, err := os.Stat(path); err == nil {
				return nil
			}
		}
	}
}

// watchSessionLog follows a plaintext session log file, printing new bytes as
// they arrive. It prints the full existing content first, then polls for new
// data until the context is canceled. If the file does not yet exist, it waits
// for the file to appear before tailing.
func (a *App) watchSessionLog(ctx context.Context, path string) error {
	if _, err := os.Stat(path); err != nil {
		if waitErr := a.waitForFile(ctx, path); waitErr != nil {
			return waitErr
		}
		if _, err := os.Stat(path); err != nil {
			return fmt.Errorf("no session log found for follow mode")
		}
	}
	return watchPlaintextLog(ctx, a.stdout, path, false)
}

func (a *App) watchAccessLog(ctx context.Context, path string) error {
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("no access log found")
	}
	return followFile(ctx, path, false, 500*time.Millisecond, func(r io.Reader) error {
		return renderAccessLogStream(a.stdout, r)
	})
}

func renderAccessLogStream(w io.Writer, r io.Reader) error {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var entry environment.AccessLogEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			fmt.Fprintf(w, "[malformed] %s\n", line)
			continue
		}
		fmt.Fprintln(w, formatAccessLogEntry(entry))
	}
	return scanner.Err()
}

func watchPlaintextLog(ctx context.Context, w io.Writer, path string, startAtEnd bool) error {
	return followFile(ctx, path, startAtEnd, 250*time.Millisecond, func(r io.Reader) error {
		_, err := io.Copy(w, r)
		return err
	})
}

// followFile polls path and hands every newly appended chunk to emit until the
// context is canceled. The file is reopened on each tick rather than held open,
// and the byte offset resets whenever the file shrinks, so a rename-based log
// rotation is survivable: the follow picks up the fresh current file instead of
// tailing the rotated-away inode. Expect a visible seam at the rotation.
func followFile(ctx context.Context, path string, startAtEnd bool, interval time.Duration, emit func(io.Reader) error) error {
	var offset int64
	if startAtEnd {
		if info, err := os.Stat(path); err == nil {
			offset = info.Size()
		}
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	readDelta := func() error {
		f, err := os.Open(path)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		defer f.Close()

		info, err := f.Stat()
		if err != nil {
			return err
		}
		if info.Size() < offset {
			offset = 0
		}
		if info.Size() == offset {
			return nil
		}
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			return err
		}
		if err := emit(io.LimitReader(f, info.Size()-offset)); err != nil {
			return err
		}
		offset = info.Size()
		return nil
	}

	if err := readDelta(); err != nil {
		return err
	}

	for {
		select {
		case <-ctx.Done():
			return readDelta()
		case <-ticker.C:
			if err := readDelta(); err != nil {
				return err
			}
		}
	}
}

func (a *App) streamSessionLog(ctx context.Context, repo string, issue int) error {
	path := a.state.SessionLogPath(repo, issue)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return watchPlaintextLog(ctx, a.stdout, path, true)
}
