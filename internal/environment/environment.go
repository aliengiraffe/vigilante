package environment

import (
	"bytes"
	"context"
	"fmt"
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
