package environment

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"time"
)

type AccessLogContext struct {
	ExecutionContext string `json:"context,omitempty"`
	Repo             string `json:"repo,omitempty"`
	IssueNumber      int    `json:"issue_number,omitempty"`
	Branch           string `json:"branch,omitempty"`
	WorktreePath     string `json:"worktree_path,omitempty"`
}

type AccessLogEntry struct {
	Timestamp        string   `json:"timestamp"`
	CompletedAt      string   `json:"completed_at"`
	ExecutionContext string   `json:"context"`
	Repo             string   `json:"repo,omitempty"`
	IssueNumber      int      `json:"issue_number,omitempty"`
	Branch           string   `json:"branch,omitempty"`
	WorktreePath     string   `json:"worktree_path,omitempty"`
	Dir              string   `json:"dir,omitempty"`
	Tool             string   `json:"tool"`
	Argv             []string `json:"argv"`
	ExitCode         int      `json:"exit_code"`
	DurationMs       int64    `json:"duration_ms"`
	Success          bool     `json:"success"`
}

type accessLogContextKey struct{}

func WithAccessLogContext(ctx context.Context, meta AccessLogContext) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	current, _ := ctx.Value(accessLogContextKey{}).(AccessLogContext)
	if strings.TrimSpace(meta.ExecutionContext) != "" {
		current.ExecutionContext = strings.TrimSpace(meta.ExecutionContext)
	}
	if strings.TrimSpace(meta.Repo) != "" {
		current.Repo = strings.TrimSpace(meta.Repo)
	}
	if meta.IssueNumber > 0 {
		current.IssueNumber = meta.IssueNumber
	}
	if strings.TrimSpace(meta.Branch) != "" {
		current.Branch = strings.TrimSpace(meta.Branch)
	}
	if strings.TrimSpace(meta.WorktreePath) != "" {
		current.WorktreePath = strings.TrimSpace(meta.WorktreePath)
	}
	return context.WithValue(ctx, accessLogContextKey{}, current)
}

func buildAccessLogEntry(ctx context.Context, dir string, name string, args []string, startedAt time.Time, endedAt time.Time, err error) AccessLogEntry {
	if ctx == nil {
		ctx = context.Background()
	}
	meta, _ := ctx.Value(accessLogContextKey{}).(AccessLogContext)
	executionContext := strings.TrimSpace(meta.ExecutionContext)
	if executionContext == "" {
		executionContext = "daemon"
	}
	return AccessLogEntry{
		Timestamp:        startedAt.UTC().Format(time.RFC3339Nano),
		CompletedAt:      endedAt.UTC().Format(time.RFC3339Nano),
		ExecutionContext: executionContext,
		Repo:             strings.TrimSpace(meta.Repo),
		IssueNumber:      meta.IssueNumber,
		Branch:           strings.TrimSpace(meta.Branch),
		WorktreePath:     strings.TrimSpace(meta.WorktreePath),
		Dir:              strings.TrimSpace(dir),
		Tool:             strings.TrimSpace(name),
		Argv:             sanitizeAccessLogArgs(args),
		ExitCode:         exitCodeForError(err),
		DurationMs:       endedAt.Sub(startedAt).Milliseconds(),
		Success:          err == nil,
	}
}

func exitCodeForError(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	if err == context.DeadlineExceeded {
		return 124
	}
	if err == context.Canceled {
		return 130
	}
	return 1
}

func sanitizeAccessLogArgs(args []string) []string {
	if len(args) == 0 {
		return nil
	}
	sanitized := make([]string, 0, len(args))
	redactNext := false
	for _, arg := range args {
		trimmed := strings.TrimSpace(arg)
		switch {
		case redactNext:
			sanitized = append(sanitized, "<redacted>")
			redactNext = false
		case isHeaderFlag(trimmed):
			sanitized = append(sanitized, trimmed)
			redactNext = true
		case hasSensitiveAssignment(trimmed):
			key, _, _ := strings.Cut(trimmed, "=")
			sanitized = append(sanitized, key+"=<redacted>")
		case hasSensitiveFlag(trimmed):
			sanitized = append(sanitized, trimmed)
			redactNext = true
		case looksSensitiveValue(trimmed):
			sanitized = append(sanitized, "<redacted>")
		default:
			sanitized = append(sanitized, trimmed)
		}
	}
	return sanitized
}

func isHeaderFlag(arg string) bool {
	return arg == "-H" || arg == "--header"
}

func hasSensitiveAssignment(arg string) bool {
	key, value, ok := strings.Cut(arg, "=")
	if !ok {
		return false
	}
	return value != "" && looksSensitiveFlagName(key)
}

func hasSensitiveFlag(arg string) bool {
	if !strings.HasPrefix(arg, "-") {
		return false
	}
	return looksSensitiveFlagName(arg)
}

func looksSensitiveFlagName(arg string) bool {
	lower := strings.ToLower(strings.TrimLeft(strings.TrimSpace(arg), "-"))
	for _, token := range []string{"token", "secret", "password", "authorization", "auth", "cookie"} {
		if strings.Contains(lower, token) {
			return true
		}
	}
	return false
}

func looksSensitiveValue(arg string) bool {
	lower := strings.ToLower(strings.TrimSpace(arg))
	for _, token := range []string{"authorization:", "bearer ", "token ", "ghp_", "github_pat_"} {
		if strings.Contains(lower, token) {
			return true
		}
	}
	return false
}
