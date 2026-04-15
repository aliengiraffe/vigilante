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
)

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
	f, err := os.Open(path)
	if err != nil {
		if waitErr := a.waitForFile(ctx, path); waitErr != nil {
			return waitErr
		}
		f, err = os.Open(path)
		if err != nil {
			return fmt.Errorf("no session log found for follow mode")
		}
	}
	defer f.Close()

	// Print existing content.
	if _, err := io.Copy(a.stdout, f); err != nil {
		return err
	}

	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()

	buf := make([]byte, 4096)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			for {
				n, err := f.Read(buf)
				if n > 0 {
					if _, wErr := a.stdout.Write(buf[:n]); wErr != nil {
						return wErr
					}
				}
				if err != nil {
					break
				}
			}
		}
	}
}

func (a *App) watchAccessLog(ctx context.Context, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("no access log found")
	}
	defer f.Close()

	// Print existing entries first.
	if err := renderAccessLogStream(a.stdout, f); err != nil {
		return err
	}

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := renderAccessLogStream(a.stdout, f); err != nil {
				return err
			}
		}
	}
}

func renderAccessLogStream(w io.Writer, f *os.File) error {
	scanner := bufio.NewScanner(f)
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
	var offset int64
	if startAtEnd {
		if info, err := os.Stat(path); err == nil {
			offset = info.Size()
		}
	}

	ticker := time.NewTicker(250 * time.Millisecond)
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
		if _, err := io.Copy(w, f); err != nil {
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
