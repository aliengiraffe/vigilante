package telemetry

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nicobistolfi/vigilante/internal/state"
)

func TestCoverageClassifiersAndCommandParsers(t *testing.T) {
	for _, tc := range []struct {
		diagnostic, want string
		retry, ok        bool
	}{
		{"secondary rate limit", "rate_limit", true, true}, {"HTTP 429", "rate_limit", true, true},
		{"usage limit", "quota", false, true}, {"credits exhausted; purchase more", "quota", false, true}, {"ordinary error", "", false, false},
	} {
		got, retry, ok := classifyDownstreamRateLimit(state.BlockedReason{}, tc.diagnostic)
		if got != tc.want || retry != tc.retry || ok != tc.ok {
			t.Errorf("classify(%q)=(%q,%v,%v)", tc.diagnostic, got, retry, ok)
		}
	}
	if got, retry, ok := classifyDownstreamRateLimit(state.BlockedReason{Kind: "provider_quota"}, ""); got != "quota" || retry || !ok {
		t.Fatalf("provider quota=(%q,%v,%v)", got, retry, ok)
	}
	for _, tc := range []struct{ operation, kind, diagnostic, want string }{
		{"gh pr view", "", "", "github"}, {"", "provider_quota", "", "provider"}, {"", "", "otlp failure", "telemetry_export"}, {"other", "", "", "unknown"},
	} {
		if got := downstreamServiceCategory(tc.operation, state.BlockedReason{Kind: tc.kind}, tc.diagnostic); got != tc.want {
			t.Errorf("category=%q want %q", got, tc.want)
		}
	}
	if boundedOperation("   ") != "unknown" || boundedOperation(" ok ") != "ok" || len(boundedOperation(strings.Repeat("x", 90))) != 80 {
		t.Fatal("boundedOperation boundaries")
	}
	for category, want := range map[string]string{"git": "tool_proxy", "coding_agent": "coding_agent", "other": "internal_subprocess"} {
		if got := internalCommandFeatureArea(category); got != want {
			t.Errorf("feature=%q", got)
		}
	}
	for group, want := range map[string]string{"setup": "setup", "watch": "watch_management", "status": "service_management", "daemon": "daemon", "cleanup": "cleanup", "resume": "issue_session", "gh": "tool_proxy", "help": "operator_cli", "weird": "operator_cli"} {
		if got := commandFeatureArea(group); got != want {
			t.Errorf("group %q=%q", group, got)
		}
	}
	for _, tc := range []struct {
		tool string
		args []string
		want string
	}{
		{"codex", []string{"--model", "gpt", "exec"}, "exec"}, {"codex", []string{"--help"}, "help"}, {"claude", []string{"--permission-mode", "plan", "doctor"}, "doctor"}, {"gemini", []string{"--prompt=hi", "mcp"}, "mcp"}, {"gemini", []string{"secret"}, ""},
	} {
		if got := internalCommandPath(tc.tool, tc.args); got != tc.want {
			t.Errorf("path %s %#v=%q want %q", tc.tool, tc.args, got, tc.want)
		}
	}
	for _, tc := range []struct {
		tool string
		args []string
		want string
	}{
		{"gh", []string{"--repo", "owner/repo", "pr", "view"}, "pr view"}, {"git", []string{"-C", "/tmp", "worktree", "list"}, "worktree list"}, {"docker", []string{"--context=local", "compose", "up"}, "compose up"}, {"gh", []string{"--help"}, "help"}, {"git", nil, ""},
	} {
		if got := proxyCommandPath(tc.tool, tc.args); got != tc.want {
			t.Errorf("proxy %s %#v=%q want %q", tc.tool, tc.args, got, tc.want)
		}
	}
}

func TestHTTPAnalyticsExporter(t *testing.T) {
	var got analyticsBatch
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/batch/" || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("request %s %#v", r.URL.Path, r.Header)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()
	exporter := &httpAnalyticsExporter{baseURL: server.URL, apiKey: "key", client: server.Client()}
	if err := exporter.Export(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if err := exporter.Export(context.Background(), []analyticsEvent{{Event: "command"}}); err != nil {
		t.Fatal(err)
	}
	if got.APIKey != "key" || len(got.Messages) != 1 {
		t.Fatalf("batch=%#v", got)
	}
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, " slow down ")
	}))
	defer bad.Close()
	exporter.baseURL, exporter.client = bad.URL, bad.Client()
	if err := exporter.Export(context.Background(), []analyticsEvent{{Event: "command"}}); err == nil || !strings.Contains(err.Error(), "slow down") {
		t.Fatalf("error=%v", err)
	}
}

