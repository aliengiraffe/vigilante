package environment

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"time"
)

type Runner interface {
	Run(ctx context.Context, dir string, name string, args ...string) (string, error)
	LookPath(file string) (string, error)
}

// StreamingRunner extends Runner with a variant that copies command output to
// an io.Writer in real-time while still returning the full output string.
type StreamingRunner interface {
	Runner
	RunStreaming(ctx context.Context, dir string, w io.Writer, name string, args ...string) (string, error)
}

type ExecRunner struct{}

type LoggingRunner struct {
	Base             Runner
	CaptureCommand   func(context.Context, string, []string, int, int64)
	AccessLog        func(AccessLogEntry)
	Logger           *slog.Logger
	LogSuccessOutput bool
}

func (ExecRunner) Run(ctx context.Context, dir string, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	output := stdout.String()
	if stderr.Len() > 0 {
		output += stderr.String()
	}
	if err != nil {
		return output, fmt.Errorf("%s %v: %w", name, args, err)
	}
	return output, nil
}

// RunWithStdin executes a command with the given bytes piped on stdin and
// returns the combined stdout/stderr output. Used by the sandbox proxy to
// forward agent stdin (e.g. `--body-file -`) through to the host gh CLI.
func (ExecRunner) RunWithStdin(ctx context.Context, stdin string, dir string, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	output := stdout.String()
	if stderr.Len() > 0 {
		output += stderr.String()
	}
	if err != nil {
		return output, fmt.Errorf("%s %v: %w", name, args, err)
	}
	return output, nil
}

// RunStreaming executes a command and copies stdout/stderr to w in real-time
// while also capturing the full output to return as a string.
func (ExecRunner) RunStreaming(ctx context.Context, dir string, w io.Writer, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	var buf bytes.Buffer
	multi := io.MultiWriter(w, &buf)
	cmd.Stdout = multi
	cmd.Stderr = multi
	err := cmd.Run()
	output := buf.String()
	if err != nil {
		return output, fmt.Errorf("%s %v: %w", name, args, err)
	}
	return output, nil
}

func (ExecRunner) LookPath(file string) (string, error) {
	return exec.LookPath(file)
}

func (r LoggingRunner) Run(ctx context.Context, dir string, name string, args ...string) (string, error) {
	startedAt := time.Now().UTC()
	if r.Logger != nil {
		r.Logger.Info("command start", "dir", dir, "cmd", commandString(name, args...))
	}
	output, err := r.Base.Run(ctx, dir, name, args...)
	endedAt := time.Now().UTC()
	exitCode := exitCodeForError(err)
	if r.CaptureCommand != nil {
		r.CaptureCommand(ctx, name, args, exitCode, endedAt.Sub(startedAt).Milliseconds())
	}
	if r.AccessLog != nil {
		r.AccessLog(buildAccessLogEntry(ctx, dir, name, args, startedAt, endedAt, output, err))
	}
	if r.Logger != nil {
		if err != nil {
			r.Logger.Error("command failed", "cmd", commandString(name, args...), "err", err, "output", trimForLog(output))
		} else {
			if r.LogSuccessOutput {
				r.Logger.Info("command ok", "cmd", commandString(name, args...), "output", trimForLog(output))
			} else {
				r.Logger.Info("command ok", "cmd", commandString(name, args...))
			}
		}
	}
	return output, err
}

// RunWithStdin delegates to the base runner's RunWithStdin if available,
// otherwise falls back to Run (dropping stdin). Logging and telemetry are
// applied as usual.
func (r LoggingRunner) RunWithStdin(ctx context.Context, stdin string, dir string, name string, args ...string) (string, error) {
	startedAt := time.Now().UTC()
	if r.Logger != nil {
		r.Logger.Info("command start", "dir", dir, "cmd", commandString(name, args...), "stdin_bytes", len(stdin))
	}
	var output string
	var err error
	type stdinCapable interface {
		RunWithStdin(context.Context, string, string, string, ...string) (string, error)
	}
	if sr, ok := r.Base.(stdinCapable); ok {
		output, err = sr.RunWithStdin(ctx, stdin, dir, name, args...)
	} else {
		output, err = r.Base.Run(ctx, dir, name, args...)
	}
	endedAt := time.Now().UTC()
	exitCode := exitCodeForError(err)
	if r.CaptureCommand != nil {
		r.CaptureCommand(ctx, name, args, exitCode, endedAt.Sub(startedAt).Milliseconds())
	}
	if r.AccessLog != nil {
		r.AccessLog(buildAccessLogEntry(ctx, dir, name, args, startedAt, endedAt, output, err))
	}
	if r.Logger != nil {
		if err != nil {
			r.Logger.Error("command failed", "cmd", commandString(name, args...), "err", err, "output", trimForLog(output))
		} else {
			if r.LogSuccessOutput {
				r.Logger.Info("command ok", "cmd", commandString(name, args...), "output", trimForLog(output))
			} else {
				r.Logger.Info("command ok", "cmd", commandString(name, args...))
			}
		}
	}
	return output, err
}

// RunStreaming delegates to the base runner's RunStreaming if available,
// otherwise falls back to Run. Logging and telemetry are applied as usual.
func (r LoggingRunner) RunStreaming(ctx context.Context, dir string, w io.Writer, name string, args ...string) (string, error) {
	startedAt := time.Now().UTC()
	if r.Logger != nil {
		r.Logger.Info("command start", "dir", dir, "cmd", commandString(name, args...), "streaming", true)
	}
	var output string
	var err error
	if sr, ok := r.Base.(StreamingRunner); ok {
		output, err = sr.RunStreaming(ctx, dir, w, name, args...)
	} else {
		output, err = r.Base.Run(ctx, dir, name, args...)
		if w != nil && output != "" {
			_, _ = io.WriteString(w, output)
		}
	}
	endedAt := time.Now().UTC()
	exitCode := exitCodeForError(err)
	if r.CaptureCommand != nil {
		r.CaptureCommand(ctx, name, args, exitCode, endedAt.Sub(startedAt).Milliseconds())
	}
	if r.AccessLog != nil {
		r.AccessLog(buildAccessLogEntry(ctx, dir, name, args, startedAt, endedAt, output, err))
	}
	if r.Logger != nil {
		if err != nil {
			r.Logger.Error("command failed", "cmd", commandString(name, args...), "err", err, "output", trimForLog(output))
		} else {
			if r.LogSuccessOutput {
				r.Logger.Info("command ok", "cmd", commandString(name, args...), "output", trimForLog(output))
			} else {
				r.Logger.Info("command ok", "cmd", commandString(name, args...))
			}
		}
	}
	return output, err
}

func (r LoggingRunner) LookPath(file string) (string, error) {
	path, err := r.Base.LookPath(file)
	if r.Logger != nil {
		if err != nil {
			r.Logger.Error("lookpath failed", "binary", file, "err", err)
		} else {
			r.Logger.Info("lookpath ok", "binary", file, "path", path)
		}
	}
	return path, err
}

type Environment struct {
	OS     string
	Runner Runner
}

func New(goos string) *Environment {
	return &Environment{
		OS:     goos,
		Runner: ExecRunner{},
	}
}

func ExecutablePath() string {
	path, err := os.Executable()
	if err != nil {
		return "vigilante"
	}
	return path
}

func trimForLog(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return "<empty>"
	}
	const limit = 1000
	if len(text) <= limit {
		return text
	}
	return text[:limit] + "...(truncated)"
}

func commandString(name string, args ...string) string {
	return strings.TrimSpace(name + " " + strings.Join(args, " "))
}
