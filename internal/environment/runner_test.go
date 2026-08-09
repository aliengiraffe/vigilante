package environment

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// ExecRunner is the one place vigilante actually spawns processes, so it is
// exercised against real commands. Only harmless, universally available ones:
// nothing here touches the network, the repository, or anything outside t.TempDir().
func TestExecRunnerRun(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == "windows" {
		t.Skip("the shell commands used here are POSIX-only")
	}

	t.Run("captures stdout", func(t *testing.T) {
		t.Parallel()

		output, err := (ExecRunner{}).Run(context.Background(), "", "echo", "hello")
		if err != nil {
			t.Fatal(err)
		}
		if strings.TrimSpace(output) != "hello" {
			t.Fatalf("output = %q, want hello", output)
		}
	})

	// stderr is appended to stdout rather than discarded: for a failing git or gh
	// command the diagnostic is on stderr, and losing it leaves an operator with
	// only an exit status.
	t.Run("appends stderr to stdout", func(t *testing.T) {
		t.Parallel()

		output, err := (ExecRunner{}).Run(context.Background(), "", "sh", "-c", "echo out; echo err 1>&2")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(output, "out") || !strings.Contains(output, "err") {
			t.Fatalf("output = %q, want both streams", output)
		}
	})

	t.Run("runs in the requested directory", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		output, err := (ExecRunner{}).Run(context.Background(), dir, "pwd")
		if err != nil {
			t.Fatal(err)
		}
		// macOS reports /private/var for /var, so compare resolved paths.
		want, err := filepath.EvalSymlinks(dir)
		if err != nil {
			t.Fatal(err)
		}
		got, err := filepath.EvalSymlinks(strings.TrimSpace(output))
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("pwd = %q, want %q", got, want)
		}
	})

	// A non-zero exit must return the output *and* an error naming the command.
	// Callers rely on the output for diagnostics even on failure.
	t.Run("failure returns output and a command-qualified error", func(t *testing.T) {
		t.Parallel()

		output, err := (ExecRunner{}).Run(context.Background(), "", "sh", "-c", "echo partial; exit 3")
		if err == nil {
			t.Fatal("expected an error for a non-zero exit")
		}
		if !strings.Contains(output, "partial") {
			t.Fatalf("output = %q, want the partial output preserved", output)
		}
		if !strings.Contains(err.Error(), "sh") {
			t.Fatalf("error should name the command, got %v", err)
		}
	})

	t.Run("missing binary is an error", func(t *testing.T) {
		t.Parallel()

		if _, err := (ExecRunner{}).Run(context.Background(), "", "vigilante-no-such-binary-xyz"); err == nil {
			t.Fatal("expected an error for a missing binary")
		}
	})

	t.Run("cancelled context stops the command", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := (ExecRunner{}).Run(ctx, "", "echo", "hi"); err == nil {
			t.Fatal("expected an error for a cancelled context")
		}
	})
}

func TestExecRunnerRunWithStdin(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == "windows" {
		t.Skip("the shell commands used here are POSIX-only")
	}

	// This is the path the sandbox proxy uses to forward `--body-file -` through
	// to the host gh, so stdin actually reaching the child is the whole point.
	t.Run("pipes stdin to the command", func(t *testing.T) {
		t.Parallel()

		output, err := (ExecRunner{}).RunWithStdin(context.Background(), "piped payload", "", "cat")
		if err != nil {
			t.Fatal(err)
		}
		if strings.TrimSpace(output) != "piped payload" {
			t.Fatalf("output = %q, want the piped payload", output)
		}
	})

	t.Run("empty stdin leaves stdin unset", func(t *testing.T) {
		t.Parallel()

		output, err := (ExecRunner{}).RunWithStdin(context.Background(), "", "", "echo", "no-stdin")
		if err != nil {
			t.Fatal(err)
		}
		if strings.TrimSpace(output) != "no-stdin" {
			t.Fatalf("output = %q", output)
		}
	})

	t.Run("runs in the requested directory", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "marker"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		output, err := (ExecRunner{}).RunWithStdin(context.Background(), "", dir, "ls")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(output, "marker") {
			t.Fatalf("output = %q, want the directory listing", output)
		}
	})

	t.Run("failure returns output and an error", func(t *testing.T) {
		t.Parallel()

		output, err := (ExecRunner{}).RunWithStdin(context.Background(), "", "", "sh", "-c", "echo oops 1>&2; exit 1")
		if err == nil {
			t.Fatal("expected an error")
		}
		if !strings.Contains(output, "oops") {
			t.Fatalf("output = %q, want stderr preserved", output)
		}
	})
}

