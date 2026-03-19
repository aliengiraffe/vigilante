package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nicobistolfi/vigilante/internal/state"
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
		NewAnalyticsExporter: func(BuildInfo, string) (analyticsExporter, error) {
			return &captureAnalyticsExporter{}, nil
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

func TestSetupEnablesAnalyticsWithoutTelemetryURLPath(t *testing.T) {
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
		NewAnalyticsExporter: func(BuildInfo, string) (analyticsExporter, error) {
			return &captureAnalyticsExporter{}, nil
		},
	})
	if err != nil {
		t.Fatalf("Setup() error = %v", err)
	}
	if manager == nil {
		t.Fatal("expected manager instance")
	}
	if _, err := os.Stat(filepath.Join(root, "state.json")); err != nil {
		t.Fatalf("expected state file when analytics is enabled, got err=%v", err)
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
		{name: "gh proxy sanitizes positional args", args: []string{"gh", "repo", "view", "owner/repo"}, want: "gh repo view"},
		{name: "gh proxy keeps api path bounded", args: []string{"gh", "api", "repos/owner/repo/issues/1"}, want: "gh api"},
		{name: "git proxy skips global flag values", args: []string{"git", "-C", "/tmp/repo", "status", "--short"}, want: "git status"},
		{name: "docker proxy records compose path", args: []string{"docker", "--context", "prod", "compose", "up", "-d"}, want: "docker compose up"},
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

func TestCommandFeatureAreaUsesToolProxyForSupportedProxyCommands(t *testing.T) {
	t.Parallel()

	if got, want := commandFeatureArea(commandGroup(CommandName([]string{"gh", "repo", "view", "owner/repo"}))), "tool_proxy"; got != want {
		t.Fatalf("feature area = %q, want %q", got, want)
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

func TestTelemetryBaseURL(t *testing.T) {
	t.Parallel()

	got, err := telemetryBaseURL(BuildInfo{TelemetryEndpoint: " us.i.posthog.com "})
	if err != nil {
		t.Fatalf("telemetryBaseURL() error = %v", err)
	}
	if want := "https://us.i.posthog.com"; got != want {
		t.Fatalf("telemetryBaseURL() = %q, want %q", got, want)
	}

	got, err = telemetryBaseURL(BuildInfo{TelemetryEndpoint: "https://eu.i.posthog.com/ingest"})
	if err != nil {
		t.Fatalf("telemetryBaseURL() error = %v", err)
	}
	if want := "https://eu.i.posthog.com"; got != want {
		t.Fatalf("telemetryBaseURL() = %q, want %q", got, want)
	}
}

func TestShutdownTimeoutExceedsExportTimeout(t *testing.T) {
	t.Parallel()

	if got, want := exportTimeout, 2*time.Second; got != want {
		t.Fatalf("exportTimeout = %v, want %v", got, want)
	}
	if got, want := ShutdownTimeout(), 3*time.Second; got != want {
		t.Fatalf("ShutdownTimeout() = %v, want %v", got, want)
	}
	if ShutdownTimeout() <= exportTimeout {
		t.Fatalf("ShutdownTimeout() = %v, want greater than exportTimeout %v", ShutdownTimeout(), exportTimeout)
	}
}

func TestStartCommandEmitsLogRecord(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	exporter := &captureExporter{}
	analytics := &captureAnalyticsExporter{}
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
		NewAnalyticsExporter: func(BuildInfo, string) (analyticsExporter, error) {
			return analytics, nil
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

	events := analytics.Events()
	if len(events) != 2 {
		t.Fatalf("expected 2 analytics events, got %d", len(events))
	}
	if got, want := events[0].Event, "cli_command_started"; got != want {
		t.Fatalf("events[0].Event = %q, want %q", got, want)
	}
	if got, want := events[1].Event, "cli_command_completed"; got != want {
		t.Fatalf("events[1].Event = %q, want %q", got, want)
	}
	if got, want := events[0].Properties["command_name"], "watch"; got != want {
		t.Fatalf("started command_name = %v, want %q", got, want)
	}
	if got, want := events[0].Properties["feature_area"], "watch_management"; got != want {
		t.Fatalf("started feature_area = %v, want %q", got, want)
	}
	if _, ok := events[0].Properties["path"]; ok {
		t.Fatal("expected analytics payload to omit raw command arguments")
	}
	if got, want := events[1].Properties["result"], "failure"; got != want {
		t.Fatalf("completed result = %v, want %q", got, want)
	}
	if got, want := events[1].Properties["exit_code"], 7; got != want {
		t.Fatalf("completed exit_code = %v, want %d", got, want)
	}
	if got, ok := events[1].Properties["duration_ms"].(int64); !ok || got < 0 {
		t.Fatalf("completed duration_ms = %v, want non-negative int64", events[1].Properties["duration_ms"])
	}
}

func TestStartCommandAnalyticsTaxonomyForGroupedCommands(t *testing.T) {
	t.Parallel()

	manager := &Manager{
		analytics: &captureAnalyticsExporter{},
		version:   "1.2.3",
		distro:    "direct",
		anonID:    "anon-123",
	}

	_, finish := manager.StartCommand(context.Background(), []string{"service", "restart"})
	finish(0)
	if err := manager.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}

	events := manager.analytics.(*captureAnalyticsExporter).Events()
	if len(events) != 2 {
		t.Fatalf("expected 2 analytics events, got %d", len(events))
	}
	if got, want := events[0].Properties["command_name"], "service restart"; got != want {
		t.Fatalf("command_name = %v, want %q", got, want)
	}
	if got, want := events[0].Properties["command_group"], "service"; got != want {
		t.Fatalf("command_group = %v, want %q", got, want)
	}
	if got, want := events[0].Properties["feature_area"], "service_management"; got != want {
		t.Fatalf("feature_area = %v, want %q", got, want)
	}
	if got, want := events[1].Properties["result"], "success"; got != want {
		t.Fatalf("result = %v, want %q", got, want)
	}
}

func TestInternalCommandName(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name         string
		command      string
		args         []string
		wantName     string
		wantCategory string
		wantOK       bool
	}{
		{
			name:         "git uses proxy sanitization",
			command:      "git",
			args:         []string{"-C", "/tmp/repo", "worktree", "add", "/tmp/repo/.worktrees/issue-1", "branch"},
			wantName:     "git worktree add",
			wantCategory: "git",
			wantOK:       true,
		},
		{
			name:         "codex drops prompt payload",
			command:      "codex",
			args:         []string{"exec", "--cd", "/tmp/worktree", "--dangerously-bypass-approvals-and-sandbox", "write a full prompt here"},
			wantName:     "codex exec",
			wantCategory: "coding_agent",
			wantOK:       true,
		},
		{
			name:         "claude with prompt flags stays bounded",
			command:      "claude",
			args:         []string{"--print", "--dangerously-skip-permissions", "free form prompt"},
			wantName:     "claude",
			wantCategory: "coding_agent",
			wantOK:       true,
		},
		{
			name:         "gemini skips prompt flag value",
			command:      "gemini",
			args:         []string{"--prompt", "free form prompt", "--yolo"},
			wantName:     "gemini",
			wantCategory: "coding_agent",
			wantOK:       true,
		},
		{
			name:    "non target command excluded",
			command: "gh",
			args:    []string{"issue", "view", "255"},
			wantOK:  false,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			gotName, gotCategory, gotOK := internalCommandName(tc.command, tc.args)
			if gotOK != tc.wantOK {
				t.Fatalf("internalCommandName(%q, %v) ok = %v, want %v", tc.command, tc.args, gotOK, tc.wantOK)
			}
			if !tc.wantOK {
				return
			}
			if gotName != tc.wantName {
				t.Fatalf("internalCommandName(%q, %v) name = %q, want %q", tc.command, tc.args, gotName, tc.wantName)
			}
			if gotCategory != tc.wantCategory {
				t.Fatalf("internalCommandName(%q, %v) category = %q, want %q", tc.command, tc.args, gotCategory, tc.wantCategory)
			}
		})
	}
}

func TestCaptureInternalCommandEmitsSanitizedOTELRecord(t *testing.T) {
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
		NewAnalyticsExporter: func(BuildInfo, string) (analyticsExporter, error) {
			return &captureAnalyticsExporter{}, nil
		},
	})
	if err != nil {
		t.Fatalf("Setup() error = %v", err)
	}

	SetDefault(manager)
	t.Cleanup(func() {
		SetDefault(nil)
	})

	CaptureInternalCommand(context.Background(), "codex", []string{"exec", "--cd", "/tmp/worktree", "--dangerously-bypass-approvals-and-sandbox", "top secret prompt"}, 7, 123)

	if err := manager.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}

	records := exporter.Records()
	if len(records) != 1 {
		t.Fatalf("expected 1 exported record, got %d", len(records))
	}

	record := records[0]
	if got, want := record.EventName(), "internal.command"; got != want {
		t.Fatalf("record.EventName() = %q, want %q", got, want)
	}
	if got, want := record.Severity(), otellog.SeverityError; got != want {
		t.Fatalf("record.Severity() = %v, want %v", got, want)
	}
	if got, want := record.Body().AsString(), "internal command failed"; got != want {
		t.Fatalf("record.Body() = %q, want %q", got, want)
	}

	attrs := map[string]otellog.Value{}
	record.WalkAttributes(func(kv otellog.KeyValue) bool {
		attrs[kv.Key] = kv.Value
		return true
	})
	if got, want := attrs["command.name"].AsString(), "codex exec"; got != want {
		t.Fatalf("command.name = %q, want %q", got, want)
	}
	if got, want := attrs["command.group"].AsString(), "codex"; got != want {
		t.Fatalf("command.group = %q, want %q", got, want)
	}
	if got, want := attrs["tool.category"].AsString(), "coding_agent"; got != want {
		t.Fatalf("tool.category = %q, want %q", got, want)
	}
	if got, want := attrs["tool.name"].AsString(), "codex"; got != want {
		t.Fatalf("tool.name = %q, want %q", got, want)
	}
	if got, want := attrs["invocation"].AsString(), "internal"; got != want {
		t.Fatalf("invocation = %q, want %q", got, want)
	}
	if got, want := attrs["command.exit_code"].AsInt64(), int64(7); got != want {
		t.Fatalf("command.exit_code = %d, want %d", got, want)
	}
	if got, want := attrs["command.duration_ms"].AsInt64(), int64(123); got != want {
		t.Fatalf("command.duration_ms = %d, want %d", got, want)
	}
	for _, forbidden := range []string{"/tmp/worktree", "top secret prompt"} {
		if strings.Contains(attrs["command.name"].AsString(), forbidden) {
			t.Fatalf("command.name leaked %q", forbidden)
		}
	}
}