func TestEnsureLocalStateCreateReuseAndInvalid(t *testing.T) {
	root := filepath.Join(t.TempDir(), "telemetry")
	created, fresh, err := ensureLocalState(root)
	if err != nil || !fresh || strings.TrimSpace(created.AnonymousID) == "" {
		t.Fatalf("created=%#v fresh=%v err=%v", created, fresh, err)
	}
	reused, fresh, err := ensureLocalState(root)
	if err != nil || fresh || reused.AnonymousID != created.AnonymousID {
		t.Fatalf("reused=%#v fresh=%v err=%v", reused, fresh, err)
	}
	if err := os.WriteFile(filepath.Join(root, "state.json"), []byte("not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ensureLocalState(root); err == nil {
		t.Fatal("expected invalid state error")
	}
	fileRoot := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(fileRoot, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ensureLocalState(fileRoot); err == nil {
		t.Fatal("expected root creation error")
	}
}

// The command name becomes the telemetry event name, so it has to be stable and
// must never carry user data. Flags and help tokens collapse rather than being
// echoed, and proxied tool invocations are grouped by their subcommand path.
func TestCommandNameTableCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "no args", args: nil, want: "root"},
		{name: "empty args", args: []string{}, want: "root"},
		{name: "help token", args: []string{"help"}, want: "help"},
		{name: "short help flag", args: []string{"-h"}, want: "help"},
		{name: "long help flag", args: []string{"--help"}, want: "help"},
		{name: "any leading flag is help", args: []string{"--version"}, want: "help"},
		{name: "plain command", args: []string{"status"}, want: "status"},
		{name: "command that does not expand", args: []string{"status", "extra"}, want: "status"},

		// Group commands expand to two words so `daemon run` and `daemon stop`
		// are distinguishable in the metrics.
		{name: "group command expands", args: []string{"daemon", "run"}, want: "daemon run"},
		{name: "resume expands", args: []string{"resume", "all"}, want: "resume all"},
		{name: "cleanup expands", args: []string{"cleanup", "sessions"}, want: "cleanup sessions"},
		{name: "completion expands", args: []string{"completion", "zsh"}, want: "completion zsh"},
		{name: "redispatch expands", args: []string{"redispatch", "session"}, want: "redispatch session"},
		{name: "service expands", args: []string{"service", "install"}, want: "service install"},

		// A flag or help token after a group command must not be treated as the
		// subcommand, or every flag combination becomes its own event name.
		{name: "group command with a flag does not expand", args: []string{"daemon", "--once"}, want: "daemon"},
		{name: "group command with help does not expand", args: []string{"daemon", "--help"}, want: "daemon"},
		{name: "group command with help word does not expand", args: []string{"daemon", "help"}, want: "daemon"},
		{name: "group command alone", args: []string{"daemon"}, want: "daemon"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := CommandName(tt.args); got != tt.want {
				t.Fatalf("CommandName(%v) = %q, want %q", tt.args, got, tt.want)
			}
		})
	}
}

func TestExpandsCommandGroup(t *testing.T) {
	t.Parallel()

	for _, command := range []string{"cleanup", "completion", "daemon", "redispatch", "resume", "service"} {
		if !expandsCommandGroup(command) {
			t.Errorf("%q should expand", command)
		}
	}
	for _, command := range []string{"status", "watch", "setup", "logs", "", "Daemon"} {
		if expandsCommandGroup(command) {
			t.Errorf("%q must not expand", command)
		}
	}
}

func TestIsHelpToken(t *testing.T) {
	t.Parallel()

	for _, token := range []string{"-h", "--help", "help"} {
		if !isHelpToken(token) {
			t.Errorf("%q should be a help token", token)
		}
	}
	for _, token := range []string{"-H", "--Help", "helpme", "", "-help"} {
		if isHelpToken(token) {
			t.Errorf("%q must not be a help token", token)
		}
	}
}

