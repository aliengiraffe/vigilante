package environment

import (
	"bytes"
	"context"
	"fmt"
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
	Logf             func(format string, args ...any)
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
	if r.Logf != nil {
		r.Logf("command start dir=%q cmd=%s", dir, commandString(name, args...))
	}
	output, err := r.Base.Run(ctx, dir, name, args...)
	endedAt := time.Now().UTC()
	exitCode := exitCodeForError(err)
	if r.CaptureCommand != nil {
		r.CaptureCommand(ctx, name, args, exitCode, endedAt.Sub(startedAt).Milliseconds())
	}
	if r.AccessLog != nil {
		r.AccessLog(buildAccessLogEntry(ctx, dir, name, args, startedAt, endedAt, err))
	}
	if r.Logf != nil {
		if err != nil {
			r.Logf("command failed cmd=%s err=%v output=%s", commandString(name, args...), err, trimForLog(output))
		} else {
			if r.LogSuccessOutput {
				r.Logf("command ok cmd=%s output=%s", commandString(name, args...), trimForLog(output))
			} else {
				r.Logf("command ok cmd=%s", commandString(name, args...))
			}
		}
	}
	return output, err
}

func (r LoggingRunner) LookPath(file string) (string, error) {
	path, err := r.Base.LookPath(file)
	if r.Logf != nil {
		if err != nil {
			r.Logf("lookpath failed binary=%s err=%v", file, err)
		} else {
			r.Logf("lookpath ok binary=%s path=%s", file, path)
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
