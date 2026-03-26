package app

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
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
