package environment

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestMergeEnvOverrideReplacesInheritedKeyAndKeepsTheRest(t *testing.T) {
	base := []string{"PATH=/usr/bin", "CLAUDE_CONFIG_DIR=/home/me/.claude", "HOME=/home/me", "CLAUDE_CONFIG_DIR=/dup"}
	got := MergeEnv(base, map[string]string{"CLAUDE_CONFIG_DIR": "/srv/client-a", "ZED": "1", "ALPHA": "2"})
	want := []string{"PATH=/usr/bin", "HOME=/home/me", "ALPHA=2", "CLAUDE_CONFIG_DIR=/srv/client-a", "ZED=1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("MergeEnv = %v, want %v", got, want)
	}
	if len(base) != 4 || base[1] != "CLAUDE_CONFIG_DIR=/home/me/.claude" {
		t.Fatalf("MergeEnv mutated its input: %v", base)
	}
}

// Without overrides the command must keep a nil Env so the child inherits the
// daemon environment exactly as it did before agent profiles existed.
func TestEnvCommandLeavesEnvNilWithoutOverrides(t *testing.T) {
	for _, env := range []map[string]string{nil, {}} {
		if cmd := envCommand(context.Background(), "", env, "true"); cmd.Env != nil {
			t.Fatalf("expected nil cmd.Env for %v, got %v", env, cmd.Env)
		}
	}
	cmd := envCommand(context.Background(), "/tmp", map[string]string{"K": "v"}, "true")
	if cmd.Dir != "/tmp" || !containsEntry(cmd.Env, "K=v") {
		t.Fatalf("expected dir and env override on command, got dir=%q env=%v", cmd.Dir, cmd.Env)
	}
}

func containsEntry(entries []string, want string) bool {
	for _, entry := range entries {
		if entry == want {
			return true
		}
	}
	return false
}

func TestExecRunnerRunStreamingWithEnv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("relies on sh")
	}
	t.Setenv("VIGILANTE_TEST_INHERITED", "inherited")
	t.Setenv("VIGILANTE_TEST_OVERRIDDEN", "daemon-value")

	script := `echo "$VIGILANTE_TEST_INHERITED|$VIGILANTE_TEST_OVERRIDDEN|$VIGILANTE_TEST_NEW"`
	var buf bytes.Buffer
	output, err := (ExecRunner{}).RunStreamingWithEnv(context.Background(), "", map[string]string{
		"VIGILANTE_TEST_OVERRIDDEN": "profile-value",
		"VIGILANTE_TEST_NEW":        "new",
	}, &buf, "sh", "-c", script)
	if err != nil {
		t.Fatal(err)
	}
	if want := "inherited|profile-value|new\n"; output != want || buf.String() != want {
		t.Fatalf("output = %q, streamed = %q, want %q", output, buf.String(), want)
	}

	// A nil writer still captures output, and no env leaves the environment alone.
	output, err = (ExecRunner{}).RunStreamingWithEnv(context.Background(), "", nil, nil, "sh", "-c", script)
	if err != nil {
		t.Fatal(err)
	}
	if want := "inherited|daemon-value|\n"; output != want {
		t.Fatalf("output = %q, want %q", output, want)
	}

	if _, err := (ExecRunner{}).RunStreamingWithEnv(context.Background(), "", map[string]string{"K": "v"}, nil, "sh", "-c", "exit 3"); err == nil {
		t.Fatal("expected error from failing command")
	}
}

// envRecordingRunner records which runner method received a call.
type envRecordingRunner struct {
	recordingRunner
	env    map[string]string
	method string
}

func (r *envRecordingRunner) Run(ctx context.Context, dir string, name string, args ...string) (string, error) {
	r.method = "Run"
	return r.recordingRunner.Run(ctx, dir, name, args...)
}

func (r *envRecordingRunner) RunStreaming(_ context.Context, _ string, w io.Writer, _ string, _ ...string) (string, error) {
	r.method = "RunStreaming"
	_, _ = io.WriteString(w, "streamed")
	return "streamed", nil
}

