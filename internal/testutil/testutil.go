package testutil

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
)

type FakeRunner struct {
	Outputs   map[string]string
	Errors    map[string]error
	LookPaths map[string]string
}

func (f FakeRunner) Run(_ context.Context, _ string, name string, args ...string) (string, error) {
	cmd := Key(name, args...)
	if err, ok := f.Errors[cmd]; ok {
		return "", err
	}
	if output, ok := f.Outputs[cmd]; ok {
		return output, nil
	}
	if len(args) == 1 && args[0] == "--version" {
		return name + " 1.0.0", nil
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