func TestCommandGroup(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"daemon run": "daemon",
		"status":     "status",
		"":           "root",
		"   ":        "root",
		"gh pr list": "gh",
	}
	for input, want := range tests {
		if got := commandGroup(input); got != want {
			t.Errorf("commandGroup(%q) = %q, want %q", input, got, want)
		}
	}
}

// Opting out has to be honored from several directions, since a user who set any
// of these expects no analytics to leave the machine.
func TestTelemetryDisabled(t *testing.T) {
	t.Parallel()

	// An info value that would otherwise enable analytics, so the env checks are
	// what decide each case.
	enabled := BuildInfo{TelemetryEndpoint: "https://example.com", TelemetryToken: "t"}

	lookupFrom := func(pairs map[string]string) func(string) string {
		return func(key string) string { return pairs[key] }
	}

	tests := []struct {
		name string
		env  map[string]string
		want bool
	}{
		{name: "DO_NOT_TRACK=1 disables", env: map[string]string{"DO_NOT_TRACK": "1"}, want: true},
		{name: "MYTOOL_NO_ANALYTICS=1 disables", env: map[string]string{"MYTOOL_NO_ANALYTICS": "1"}, want: true},
		{name: "CI=true disables", env: map[string]string{"CI": "true"}, want: true},
		{name: "CI=TRUE disables case-insensitively", env: map[string]string{"CI": "TRUE"}, want: true},
		{name: "CI=false does not disable", env: map[string]string{"CI": "false"}, want: false},
		{name: "DO_NOT_TRACK=0 does not disable", env: map[string]string{"DO_NOT_TRACK": "0"}, want: false},
		{name: "no env does not disable", env: nil, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := telemetryDisabled(enabled, lookupFrom(tt.env)); got != tt.want {
				t.Fatalf("telemetryDisabled() = %v, want %v", got, tt.want)
			}
		})
	}
}

// With no build-time endpoint configured, telemetry must be off regardless of the
// environment: there is nowhere to send it.
func TestTelemetryDisabledWithoutBuildConfiguration(t *testing.T) {
	t.Parallel()

	noEnv := func(string) string { return "" }
	if !telemetryDisabled(BuildInfo{}, noEnv) {
		t.Fatal("an unconfigured build must disable telemetry")
	}
}

func TestDefaultString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		value    string
		fallback string
		want     string
	}{
		{value: "set", fallback: "fb", want: "set"},
		{value: "", fallback: "fb", want: "fb"},
		{value: "   ", fallback: "fb", want: "fb"},
		{value: " padded ", fallback: "fb", want: " padded "},
	}
	for _, tt := range tests {
		if got := defaultString(tt.value, tt.fallback); got != tt.want {
			t.Errorf("defaultString(%q, %q) = %q, want %q", tt.value, tt.fallback, got, tt.want)
		}
	}
}

func TestTelemetryURLPathTrimming(t *testing.T) {
	t.Parallel()

	if got := telemetryURLPath(BuildInfo{TelemetryURLPath: "  /v1/events  "}); got != "/v1/events" {
		t.Fatalf("got %q, want the trimmed path", got)
	}
	if got := telemetryURLPath(BuildInfo{}); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}

// A bare host is upgraded to https rather than left scheme-less, so a
// misconfigured build cannot silently send telemetry over plaintext.
func TestTelemetryBaseURLSchemeHandling(t *testing.T) {
	t.Parallel()

	t.Run("empty endpoint is an error", func(t *testing.T) {
		t.Parallel()

		if _, err := telemetryBaseURL(BuildInfo{}); err == nil {
			t.Fatal("expected an error for an empty endpoint")
		}
		if _, err := telemetryBaseURL(BuildInfo{TelemetryEndpoint: "   "}); err == nil {
			t.Fatal("expected an error for a blank endpoint")
		}
	})

	t.Run("scheme-less host is upgraded to https", func(t *testing.T) {
		t.Parallel()

		got, err := telemetryBaseURL(BuildInfo{TelemetryEndpoint: "collector.example.com"})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(got, "https://") {
			t.Fatalf("got %q, want an https URL", got)
		}
	})

	t.Run("explicit scheme is preserved", func(t *testing.T) {
		t.Parallel()

		got, err := telemetryBaseURL(BuildInfo{TelemetryEndpoint: "http://localhost:4318"})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(got, "http://") {
			t.Fatalf("got %q, want the explicit http scheme preserved", got)
		}
	})

	t.Run("unparseable endpoint is an error", func(t *testing.T) {
		t.Parallel()

		if _, err := telemetryBaseURL(BuildInfo{TelemetryEndpoint: "https://exa mple.com/\x7f"}); err == nil {
			t.Fatal("expected a parse error")
		}
	})
}