func (r *envRecordingRunner) RunStreamingWithEnv(_ context.Context, _ string, env map[string]string, _ io.Writer, _ string, _ ...string) (string, error) {
	r.method = "RunStreamingWithEnv"
	r.env = env
	return "with-env", nil
}

func TestRunWithEnvRoutesByEnvAndWriter(t *testing.T) {
	ctx := context.Background()

	base := &envRecordingRunner{recordingRunner: recordingRunner{output: "plain"}}
	if out, err := RunWithEnv(ctx, base, "", nil, nil, "agent"); err != nil || out != "plain" || base.method != "Run" {
		t.Fatalf("no env, no writer: out=%q err=%v method=%s; want Run", out, err, base.method)
	}
	var buf bytes.Buffer
	if out, err := RunWithEnv(ctx, base, "", map[string]string{}, &buf, "agent"); err != nil || out != "streamed" || base.method != "RunStreaming" {
		t.Fatalf("no env, writer: out=%q err=%v method=%s; want RunStreaming", out, err, base.method)
	}
	env := map[string]string{"CODEX_HOME": "/srv/x"}
	if out, err := RunWithEnv(ctx, base, "", env, &buf, "agent"); err != nil || out != "with-env" || base.method != "RunStreamingWithEnv" || !reflect.DeepEqual(base.env, env) {
		t.Fatalf("env: out=%q err=%v method=%s env=%v", out, err, base.method, base.env)
	}

	// A runner that cannot apply env must fail rather than silently drop it.
	plain := &recordingRunner{output: "plain"}
	if _, err := RunWithEnv(ctx, plain, "", env, nil, "agent"); err == nil || !strings.Contains(err.Error(), "does not support environment overrides") {
		t.Fatalf("expected unsupported-env error, got %v", err)
	}
	if plain.runCalls != 0 {
		t.Fatalf("command ran without its env overrides")
	}
}

func TestLoggingAndBudgetRunnersPassEnvThroughWithoutLoggingValues(t *testing.T) {
	base := &envRecordingRunner{}
	var logs bytes.Buffer
	var entries []AccessLogEntry
	runner := LoggingRunner{
		Base:      &GitHubBudgetRunner{Base: base},
		Logger:    slog.New(slog.NewTextHandler(&logs, nil)),
		AccessLog: func(e AccessLogEntry) { entries = append(entries, e) },
	}
	env := map[string]string{"CLAUDE_CONFIG_DIR": "/srv/secret-dir", "API_TOKEN": "s3cr3t-value"}
	out, err := RunWithEnv(context.Background(), runner, "/work", env, nil, "claude", "--print")
	if err != nil || out != "with-env" {
		t.Fatalf("out=%q err=%v", out, err)
	}
	if !reflect.DeepEqual(base.env, env) {
		t.Fatalf("env did not reach the base runner: %v", base.env)
	}
	text := logs.String()
	if !strings.Contains(text, "API_TOKEN") || !strings.Contains(text, "CLAUDE_CONFIG_DIR") {
		t.Fatalf("expected env names in log, got %s", text)
	}
	if strings.Contains(text, "s3cr3t-value") || strings.Contains(text, "/srv/secret-dir") {
		t.Fatalf("env values leaked into log: %s", text)
	}
	if len(entries) != 1 {
		t.Fatalf("expected one access log entry, got %d", len(entries))
	}

	// Wrappers over a base that cannot apply env report it instead of running.
	plain := &recordingRunner{}
	for name, r := range map[string]EnvRunner{
		"logging": LoggingRunner{Base: plain},
		"budget":  &GitHubBudgetRunner{Base: plain},
	} {
		if _, err := r.RunStreamingWithEnv(context.Background(), "", env, nil, "claude"); err == nil {
			t.Errorf("%s: expected unsupported-env error", name)
		}
	}
	if plain.runCalls != 0 {
		t.Fatalf("command ran without its env overrides")
	}
}
