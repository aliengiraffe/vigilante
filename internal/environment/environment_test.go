package environment

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nicobistolfi/vigilante/internal/telemetry"
	"github.com/nicobistolfi/vigilante/internal/testutil"
	otellog "go.opentelemetry.io/otel/log"
	sdklog "go.opentelemetry.io/otel/sdk/log"
)

func TestLoggingRunnerLogsCommandsWithoutSuccessOutputByDefault(t *testing.T) {
	var entries []string
	runner := LoggingRunner{
		Base: testutil.FakeRunner{
			Outputs: map[string]string{
				"gh issue list": "[]",
			},
		},
		Logf: func(format string, args ...any) {
			entries = append(entries, sprintf(format, args...))
		},
	}

	if _, err := runner.Run(context.Background(), "/tmp/repo", "gh", "issue", "list"); err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("unexpected log entries: %#v", entries)
	}
	if !strings.Contains(entries[0], `command start dir="/tmp/repo" cmd=gh issue list`) {
		t.Fatalf("unexpected start log: %s", entries[0])
	}
	if entries[1] != "command ok cmd=gh issue list" {
		t.Fatalf("unexpected success log: %s", entries[1])
	}
}

func TestLoggingRunnerCanLogSuccessOutputWhenEnabled(t *testing.T) {
	var entries []string
	runner := LoggingRunner{
		Base: testutil.FakeRunner{
			Outputs: map[string]string{
				"gh issue list": "[]",
			},
		},
		Logf: func(format string, args ...any) {
			entries = append(entries, sprintf(format, args...))
		},
		LogSuccessOutput: true,
	}

	if _, err := runner.Run(context.Background(), "/tmp/repo", "gh", "issue", "list"); err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("unexpected log entries: %#v", entries)
	}
	if !strings.Contains(entries[1], "command ok cmd=gh issue list output=[]") {
		t.Fatalf("unexpected success log: %s", entries[1])
	}
}

func TestLoggingRunnerLogsFailures(t *testing.T) {
	var entries []string
	runner := LoggingRunner{
		Base: testutil.FakeRunner{
			Errors: map[string]error{
				"git status": fmt.Errorf("boom"),
			},
		},
		Logf: func(format string, args ...any) {
			entries = append(entries, sprintf(format, args...))
		},
	}

	if _, err := runner.Run(context.Background(), "", "git", "status"); err == nil {
		t.Fatal("expected error")
	}
	if len(entries) != 2 {
		t.Fatalf("unexpected log entries: %#v", entries)
	}
	if !strings.Contains(entries[1], "command failed cmd=git status err=boom") {
		t.Fatalf("unexpected failure log: %s", entries[1])
	}
}

func TestLoggingRunnerEmitsTelemetryForTargetedInternalCommandsOnly(t *testing.T) {
	root := t.TempDir()
	exporter := &captureExporter{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	manager, err := telemetry.Setup(context.Background(), telemetry.SetupConfig{
		BuildInfo: telemetry.BuildInfo{
			Version:           "1.2.3",
			Distro:            "direct",
			TelemetryEndpoint: server.URL,
			TelemetryToken:    "token",
			TelemetryURLPath:  "/i/v1/logs",
		},
		StateRoot: root,
		EnvLookup: func(string) string { return "" },
		NewExporter: func(context.Context, telemetry.BuildInfo) (sdklog.Exporter, error) {
			return exporter, nil
		},
	})
	if err != nil {
		t.Fatalf("Setup() error = %v", err)
	}

	telemetry.SetDefault(manager)
	t.Cleanup(func() {
		telemetry.SetDefault(nil)
	})

	runner := LoggingRunner{
		Base: testutil.FakeRunner{
			Outputs: map[string]string{
				"git -C /tmp/repo status --short": "M internal/environment/environment.go\n",
				"gh issue list":                   "[]",
			},
		},
	}

	if _, err := runner.Run(context.Background(), "", "git", "-C", "/tmp/repo", "status", "--short"); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(context.Background(), "", "gh", "issue", "list"); err != nil {
		t.Fatal(err)
	}
	if err := manager.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}

	records := exporter.Records()
	if len(records) != 1 {
		t.Fatalf("expected 1 exported record, got %d", len(records))
	}
	if got, want := recordAttr(records[0], "command.name"), "git status"; got != want {
		t.Fatalf("command.name = %q, want %q", got, want)
	}
	if got, want := recordAttr(records[0], "tool.category"), "git"; got != want {
		t.Fatalf("tool.category = %q, want %q", got, want)
	}
}

func sprintf(format string, args ...any) string {
	return fmt.Sprintf(format, args...)
}

type captureExporter struct {
	records []sdklog.Record
}

func (e *captureExporter) Export(_ context.Context, records []sdklog.Record) error {
	for _, record := range records {
		e.records = append(e.records, record.Clone())
	}
	return nil
}

func (e *captureExporter) Shutdown(context.Context) error { return nil }

func (e *captureExporter) ForceFlush(context.Context) error { return nil }

func (e *captureExporter) Records() []sdklog.Record {
	out := make([]sdklog.Record, len(e.records))
	copy(out, e.records)
	return out
}

func recordAttr(record sdklog.Record, key string) string {
	value := ""
	record.WalkAttributes(func(kv otellog.KeyValue) bool {
		if kv.Key == key {
			value = kv.Value.AsString()
			return false
		}
		return true
	})
	return value
}
