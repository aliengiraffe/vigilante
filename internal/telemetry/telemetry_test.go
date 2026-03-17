package telemetry

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	otellog "go.opentelemetry.io/otel/log"
	sdklog "go.opentelemetry.io/otel/sdk/log"
)

func TestSetupCreatesAnonymousStateAndFirstRunNotice(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	stderr := &bytes.Buffer{}
	cfg := SetupConfig{
		BuildInfo: BuildInfo{
			Version:           "1.2.3",
			Distro:            "direct",
			TelemetryEndpoint: "otel.example.test",
			TelemetryToken:    "token",
			TelemetryURLPath:  "/i/v1/logs",
		},
		StateRoot: root,
		Stderr:    stderr,
		EnvLookup: func(string) string { return "" },
		NewExporter: func(context.Context, BuildInfo) (sdklog.Exporter, error) {
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
			TelemetryEndpoint: "otel.example.test",
			TelemetryToken:    "token",
			TelemetryURLPath:  "/i/v1/logs",
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

func TestSetupDisabledWithoutTelemetryURLPath(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	manager, err := Setup(context.Background(), SetupConfig{
		BuildInfo: BuildInfo{
			Version:           "1.2.3",
			Distro:            "direct",
			TelemetryEndpoint: "otel.example.test",
			TelemetryToken:    "token",
		},
		StateRoot: root,
		EnvLookup: func(string) string { return "" },
	})
	if err != nil {
		t.Fatalf("Setup() error = %v", err)
	}
	if manager == nil {
		t.Fatal("expected disabled manager instance")
	}
	if _, err := os.Stat(filepath.Join(root, "state.json")); !os.IsNotExist(err) {
		t.Fatalf("expected no state file when telemetry config is incomplete, got err=%v", err)
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

func TestTelemetryURLPath(t *testing.T) {
	t.Parallel()

	if got, want := telemetryURLPath(BuildInfo{TelemetryURLPath: " /custom/v1/traces \n"}), "/custom/v1/traces"; got != want {
		t.Fatalf("telemetryURLPath() = %q, want %q", got, want)
	}
	if got := telemetryURLPath(BuildInfo{}); got != "" {
		t.Fatalf("telemetryURLPath() = %q, want empty string", got)
	}
}

func TestTelemetryExporterSettingsUseEmbeddedURLPath(t *testing.T) {
	t.Parallel()

	settings := telemetryExporterSettings(BuildInfo{
		TelemetryEndpoint: " otel.example.test ",
		TelemetryToken:    " token ",
		TelemetryURLPath:  " /i/v1/logs \n",
	})

	if got, want := settings.Endpoint, "otel.example.test"; got != want {
		t.Fatalf("settings.Endpoint = %q, want %q", got, want)
	}
	if got, want := settings.Headers["Authorization"], "Bearer token"; got != want {
		t.Fatalf("settings.Headers[Authorization] = %q, want %q", got, want)
	}
	if got, want := settings.URLPath, "/i/v1/logs"; got != want {
		t.Fatalf("settings.URLPath = %q, want %q", got, want)
	}
	if got, want := settings.Timeout, exportTimeout; got != want {
		t.Fatalf("settings.Timeout = %v, want %v", got, want)
	}
}

func TestStartCommandEmitsLogRecord(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	exporter := &captureExporter{}
	manager, err := Setup(context.Background(), SetupConfig{
		BuildInfo: BuildInfo{
			Version:           "1.2.3",
			Distro:            "direct",
			TelemetryEndpoint: "otel.example.test",
			TelemetryToken:    "token",
			TelemetryURLPath:  "/i/v1/logs",
		},
		StateRoot: root,
		EnvLookup: func(string) string { return "" },
		NewExporter: func(context.Context, BuildInfo) (sdklog.Exporter, error) {
			return exporter, nil
		},
	})
	if err != nil {
		t.Fatalf("Setup() error = %v", err)
	}

	ctx, finish := manager.StartCommand(context.Background(), []string{"watch", "/tmp/repo"})
	finish(7)
	if err := manager.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}

	records := exporter.Records()
	if len(records) != 1 {
		t.Fatalf("expected 1 exported record, got %d", len(records))
	}

	record := records[0]
	if got, want := record.EventName(), "cli.command"; got != want {
		t.Fatalf("record.EventName() = %q, want %q", got, want)
	}
	if got, want := record.Severity(), otellog.SeverityError; got != want {
		t.Fatalf("record.Severity() = %v, want %v", got, want)
	}
	if got, want := record.Body().AsString(), "command failed"; got != want {
		t.Fatalf("record.Body() = %q, want %q", got, want)
	}

	attrs := map[string]otellog.Value{}
	record.WalkAttributes(func(kv otellog.KeyValue) bool {
		attrs[kv.Key] = kv.Value
		return true
	})
	if got, want := attrs["command.name"].AsString(), "watch"; got != want {
		t.Fatalf("command.name = %q, want %q", got, want)
	}
	if got, want := attrs["command.exit_code"].AsInt64(), int64(7); got != want {
		t.Fatalf("command.exit_code = %d, want %d", got, want)
	}
	if got := attrs["command.duration_ms"].AsInt64(); got < 0 {
		t.Fatalf("command.duration_ms = %d, want non-negative", got)
	}
}

type noopExporter struct{}

func (noopExporter) Export(context.Context, []sdklog.Record) error { return nil }

func (noopExporter) Shutdown(context.Context) error { return nil }

func (noopExporter) ForceFlush(context.Context) error { return nil }

type captureExporter struct {
	mu      sync.Mutex
	records []sdklog.Record
}

func (e *captureExporter) Export(_ context.Context, records []sdklog.Record) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	for _, record := range records {
		e.records = append(e.records, record.Clone())
	}
	return nil
}

func (e *captureExporter) Shutdown(context.Context) error { return nil }

func (e *captureExporter) ForceFlush(context.Context) error { return nil }

func (e *captureExporter) Records() []sdklog.Record {
	e.mu.Lock()
	defer e.mu.Unlock()

	out := make([]sdklog.Record, len(e.records))
	copy(out, e.records)
	return out
}
