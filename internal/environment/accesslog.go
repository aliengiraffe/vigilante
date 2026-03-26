package environment

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

type AccessLogContext struct {
	ExecutionContext string `json:"context,omitempty"`
	Repo             string `json:"repo,omitempty"`
	IssueNumber      int    `json:"issue_number,omitempty"`
	Branch           string `json:"branch,omitempty"`
	WorktreePath     string `json:"worktree_path,omitempty"`
	CorrelationID    string `json:"correlation_id,omitempty"`
}

type AccessLogEntry struct {
	Timestamp        string   `json:"timestamp"`
	CompletedAt      string   `json:"completed_at"`
	ExecutionContext string   `json:"context"`
	Repo             string   `json:"repo,omitempty"`
	IssueNumber      int      `json:"issue_number,omitempty"`
	Branch           string   `json:"branch,omitempty"`
	WorktreePath     string   `json:"worktree_path,omitempty"`
	CorrelationID    string   `json:"correlation_id,omitempty"`
	Dir              string   `json:"dir,omitempty"`
	Tool             string   `json:"tool"`
	ToolPath         string   `json:"tool_path,omitempty"`
	Argv             []string `json:"argv"`
	ExitCode         int      `json:"exit_code"`
	DurationMs       int64    `json:"duration_ms"`
	Success          bool     `json:"success"`
	FailureKind      string   `json:"failure_kind,omitempty"`
	Error            string   `json:"error,omitempty"`
}

type accessLogContextKey struct{}

var shellWrappedToolPattern = regexp.MustCompile(`(?:^|\s)(?:command -v\s+)?'([^']+)'`)

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
	if strings.TrimSpace(meta.CorrelationID) != "" {
		current.CorrelationID = strings.TrimSpace(meta.CorrelationID)
	}
	return context.WithValue(ctx, accessLogContextKey{}, current)
}

func buildAccessLogEntry(ctx context.Context, dir string, name string, args []string, startedAt time.Time, endedAt time.Time, output string, err error) AccessLogEntry {
	if ctx == nil {
		ctx = context.Background()
	}
	meta, _ := ctx.Value(accessLogContextKey{}).(AccessLogContext)
	executionContext := strings.TrimSpace(meta.ExecutionContext)
	if executionContext == "" {
		executionContext = "daemon"
	}
	if executionContext == "daemon" && isHealthcheckCommand(name, args) {
		executionContext = "healthcheck"
	}
	toolPath := strings.TrimSpace(name)
	tool := normalizeAccessLogTool(name, args)
	failureKind, failureDetail := accessLogFailureDetails(output, err)
	return AccessLogEntry{
		Timestamp:        startedAt.UTC().Format(time.RFC3339Nano),
		CompletedAt:      endedAt.UTC().Format(time.RFC3339Nano),
		ExecutionContext: executionContext,
		Repo:             strings.TrimSpace(meta.Repo),
		IssueNumber:      meta.IssueNumber,
		Branch:           strings.TrimSpace(meta.Branch),
		WorktreePath:     strings.TrimSpace(meta.WorktreePath),
		CorrelationID:    strings.TrimSpace(meta.CorrelationID),
		Dir:              strings.TrimSpace(dir),
		Tool:             tool,
		ToolPath:         toolPath,
		Argv:             sanitizeAccessLogArgs(args),
		ExitCode:         exitCodeForError(err),
		DurationMs:       endedAt.Sub(startedAt).Milliseconds(),
		Success:          err == nil,
		FailureKind:      failureKind,
		Error:            failureDetail,
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

func sanitizeAccessLogText(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	for _, token := range []string{"ghp_", "github_pat_"} {
		if index := strings.Index(strings.ToLower(text), strings.ToLower(token)); index >= 0 {
			end := index + len(token)
			for end < len(text) {
				ch := text[end]
				if (ch < 'a' || ch > 'z') && (ch < 'A' || ch > 'Z') && (ch < '0' || ch > '9') && ch != '_' && ch != '-' {
					break
				}
				end++
			}
			text = text[:index] + "<redacted>" + text[end:]
		}
	}
	lower := strings.ToLower(text)
	for _, prefix := range []string{"authorization:", "bearer ", "token "} {
		searchStart := 0
		for searchStart < len(lower) {
			index := strings.Index(lower[searchStart:], prefix)
			if index < 0 {
				break
			}
			index += searchStart
			end := index + len(prefix)
			for end < len(text) && text[end] != '\n' && text[end] != '\r' && text[end] != '"' && text[end] != '\'' && text[end] != ' ' {
				end++
			}
			text = text[:index] + "<redacted>" + text[end:]
			lower = strings.ToLower(text)
			searchStart = index + len("<redacted>")
		}
	}
	return text
}

func accessLogFailureDetails(output string, err error) (string, string) {
	if err == nil {
		return "", ""
	}
	kind := "runtime_error"
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		kind = "timeout"
	case errors.Is(err, context.Canceled):
		kind = "canceled"
	default:
		var exitErr *exec.ExitError
		switch {
		case errors.As(err, &exitErr):
			kind = "exit_error"
		case strings.Contains(strings.ToLower(err.Error()), "executable file not found") || strings.Contains(strings.ToLower(err.Error()), "no such file or directory"):
			kind = "not_found"
		}
	}
	detail := sanitizeAccessLogText(strings.TrimSpace(output))
	if detail == "" {
		detail = sanitizeAccessLogText(strings.TrimSpace(err.Error()))
	}
	if len(detail) > 240 {
		detail = detail[:240] + "...(truncated)"
	}
	return kind, detail
}

func normalizeAccessLogTool(name string, args []string) string {
	base := strings.TrimSpace(filepath.Base(strings.TrimSpace(name)))
	if base == "." || base == string(filepath.Separator) {
		base = strings.TrimSpace(name)
	}
	if base == "sh" || base == "bash" || base == "zsh" {
		if wrapped := shellWrappedTool(args); wrapped != "" {
			return wrapped
		}
	}
	if base == "" {
		return strings.TrimSpace(name)
	}
	return base
}

func shellWrappedTool(args []string) string {
	if len(args) < 2 {
		return ""
	}
	if args[0] != "-lc" && args[0] != "-lic" {
		return ""
	}
	matches := shellWrappedToolPattern.FindStringSubmatch(args[1])
	if len(matches) < 2 {
		return ""
	}
	return strings.TrimSpace(filepath.Base(matches[1]))
}

func isHealthcheckCommand(name string, args []string) bool {
	tool := normalizeAccessLogTool(name, args)
	rawTool := strings.TrimSpace(filepath.Base(strings.TrimSpace(name)))
	if len(args) == 1 && args[0] == "--version" {
		return true
	}
	if len(args) >= 2 && tool == "launchctl" && args[0] == "print" {
		return true
	}
	if len(args) >= 3 && tool == "systemctl" && args[0] == "--user" && args[1] == "show" {
		return true
	}
	if len(args) >= 2 && tool == "gh" && args[0] == "auth" && args[1] == "status" {
		return true
	}
	if len(args) >= 3 && (rawTool == "sh" || rawTool == "bash" || rawTool == "zsh") && (args[0] == "-lc" || args[0] == "-lic") {
		command := strings.TrimSpace(args[1])
		return strings.Contains(command, "command -v ") || strings.Contains(command, " --version")
	}
	return false
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
