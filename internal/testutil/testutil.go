package testutil

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
)

// Ensure FakeRunner implements StreamingRunner at compile time.
var _ interface {
	RunStreaming(context.Context, string, io.Writer, string, ...string) (string, error)
} = FakeRunner{}

type FakeRunner struct {
	Outputs      map[string]string
	ErrorOutputs map[string]string
	Errors       map[string]error
	LookPaths    map[string]string
}

func (f FakeRunner) Run(_ context.Context, _ string, name string, args ...string) (string, error) {
	cmd := Key(name, args...)
	if err, ok := f.Errors[cmd]; ok {
		return f.ErrorOutputs[cmd], err
	}
	if output, ok := f.Outputs[cmd]; ok {
		return output, nil
	}
	if name == "git" && len(args) == 5 && args[0] == "ls-remote" && args[1] == "--exit-code" && args[2] == "--heads" && args[3] == "origin" {
		return "", errors.New("exit status 2")
	}
	if len(args) == 1 && args[0] == "--version" {
		return name + " 1.0.0", nil
	}
	return "", fmt.Errorf("unexpected command: %s", cmd)
}

// RunStreaming writes output to w while also returning it, mirroring the real
// streaming behavior for test assertions.
func (f FakeRunner) RunStreaming(_ context.Context, _ string, w io.Writer, name string, args ...string) (string, error) {
	cmd := Key(name, args...)
	if err, ok := f.Errors[cmd]; ok {
		out := f.ErrorOutputs[cmd]
		if w != nil && out != "" {
			_, _ = io.WriteString(w, out)
		}
		return out, err
	}
	if output, ok := f.Outputs[cmd]; ok {
		if w != nil && output != "" {
			_, _ = io.WriteString(w, output)
		}
		return output, nil
	}
	if name == "git" && len(args) == 5 && args[0] == "ls-remote" && args[1] == "--exit-code" && args[2] == "--heads" && args[3] == "origin" {
		return "", errors.New("exit status 2")
	}
	if len(args) == 1 && args[0] == "--version" {
		out := name + " 1.0.0"
		if w != nil {
			_, _ = io.WriteString(w, out)
		}
		return out, nil
	}
	return "", fmt.Errorf("unexpected command: %s", cmd)
}

func (f FakeRunner) LookPath(file string) (string, error) {
	if path, ok := f.LookPaths[file]; ok {
		return path, nil
	}
	return "", errors.New("not found")
}

func Key(name string, args ...string) string {
	return strings.TrimSpace(name + " " + strings.Join(args, " "))
}

type IODiscard struct{}

func (IODiscard) Write(p []byte) (int, error) {
	return io.Discard.Write(p)
}