func TestSetupConfigEnvLookup(t *testing.T) {
	t.Parallel()

	custom := SetupConfig{EnvLookup: func(key string) string { return "custom:" + key }}
	if got := custom.envLookup()("X"); got != "custom:X" {
		t.Fatalf("got %q, want the custom lookup", got)
	}

	// The default has to be a real lookup rather than nil, or Setup would panic.
	var zero SetupConfig
	if zero.envLookup() == nil {
		t.Fatal("default envLookup must not be nil")
	}
}

func TestSetupConfigStderr(t *testing.T) {
	t.Parallel()

	var sink strings.Builder
	custom := SetupConfig{Stderr: &sink}
	if _, err := custom.stderr().Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if sink.String() != "x" {
		t.Fatalf("custom stderr got %q", sink.String())
	}

	// The default discards rather than being nil, so callers can always write.
	var zero SetupConfig
	if _, err := zero.stderr().Write([]byte("x")); err != nil {
		t.Fatalf("default stderr should accept writes: %v", err)
	}
}

// Workflow properties always carry the library and platform identity, and
// caller-supplied keys are merged on top with blank keys dropped.
func TestWorkflowProperties(t *testing.T) {
	t.Parallel()

	m := &Manager{version: "1.2.3", distro: "homebrew"}

	got := m.workflowProperties(map[string]any{
		"custom": "value",
		"":       "dropped",
		"   ":    "also dropped",
	})

	for _, key := range []string{"$lib", "$lib_version", "app_version", "distro", "platform_arch", "platform_os"} {
		if _, ok := got[key]; !ok {
			t.Errorf("missing required property %q", key)
		}
	}
	if got["$lib"] != "vigilante-cli" {
		t.Errorf("$lib = %v", got["$lib"])
	}
	if got["app_version"] != "1.2.3" {
		t.Errorf("app_version = %v", got["app_version"])
	}
	if got["distro"] != "homebrew" {
		t.Errorf("distro = %v", got["distro"])
	}
	if got["custom"] != "value" {
		t.Errorf("custom = %v", got["custom"])
	}
	if _, ok := got[""]; ok {
		t.Error("a blank key must be dropped")
	}
	if len(got) != 7 {
		t.Errorf("got %d properties, want 7 (6 required + 1 custom): %#v", len(got), got)
	}
}

func TestWorkflowPropertiesWithNoCallerProperties(t *testing.T) {
	t.Parallel()

	m := &Manager{version: "v", distro: "d"}
	if got := m.workflowProperties(nil); len(got) != 6 {
		t.Fatalf("got %d properties, want the 6 required ones: %#v", len(got), got)
	}
}

// enqueueAnalytics is called from capture paths that may run before setup, so a
// nil manager or a manager with analytics disabled must be a silent no-op rather
// than a panic.
func TestEnqueueAnalyticsIsSafeWhenDisabled(t *testing.T) {
	t.Parallel()

	var nilManager *Manager
	nilManager.enqueueAnalytics(analyticsEvent{})

	disabled := &Manager{}
	disabled.enqueueAnalytics(analyticsEvent{})
	if len(disabled.events) != 0 {
		t.Fatalf("events = %d, want none queued when analytics is off", len(disabled.events))
	}
}

func TestShutdownTimeoutIsPositive(t *testing.T) {
	t.Parallel()

	if ShutdownTimeout() <= 0 {
		t.Fatal("shutdown timeout must be positive or shutdown would never flush")
	}
}
