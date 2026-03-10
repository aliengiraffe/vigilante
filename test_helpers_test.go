package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
)

type fakeRunner struct {
	outputs  map[string]string
	errors   map[string]error
	lookPath map[string]string
}

func (f fakeRunner) Run(_ context.Context, _ string, name string, args ...string) (string, error) {
	cmd := key(name, args...)
	if err, ok := f.errors[cmd]; ok {
		return "", err
	}
	if output, ok := f.outputs[cmd]; ok {
		return output, nil
	}
	return "", fmt.Errorf("unexpected command: %s", cmd)
}

func (f fakeRunner) LookPath(file string) (string, error) {
	if path, ok := f.lookPath[file]; ok {
		return path, nil
	}
	return "", errors.New("not found")
}

func key(name string, args ...string) string {
	return strings.TrimSpace(name + " " + strings.Join(args, " "))
}

type errString string

func (e errString) Error() string { return string(e) }

type ioDiscard struct{}

func (ioDiscard) Write(p []byte) (int, error) {
	return io.Discard.Write(p)
}