func TestExecRunnerRunStreaming(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == "windows" {
		t.Skip("the shell commands used here are POSIX-only")
	}

	// Streaming has to do both jobs: write through to the caller's writer as
	// output arrives, and still return the complete output.
	t.Run("writes to the writer and returns the output", func(t *testing.T) {
		t.Parallel()

		var buf bytes.Buffer
		output, err := (ExecRunner{}).RunStreaming(context.Background(), "", &buf, "sh", "-c", "echo streamed; echo alsoerr 1>&2")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(output, "streamed") || !strings.Contains(output, "alsoerr") {
			t.Fatalf("returned output = %q, want both streams", output)
		}
		if !strings.Contains(buf.String(), "streamed") {
			t.Fatalf("writer got %q, want the streamed output", buf.String())
		}
	})

	t.Run("failure returns output and an error", func(t *testing.T) {
		t.Parallel()

		var buf bytes.Buffer
		output, err := (ExecRunner{}).RunStreaming(context.Background(), "", &buf, "sh", "-c", "echo before; exit 2")
		if err == nil {
			t.Fatal("expected an error")
		}
		if !strings.Contains(output, "before") {
			t.Fatalf("output = %q, want the partial output", output)
		}
	})

	t.Run("runs in the requested directory", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		var buf bytes.Buffer
		if _, err := (ExecRunner{}).RunStreaming(context.Background(), dir, &buf, "pwd"); err != nil {
			t.Fatal(err)
		}
		want, err := filepath.EvalSymlinks(dir)
		if err != nil {
			t.Fatal(err)
		}
		got, err := filepath.EvalSymlinks(strings.TrimSpace(buf.String()))
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("pwd = %q, want %q", got, want)
		}
	})
}

func TestExecRunnerLookPath(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == "windows" {
		t.Skip("relies on a POSIX binary being on PATH")
	}

	path, err := (ExecRunner{}).LookPath("sh")
	if err != nil {
		t.Fatalf("sh should be resolvable: %v", err)
	}
	if path == "" {
		t.Fatal("expected a non-empty path")
	}

	if _, err := (ExecRunner{}).LookPath("vigilante-no-such-binary-xyz"); err == nil {
		t.Fatal("expected an error for a missing binary")
	}
}

// recordingRunner is a Runner with no streaming or stdin support, used to prove
// LoggingRunner's fallbacks.
type recordingRunner struct {
	output   string
	err      error
	gotDir   string
	gotName  string
	gotArgs  []string
	runCalls int
}

func (r *recordingRunner) Run(_ context.Context, dir string, name string, args ...string) (string, error) {
	r.runCalls++
	r.gotDir, r.gotName, r.gotArgs = dir, name, args
	return r.output, r.err
}

func (r *recordingRunner) LookPath(file string) (string, error) {
	if file == "missing" {
		return "", errors.New("not found")
	}
	return "/bin/" + file, nil
}

// stdinStreamingRunner supports both optional interfaces so the delegating
// branches are exercised too.
type stdinStreamingRunner struct {
	recordingRunner
	gotStdin      string
	stdinCalls    int
	streamCalls   int
	streamedBytes string
}

func (r *stdinStreamingRunner) RunWithStdin(_ context.Context, stdin string, dir string, name string, args ...string) (string, error) {
	r.stdinCalls++
	r.gotStdin, r.gotDir, r.gotName, r.gotArgs = stdin, dir, name, args
	return r.output, r.err
}

func (r *stdinStreamingRunner) RunStreaming(_ context.Context, dir string, w io.Writer, name string, args ...string) (string, error) {
	r.streamCalls++
	r.gotDir, r.gotName, r.gotArgs = dir, name, args
	if w != nil {
		_, _ = io.WriteString(w, r.streamedBytes)
	}
	return r.output, r.err
}

func TestLoggingRunnerRunInvokesHooks(t *testing.T) {
	t.Parallel()

	base := &recordingRunner{output: "base output"}
	var capturedName string
	var capturedExit int
	var entry AccessLogEntry
	captured := false

	r := LoggingRunner{
		Base: base,
		CaptureCommand: func(_ context.Context, name string, _ []string, exitCode int, _ int64) {
			capturedName, capturedExit = name, exitCode
		},
		AccessLog: func(e AccessLogEntry) {
			entry = e
			captured = true
		},
	}

	output, err := r.Run(context.Background(), "/dir", "git", "status")
	if err != nil {
		t.Fatal(err)
	}
	if output != "base output" {
		t.Fatalf("output = %q, want the base runner's output", output)
	}
	if base.gotDir != "/dir" || base.gotName != "git" {
		t.Fatalf("base got dir=%q name=%q", base.gotDir, base.gotName)
	}
	if capturedName != "git" || capturedExit != 0 {
		t.Fatalf("telemetry got name=%q exit=%d, want git/0", capturedName, capturedExit)
	}
	if !captured || entry.Tool == "" {
		t.Fatalf("access log entry not populated: %#v", entry)
	}
}

