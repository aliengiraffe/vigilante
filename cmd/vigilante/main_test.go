package main

import (
	"context"
	"errors"
	"testing"
)

func TestRunIgnoresTelemetrySetupFailure(t *testing.T) {
	originalArgs := cliArgs
	originalRunCLI := runCLI
	originalSetupTelemetry := setupTelemetry
	t.Cleanup(func() {
		cliArgs = originalArgs
		runCLI = originalRunCLI
		setupTelemetry = originalSetupTelemetry
	})

	cliArgs = func() []string { return []string{"status"} }
	runCLI = func(_ context.Context, args []string) int {
		if len(args) != 1 || args[0] != "status" {
			t.Fatalf("runCLI args = %v, want [status]", args)
		}
		return 0
	}
	setupTelemetry = func(context.Context) (telemetrySession, error) {
		return nil, errors.New("collector unavailable")
	}

	if got := run(); got != 0 {
		t.Fatalf("run() = %d, want 0", got)
	}
}

func TestRunIgnoresTelemetryShutdownFailure(t *testing.T) {
	originalArgs := cliArgs
	originalRunCLI := runCLI
	originalSetupTelemetry := setupTelemetry
	t.Cleanup(func() {
		cliArgs = originalArgs
		runCLI = originalRunCLI
		setupTelemetry = originalSetupTelemetry
	})

	session := &stubTelemetrySession{shutdownErr: errors.New("flush timeout")}
	cliArgs = func() []string { return []string{"status"} }
	runCLI = func(ctx context.Context, args []string) int {
		if ctx != context.Background() {
			t.Fatalf("runCLI ctx changed unexpectedly")
		}
		if len(args) != 1 || args[0] != "status" {
			t.Fatalf("runCLI args = %v, want [status]", args)
		}
		return 0
	}
	setupTelemetry = func(context.Context) (telemetrySession, error) {
		return session, nil
	}

	if got := run(); got != 0 {
		t.Fatalf("run() = %d, want 0", got)
	}
	if !session.shutdownCalled {
		t.Fatal("expected telemetry shutdown to be attempted")
	}
	if session.finishedExitCode != 0 {
		t.Fatalf("finish exit code = %d, want 0", session.finishedExitCode)
	}
}

func TestRunPreservesCommandFailureWhenTelemetryFails(t *testing.T) {
	originalArgs := cliArgs
	originalRunCLI := runCLI
	originalSetupTelemetry := setupTelemetry
	t.Cleanup(func() {
		cliArgs = originalArgs
		runCLI = originalRunCLI
		setupTelemetry = originalSetupTelemetry
	})

	session := &stubTelemetrySession{shutdownErr: errors.New("flush timeout")}
	cliArgs = func() []string { return []string{"watch", "/tmp/repo"} }
	runCLI = func(_ context.Context, args []string) int {
		if len(args) != 2 || args[0] != "watch" || args[1] != "/tmp/repo" {
			t.Fatalf("runCLI args = %v, want [watch /tmp/repo]", args)
		}
		return 7
	}
	setupTelemetry = func(context.Context) (telemetrySession, error) {
		return session, nil
	}

	if got := run(); got != 7 {
		t.Fatalf("run() = %d, want 7", got)
	}
	if session.finishedExitCode != 7 {
		t.Fatalf("finish exit code = %d, want 7", session.finishedExitCode)
	}
}

type stubTelemetrySession struct {
	shutdownErr      error
	shutdownCalled   bool
	finishedExitCode int
}

func (s *stubTelemetrySession) StartCommand(ctx context.Context, _ []string) (context.Context, func(int)) {
	return ctx, func(exitCode int) {
		s.finishedExitCode = exitCode
	}
}

func (s *stubTelemetrySession) Shutdown(context.Context) error {
	s.shutdownCalled = true
	return s.shutdownErr
}
