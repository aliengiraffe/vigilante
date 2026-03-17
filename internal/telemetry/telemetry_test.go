package telemetry

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

func TestSetupCreatesAnonymousStateAndFirstRunNotice(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	stderr := &bytes.Buffer{}
	cfg := SetupConfig{
		BuildInfo: BuildInfo{
			Version:           "1.2.3",
			Distro:            "direct",
			TelemetryEndpoint: "https://otel.example.test/v1/traces",
			TelemetryToken:    "token",
		},
		StateRoot: root,
		Stderr:    stderr,
		EnvLookup: func(string) string { return "" },
		NewExporter: func(context.Context, BuildInfo) (sdktrace.SpanExporter, error) {
			return noopExporter{}, nil
		},
	}

	manager, err := Setup(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Setup() error = %v", err)
	}
	t.Cleanup(func() {
		_ = manager.Shutdown(context.Background())
	})

	if _, err := os.Stat(filepath.Join(root, "state.json")); err != nil {
		t.Fatalf("state.json not created: %v", err)
	}
	if got := stderr.String(); got == "" {
		t.Fatal("expected first-run notice on stderr")
	}

	stderr.Reset()
	manager, err = Setup(context.Background(), cfg)
	if err != nil {
		t.Fatalf("second Setup() error = %v", err)
	}
	t.Cleanup(func() {
		_ = manager.Shutdown(context.Background())
	})
	if got := stderr.String(); got != "" {
		t.Fatalf("expected no repeat notice, got %q", got)
	}
}

func TestSetupDisabledByConsentFlags(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	manager, err := Setup(context.Background(), SetupConfig{
		BuildInfo: BuildInfo{
			Version:           "1.2.3",
			Distro:            "direct",
			TelemetryEndpoint: "https://otel.example.test/v1/traces",
			TelemetryToken:    "token",
		},
		StateRoot: root,
		EnvLookup: func(key string) string {
			if key == "DO_NOT_TRACK" {
				return "1"
			}
			return ""
		},
	})
	if err != nil {
		t.Fatalf("Setup() error = %v", err)
	}
	if manager == nil {
		t.Fatal("expected disabled manager instance")
	}
	if _, err := os.Stat(filepath.Join(root, "state.json")); !os.IsNotExist(err) {
		t.Fatalf("expected no state file when telemetry is disabled, got err=%v", err)
	}
}

func TestCommandName(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		args []string
		want string
	}{
		{name: "root", args: nil, want: "root"},
		{name: "help flag", args: []string{"--help"}, want: "help"},
		{name: "simple", args: []string{"watch", "/tmp/repo"}, want: "watch"},
		{name: "grouped", args: []string{"daemon", "run", "--once"}, want: "daemon run"},
		{name: "grouped with flag", args: []string{"service", "--help"}, want: "service"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := CommandName(tc.args); got != tc.want {
				t.Fatalf("CommandName(%v) = %q, want %q", tc.args, got, tc.want)
			}
		})
	}
}

type noopExporter struct{}

func (noopExporter) ExportSpans(context.Context, []sdktrace.ReadOnlySpan) error { return nil }

func (noopExporter) Shutdown(context.Context) error { return nil }