func TestCaptureWorkflowEventUsesDefaultManagerAndBoundedProperties(t *testing.T) {
	t.Parallel()

	analytics := &captureAnalyticsExporter{}
	manager := &Manager{
		analytics: analytics,
		version:   "1.2.3",
		distro:    "direct",
		anonID:    "anon-123",
	}

	SetDefault(manager)
	t.Cleanup(func() {
		SetDefault(nil)
	})

	CaptureWorkflowEvent("issue_session_transition", map[string]any{
		"feature_area": "issue_session",
		"status":       "blocked",
		"source":       "dispatch",
	})

	if err := manager.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}

	events := analytics.Events()
	if len(events) != 1 {
		t.Fatalf("expected 1 analytics event, got %d", len(events))
	}
	if got, want := events[0].Event, "issue_session_transition"; got != want {
		t.Fatalf("event = %q, want %q", got, want)
	}
	if got, want := events[0].Properties["feature_area"], "issue_session"; got != want {
		t.Fatalf("feature_area = %v, want %q", got, want)
	}
	if got, want := events[0].Properties["status"], "blocked"; got != want {
		t.Fatalf("status = %v, want %q", got, want)
	}
	if got, want := events[0].Properties["app_version"], "1.2.3"; got != want {
		t.Fatalf("app_version = %v, want %q", got, want)
	}
	if got, want := events[0].Properties["platform_os"], runtime.GOOS; got != want {
		t.Fatalf("platform_os = %v, want %q", got, want)
	}
}