// A failing command must still reach both hooks with a non-zero exit code, or
// failures become invisible in telemetry and the access log.
func TestLoggingRunnerRunRecordsFailures(t *testing.T) {
	t.Parallel()

	base := &recordingRunner{output: "boom output", err: errors.New("exit status 1")}
	var exitCode int
	var loggedFailure bool

	r := LoggingRunner{
		Base:           base,
		CaptureCommand: func(_ context.Context, _ string, _ []string, code int, _ int64) { exitCode = code },
		AccessLog:      func(e AccessLogEntry) { loggedFailure = e.ExitCode != 0 },
	}

	output, err := r.Run(context.Background(), "", "git", "push")
	if err == nil {
		t.Fatal("expected the base error to propagate")
	}
	if output != "boom output" {
		t.Fatalf("output = %q, want the base output preserved on failure", output)
	}
	if exitCode == 0 {
		t.Fatal("telemetry should record a non-zero exit code")
	}
	if !loggedFailure {
		t.Fatal("access log should record a non-zero exit code")
	}
}

func TestLoggingRunnerRunWithStdin(t *testing.T) {
	t.Parallel()

	t.Run("delegates to a stdin-capable base", func(t *testing.T) {
		t.Parallel()

		base := &stdinStreamingRunner{recordingRunner: recordingRunner{output: "ok"}}
		r := LoggingRunner{Base: base}

		if _, err := r.RunWithStdin(context.Background(), "payload", "/dir", "gh", "api"); err != nil {
			t.Fatal(err)
		}
		if base.stdinCalls != 1 {
			t.Fatalf("stdin calls = %d, want 1", base.stdinCalls)
		}
		if base.gotStdin != "payload" {
			t.Fatalf("stdin = %q, want payload", base.gotStdin)
		}
		if base.runCalls != 0 {
			t.Fatal("must not fall back to Run when the base supports stdin")
		}
	})

	// Falling back silently drops stdin. That is the documented behavior, and the
	// test pins it so the fallback is a deliberate choice rather than a surprise.
	t.Run("falls back to Run when the base has no stdin support", func(t *testing.T) {
		t.Parallel()

		base := &recordingRunner{output: "ok"}
		r := LoggingRunner{Base: base}

		if _, err := r.RunWithStdin(context.Background(), "dropped", "", "gh", "api"); err != nil {
			t.Fatal(err)
		}
		if base.runCalls != 1 {
			t.Fatalf("run calls = %d, want 1 fallback call", base.runCalls)
		}
	})

	t.Run("records failures through the hooks", func(t *testing.T) {
		t.Parallel()

		base := &recordingRunner{err: errors.New("exit status 1")}
		var exitCode int
		var sawEntry bool
		r := LoggingRunner{
			Base:           base,
			CaptureCommand: func(_ context.Context, _ string, _ []string, code int, _ int64) { exitCode = code },
			AccessLog:      func(AccessLogEntry) { sawEntry = true },
		}

		if _, err := r.RunWithStdin(context.Background(), "x", "", "gh", "api"); err == nil {
			t.Fatal("expected an error")
		}
		if exitCode == 0 || !sawEntry {
			t.Fatalf("hooks not invoked correctly: exit=%d entry=%v", exitCode, sawEntry)
		}
	})
}

