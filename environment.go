package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
)

type Runner interface {
	Run(ctx context.Context, dir string, name string, args ...string) (string, error)
	LookPath(file string) (string, error)
}

type ExecRunner struct{}

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

type Environment struct {
	OS     string
	Runner Runner
}

func NewEnvironment(goos string) *Environment {
	return &Environment{
		OS:     goos,
		Runner: ExecRunner{},
	}
}

func executablePath() string {
	path, err := os.Executable()
	if err != nil {
		return "vigilante"
	}
	return path
}