func TestCaptureDownstreamRateLimitEmitsBoundedProviderQuotaSignal(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	exporter := &captureExporter{}
	analytics := &captureAnalyticsExporter{}
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
		NewAnalyticsExporter: func(BuildInfo, string) (analyticsExporter, error) {
			return analytics, nil
		},
	})
	if err != nil {
		t.Fatalf("Setup() error = %v", err)
	}

	SetDefault(manager)
	t.Cleanup(func() {
		SetDefault(nil)
	})

	CaptureDownstreamRateLimit("issue_execution", "codex exec", state.BlockedReason{Kind: "provider_quota"}, "You've hit your usage limit. Purchase more credits with token sk-live-secret.")
	if err := manager.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}

	records := exporter.Records()
	if len(records) != 1 {
		t.Fatalf("expected 1 exported record, got %d", len(records))
	}

	record := records[0]
	if got, want := record.EventName(), "downstream.rate_limit"; got != want {
		t.Fatalf("record.EventName() = %q, want %q", got, want)
	}
	if got, want := record.Severity(), otellog.SeverityError; got != want {
		t.Fatalf("record.Severity() = %v, want %v", got, want)
	}
	if got, want := record.Body().AsString(), "downstream service rate limited"; got != want {
		t.Fatalf("record.Body() = %q, want %q", got, want)
	}

	attrs := map[string]otellog.Value{}
	record.WalkAttributes(func(kv otellog.KeyValue) bool {
		attrs[kv.Key] = kv.Value
		return true
	})
	if got, want := attrs["downstream.service"].AsString(), "provider"; got != want {
		t.Fatalf("downstream.service = %q, want %q", got, want)
	}
	if got, want := attrs["downstream.operation"].AsString(), "codex exec"; got != want {
		t.Fatalf("downstream.operation = %q, want %q", got, want)
	}
	if got, want := attrs["downstream.classification"].AsString(), "quota"; got != want {
		t.Fatalf("downstream.classification = %q, want %q", got, want)
	}
	if got, want := attrs["downstream.retryable"].AsBool(), false; got != want {
		t.Fatalf("downstream.retryable = %v, want %v", got, want)
	}

	events := analytics.Events()
	if len(events) != 1 {
		t.Fatalf("expected 1 analytics event, got %d", len(events))
	}
	if got, want := events[0].Event, "downstream_service_rate_limited"; got != want {
		t.Fatalf("events[0].Event = %q, want %q", got, want)
	}
	if got, want := events[0].Properties["service"], "provider"; got != want {
		t.Fatalf("service = %v, want %q", got, want)
	}
	if got, want := events[0].Properties["classification"], "quota"; got != want {
		t.Fatalf("classification = %v, want %q", got, want)
	}
	encoded, err := json.Marshal(events[0])
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if strings.Contains(string(encoded), "sk-live-secret") {
		t.Fatalf("expected telemetry payload to omit raw diagnostic details, got %s", encoded)
	}
}