func TestLoggingRunnerRunStreaming(t *testing.T) {
	t.Parallel()

	t.Run("delegates to a streaming base", func(t *testing.T) {
		t.Parallel()

		base := &stdinStreamingRunner{
			recordingRunner: recordingRunner{output: "full"},
			streamedBytes:   "streamed",
		}
		r := LoggingRunner{Base: base}
		var buf bytes.Buffer

		output, err := r.RunStreaming(context.Background(), "/dir", &buf, "go", "test")
		if err != nil {
			t.Fatal(err)
		}
		if base.streamCalls != 1 || base.runCalls != 0 {
			t.Fatalf("stream calls = %d, run calls = %d", base.streamCalls, base.runCalls)
		}
		if output != "full" {
			t.Fatalf("output = %q", output)
		}
		if buf.String() != "streamed" {
			t.Fatalf("writer got %q", buf.String())
		}
	})

	// Without streaming support the output still has to reach the writer, just
	// all at once at the end rather than incrementally.
	t.Run("non-streaming base writes the output to the writer afterwards", func(t *testing.T) {
		t.Parallel()

		base := &recordingRunner{output: "buffered output"}
		r := LoggingRunner{Base: base}
		var buf bytes.Buffer

		output, err := r.RunStreaming(context.Background(), "", &buf, "go", "build")
		if err != nil {
			t.Fatal(err)
		}
		if output != "buffered output" {
			t.Fatalf("output = %q", output)
		}
		if buf.String() != "buffered output" {
			t.Fatalf("writer got %q, want the full output", buf.String())
		}
	})

	t.Run("nil writer with a non-streaming base does not panic", func(t *testing.T) {
		t.Parallel()

		base := &recordingRunner{output: "something"}
		r := LoggingRunner{Base: base}

		if _, err := r.RunStreaming(context.Background(), "", nil, "go", "vet"); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("empty output with a non-streaming base skips the write", func(t *testing.T) {
		t.Parallel()

		base := &recordingRunner{output: ""}
		r := LoggingRunner{Base: base}
		var buf bytes.Buffer

		if _, err := r.RunStreaming(context.Background(), "", &buf, "true"); err != nil {
			t.Fatal(err)
		}
		if buf.Len() != 0 {
			t.Fatalf("writer got %q, want nothing", buf.String())
		}
	})

	t.Run("records failures through the hooks", func(t *testing.T) {
		t.Parallel()

		base := &recordingRunner{err: errors.New("exit status 1")}
		var exitCode int
		r := LoggingRunner{
			Base:           base,
			CaptureCommand: func(_ context.Context, _ string, _ []string, code int, _ int64) { exitCode = code },
			AccessLog:      func(AccessLogEntry) {},
		}

		if _, err := r.RunStreaming(context.Background(), "", nil, "go", "test"); err == nil {
			t.Fatal("expected an error")
		}
		if exitCode == 0 {
			t.Fatal("expected a non-zero exit code in telemetry")
		}
	})
}

func TestLoggingRunnerLookPath(t *testing.T) {
	t.Parallel()

	r := LoggingRunner{Base: &recordingRunner{}}

	path, err := r.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	if path != "/bin/git" {
		t.Fatalf("path = %q", path)
	}

	if _, err := r.LookPath("missing"); err == nil {
		t.Fatal("expected an error to propagate")
	}
}

// Every hook is optional. A LoggingRunner with only a Base must work, or callers
// would have to supply no-op hooks everywhere.
func TestLoggingRunnerWorksWithoutHooks(t *testing.T) {
	t.Parallel()

	r := LoggingRunner{Base: &recordingRunner{output: "x"}}

	if _, err := r.Run(context.Background(), "", "echo"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.RunWithStdin(context.Background(), "", "", "echo"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.RunStreaming(context.Background(), "", nil, "echo"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.LookPath("echo"); err != nil {
		t.Fatal(err)
	}
}

func TestNew(t *testing.T) {
	t.Parallel()

	env := New("linux")
	if env.OS != "linux" {
		t.Fatalf("OS = %q, want linux", env.OS)
	}
	if _, ok := env.Runner.(ExecRunner); !ok {
		t.Fatalf("Runner = %T, want ExecRunner", env.Runner)
	}
}

func TestExecutablePath(t *testing.T) {
	t.Parallel()

	// Under `go test` this resolves to the test binary; the contract is only that
	// it is non-empty so callers can build a re-exec command line.
	if got := ExecutablePath(); got == "" {
		t.Fatal("ExecutablePath must never return an empty string")
	}
}

func TestTrimForLog(t *testing.T) {
	t.Parallel()

	if got := trimForLog("   "); got != "<empty>" {
		t.Fatalf("blank input = %q, want <empty>", got)
	}
	if got := trimForLog("  hello  "); got != "hello" {
		t.Fatalf("got %q, want trimmed", got)
	}

	exact := strings.Repeat("a", 1000)
	if got := trimForLog(exact); got != exact {
		t.Fatalf("1000 characters should pass through unchanged, got %d chars", len(got))
	}

	long := strings.Repeat("b", 1001)
	got := trimForLog(long)
	if !strings.HasSuffix(got, "...(truncated)") {
		t.Fatalf("long input should be marked truncated, got %q", got[len(got)-20:])
	}
	if len(got) != 1000+len("...(truncated)") {
		t.Fatalf("truncated length = %d", len(got))
	}
}

func TestCommandString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "git", args: []string{"status", "--short"}, want: "git status --short"},
		{name: "gh", args: nil, want: "gh"},
		{name: "gh", args: []string{}, want: "gh"},
	}
	for _, tt := range tests {
		if got := commandString(tt.name, tt.args...); got != tt.want {
			t.Errorf("commandString(%q, %v) = %q, want %q", tt.name, tt.args, got, tt.want)
		}
	}
}
