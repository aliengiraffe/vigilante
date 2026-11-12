// Package environment is the boundary between vigilante and the host: process
// execution, executable lookup, platform detection, and the access log that
// records every external command a session ran.
//
// Everything that shells out goes through a Runner here rather than calling
// os/exec directly. That indirection is what makes command execution auditable
// and lets tests substitute a fake runner instead of touching the real system.
package environment

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"sort"
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

// EnvRunner extends Runner with a variant that runs a command with extra
// environment variables. It exists for coding-agent processes launched under an
// agent profile; ordinary git and gh calls keep using Run and RunStreaming and
// inherit the daemon environment untouched.
type EnvRunner interface {
	Runner
	// RunStreamingWithEnv runs the command with env applied on top of the
	// inherited environment (env wins on a duplicate key). Output is copied to
	// w as it is produced when w is non-nil.
	RunStreamingWithEnv(ctx context.Context, dir string, env map[string]string, w io.Writer, name string, args ...string) (string, error)
}

// RunWithEnv runs a command through runner with env applied on top of the
// inherited environment. With an empty env it is exactly Run or RunStreaming,
// so callers that never set env are unaffected. A runner that cannot apply env
// is an error rather than a silent fallback: dropping the overrides would
// launch the agent under the wrong configuration.
func RunWithEnv(ctx context.Context, runner Runner, dir string, env map[string]string, w io.Writer, name string, args ...string) (string, error) {
	if len(env) == 0 {
		if sr, ok := runner.(StreamingRunner); ok && w != nil {
			return sr.RunStreaming(ctx, dir, w, name, args...)
		}
		return runner.Run(ctx, dir, name, args...)
	}
	er, ok := runner.(EnvRunner)
	if !ok {
		return "", fmt.Errorf("%s: runner does not support environment overrides", name)
	}
	return er.RunStreamingWithEnv(ctx, dir, env, w, name, args...)
}

// MergeEnv returns base with overrides applied: an override replaces every
// inherited entry for the same key, and the remaining inherited entries keep
// their order. Overrides are appended sorted by key so the result is stable.
func MergeEnv(base []string, overrides map[string]string) []string {
	merged := make([]string, 0, len(base)+len(overrides))
	for _, entry := range base {
		key, _, _ := strings.Cut(entry, "=")
		if _, replaced := overrides[key]; replaced {
			continue
		}
		merged = append(merged, entry)
	}
	keys := make([]string, 0, len(overrides))
	for key := range overrides {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		merged = append(merged, key+"="+overrides[key])
	}
	return merged
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

// RunStreamingWithEnv executes a command with env applied on top of the
// inherited environment, copying output to w when it is non-nil. With an empty
// env the command inherits the daemon environment exactly as RunStreaming does.
func (ExecRunner) RunStreamingWithEnv(ctx context.Context, dir string, env map[string]string, w io.Writer, name string, args ...string) (string, error) {
	cmd := envCommand(ctx, dir, env, name, args...)
	var buf bytes.Buffer
	var out io.Writer = &buf
	if w != nil {
		out = io.MultiWriter(w, &buf)
	}
	cmd.Stdout = out
	cmd.Stderr = out
	err := cmd.Run()
	output := buf.String()
	if err != nil {
		return output, fmt.Errorf("%s %v: %w", name, args, err)
	}
	return output, nil
}

// envCommand builds the command for RunStreamingWithEnv. cmd.Env stays nil
// when there are no overrides so the child inherits the environment verbatim.
func envCommand(ctx context.Context, dir string, env map[string]string, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	if len(env) > 0 {
		cmd.Env = MergeEnv(os.Environ(), env)
	}
	return cmd
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

// RunStreamingWithEnv delegates to the base runner's RunStreamingWithEnv with
// the usual logging and telemetry. Only the names of the env variables are
// logged, never their values. A base runner that cannot apply env is an error.
func (r LoggingRunner) RunStreamingWithEnv(ctx context.Context, dir string, env map[string]string, w io.Writer, name string, args ...string) (string, error) {
	startedAt := time.Now().UTC()
	if r.Logger != nil {
		r.Logger.Info("command start", "dir", dir, "cmd", commandString(name, args...), "streaming", w != nil, "env_names", EnvNames(env))
	}
	var output string
	var err error
	if er, ok := r.Base.(EnvRunner); ok {
		output, err = er.RunStreamingWithEnv(ctx, dir, env, w, name, args...)
	} else {
		err = fmt.Errorf("%s: runner does not support environment overrides", name)
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

// EnvNames returns the sorted names of env, for logging overrides without
// exposing their values.
func EnvNames(env map[string]string) []string {
	names := make([]string, 0, len(env))
	for name := range env {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
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