func TestCaptureDownstreamRateLimitDetectsGitHubRateLimit(t *testing.T) {
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
		NewAnalyticsExporter: func(BuildInfo, string) (analyticsExporter, error) {
			return &captureAnalyticsExporter{}, nil
		},
	})
	if err != nil {
		t.Fatalf("Setup() error = %v", err)
	}

	SetDefault(manager)
	t.Cleanup(func() {
		SetDefault(nil)
	})

	CaptureDownstreamRateLimit("dispatch", "gh api", state.BlockedReason{Kind: "provider_runtime_error"}, "API rate limit exceeded for github user 12345.")
	if err := manager.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}

	records := exporter.Records()
	if len(records) != 1 {
		t.Fatalf("expected 1 exported record, got %d", len(records))
	}

	attrs := map[string]otellog.Value{}
	records[0].WalkAttributes(func(kv otellog.KeyValue) bool {
		attrs[kv.Key] = kv.Value
		return true
	})
	if got, want := attrs["downstream.service"].AsString(), "github"; got != want {
		t.Fatalf("downstream.service = %q, want %q", got, want)
	}
	if got, want := attrs["downstream.classification"].AsString(), "rate_limit"; got != want {
		t.Fatalf("downstream.classification = %q, want %q", got, want)
	}
	if got, want := attrs["downstream.retryable"].AsBool(), true; got != want {
		t.Fatalf("downstream.retryable = %v, want %v", got, want)
	}
}

func TestHTTPAnalyticsExporterUsesBatchEndpoint(t *testing.T) {
	t.Parallel()

	var (
		mu        sync.Mutex
		gotPath   string
		gotBody   string
		gotMethod string
	)
	server := newTestServer(t, func(body string, method string, path string) {
		mu.Lock()
		defer mu.Unlock()
		gotBody = body
		gotMethod = method
		gotPath = path
	})

	exporter, err := newHTTPAnalyticsExporter(BuildInfo{
		TelemetryEndpoint: server.URL,
		TelemetryToken:    "project-key",
	}, "anon-123")
	if err != nil {
		t.Fatalf("newHTTPAnalyticsExporter() error = %v", err)
	}
	if err := exporter.Export(context.Background(), []analyticsEvent{{
		Type:       "capture",
		UUID:       "event-1",
		Timestamp:  time.Unix(0, 0).UTC(),
		DistinctID: "anon-123",
		Event:      "cli_command_started",
		Properties: map[string]any{"command_name": "watch"},
	}}); err != nil {
		t.Fatalf("Export() error = %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if got, want := gotMethod, "POST"; got != want {
		t.Fatalf("method = %q, want %q", got, want)
	}
	if got, want := gotPath, "/batch/"; got != want {
		t.Fatalf("path = %q, want %q", got, want)
	}
	for _, want := range []string{`"api_key":"project-key"`, `"event":"cli_command_started"`, `"distinct_id":"anon-123"`, `"batch":[`} {
		if !strings.Contains(gotBody, want) {
			t.Fatalf("expected batch request body to contain %q, got %q", want, gotBody)
		}
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

type captureAnalyticsExporter struct {
	mu     sync.Mutex
	events []analyticsEvent
}

func (e *captureAnalyticsExporter) Events() []analyticsEvent {
	e.mu.Lock()
	defer e.mu.Unlock()

	out := make([]analyticsEvent, len(e.events))
	copy(out, e.events)
	return out
}

func (e *captureAnalyticsExporter) Export(_ context.Context, events []analyticsEvent) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.events = append(e.events, events...)
	return nil
}

func newTestServer(t *testing.T, capture func(body string, method string, path string)) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("ReadAll() error = %v", err)
		}
		capture(string(body), r.Method, r.URL.Path)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	return server
}
