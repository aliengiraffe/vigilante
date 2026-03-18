package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	otellog "go.opentelemetry.io/otel/log"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

const (
	exportTimeout   = 2 * time.Second
	shutdownTimeout = 3 * time.Second
)

type BuildInfo struct {
	Version           string
	Distro            string
	TelemetryEndpoint string
	TelemetryToken    string
	TelemetryURLPath  string
}

type SetupConfig struct {
	BuildInfo            BuildInfo
	StateRoot            string
	Stderr               io.Writer
	EnvLookup            func(string) string
	NewExporter          func(context.Context, BuildInfo) (sdklog.Exporter, error)
	NewAnalyticsExporter func(BuildInfo, string) (analyticsExporter, error)
}

type Manager struct {
	provider  *sdklog.LoggerProvider
	logger    otellog.Logger
	analytics analyticsExporter
	mu        sync.Mutex
	events    []analyticsEvent
	version   string
	distro    string
	anonID    string
}

var (
	defaultManagerMu sync.RWMutex
	defaultManager   *Manager
)

type localState struct {
	AnonymousID string `json:"anonymous_id"`
}

type analyticsEvent struct {
	Type       string         `json:"type"`
	UUID       string         `json:"uuid"`
	Timestamp  time.Time      `json:"timestamp"`
	DistinctID string         `json:"distinct_id"`
	Event      string         `json:"event"`
	Properties map[string]any `json:"properties"`
}

type analyticsBatch struct {
	APIKey   string            `json:"api_key"`
	Messages []json.RawMessage `json:"batch"`
}

type analyticsExporter interface {
	Export(context.Context, []analyticsEvent) error
}

func Setup(ctx context.Context, cfg SetupConfig) (*Manager, error) {
	if telemetryDisabled(cfg.BuildInfo, cfg.envLookup()) {
		return disabledManager(), nil
	}

	state, firstRun, err := ensureLocalState(cfg.StateRoot)
	if err != nil {
		return nil, err
	}
	if firstRun {
		fmt.Fprintln(cfg.stderr(), "vigilante collects anonymous CLI telemetry. Set DO_NOT_TRACK=1 or MYTOOL_NO_ANALYTICS=1 to opt out.")
	}

	res, err := resource.New(
		ctx,
		resource.WithAttributes(
			semconv.ServiceName("vigilante-cli"),
			semconv.ServiceVersion(defaultString(cfg.BuildInfo.Version, "dev")),
			attribute.String("anonymous_id", state.AnonymousID),
			attribute.String("app.version", defaultString(cfg.BuildInfo.Version, "dev")),
			attribute.String("distro", defaultString(cfg.BuildInfo.Distro, "direct")),
			attribute.String("platform.os", runtime.GOOS),
			attribute.String("platform.arch", runtime.GOARCH),
		),
	)
	if err != nil {
		return nil, err
	}

	var (
		provider *sdklog.LoggerProvider
		logger   otellog.Logger
	)
	if telemetryLogsEnabled(cfg.BuildInfo) {
		exporterFactory := cfg.NewExporter
		if exporterFactory == nil {
			exporterFactory = newHTTPExporter
		}
		exp, err := exporterFactory(ctx, cfg.BuildInfo)
		if err != nil {
			return nil, err
		}

		provider = sdklog.NewLoggerProvider(
			sdklog.WithProcessor(
				sdklog.NewBatchProcessor(
					exp,
					sdklog.WithExportInterval(100*time.Millisecond),
					sdklog.WithExportTimeout(exportTimeout),
				),
			),
			sdklog.WithResource(res),
		)
		logger = provider.Logger("github.com/nicobistolfi/vigilante/internal/telemetry")
	}

	analyticsFactory := cfg.NewAnalyticsExporter
	if analyticsFactory == nil {
		analyticsFactory = newHTTPAnalyticsExporter
	}
	analytics, err := analyticsFactory(cfg.BuildInfo, state.AnonymousID)
	if err != nil {
		return nil, err
	}

	return &Manager{
		provider:  provider,
		logger:    logger,
		analytics: analytics,
		version:   defaultString(cfg.BuildInfo.Version, "dev"),
		distro:    defaultString(cfg.BuildInfo.Distro, "direct"),
		anonID:    state.AnonymousID,
	}, nil
}

func (m *Manager) Shutdown(ctx context.Context) error {
	if m == nil {
		return nil
	}

	var shutdownErr error
	if m.provider != nil {
		shutdownErr = errors.Join(shutdownErr, m.provider.Shutdown(ctx))
	}
	if m.analytics != nil {
		m.mu.Lock()
		events := append([]analyticsEvent(nil), m.events...)
		m.events = nil
		m.mu.Unlock()
		shutdownErr = errors.Join(shutdownErr, m.analytics.Export(ctx, events))
	}
	return shutdownErr
}

func (m *Manager) StartCommand(ctx context.Context, args []string) (context.Context, func(int)) {
	if m == nil || (m.provider == nil && m.analytics == nil) {
		return ctx, func(int) {}
	}

	commandName := CommandName(args)
	startedAt := time.Now().UTC()
	m.enqueueAnalytics(analyticsEvent{
		Type:       "capture",
		UUID:       uuid.NewString(),
		Timestamp:  startedAt,
		DistinctID: m.anonID,
		Event:      "cli_command_started",
		Properties: m.commandProperties(commandName),
	})

	return ctx, func(exitCode int) {
		finishedAt := time.Now().UTC()
		durationMs := finishedAt.Sub(startedAt).Milliseconds()
		if m.provider != nil {
			record := otellog.Record{}
			record.SetEventName("cli.command")
			record.SetTimestamp(startedAt)
			record.SetObservedTimestamp(finishedAt)
			record.SetBody(otellog.StringValue("command completed"))
			record.AddAttributes(
				otellog.KeyValueFromAttribute(attribute.Int("command.exit_code", exitCode)),
				otellog.KeyValueFromAttribute(attribute.String("command.name", commandName)),
				otellog.KeyValueFromAttribute(attribute.Int64("command.duration_ms", durationMs)),
				otellog.KeyValueFromAttribute(attribute.String("platform.os", runtime.GOOS)),
				otellog.KeyValueFromAttribute(attribute.String("platform.arch", runtime.GOARCH)),
				otellog.KeyValueFromAttribute(attribute.String("app.version", m.version)),
				otellog.KeyValueFromAttribute(attribute.String("distro", m.distro)),
			)
			if exitCode != 0 {
				record.SetBody(otellog.StringValue("command failed"))
				record.SetSeverity(otellog.SeverityError)
				record.SetSeverityText("ERROR")
			} else {
				record.SetBody(otellog.StringValue("command completed"))
				record.SetSeverity(otellog.SeverityInfo)
				record.SetSeverityText("INFO")
			}
			m.logger.Emit(ctx, record)
		}
		m.enqueueAnalytics(analyticsEvent{
			Type:       "capture",
			UUID:       uuid.NewString(),
			Timestamp:  finishedAt,
			DistinctID: m.anonID,
			Event:      "cli_command_completed",
			Properties: m.commandCompletedProperties(commandName, exitCode, durationMs),
		})
	}
}

func CommandName(args []string) string {
	if len(args) == 0 {
		return "root"
	}

	if isHelpToken(args[0]) || strings.HasPrefix(args[0], "-") {
		return "help"
	}

	command := args[0]
	if len(args) > 1 && expandsCommandGroup(command) && !strings.HasPrefix(args[1], "-") && !isHelpToken(args[1]) {
		return command + " " + args[1]
	}
	return command
}

func expandsCommandGroup(command string) bool {
	switch command {
	case "cleanup", "completion", "daemon", "redispatch", "resume", "service":
		return true
	default:
		return false
	}
}

func isHelpToken(value string) bool {
	return value == "-h" || value == "--help" || value == "help"
}

func telemetryDisabled(info BuildInfo, lookup func(string) string) bool {
	if lookup("DO_NOT_TRACK") == "1" || lookup("MYTOOL_NO_ANALYTICS") == "1" {
		return true
	}
	if strings.EqualFold(lookup("CI"), "true") {
		return true
	}
	return !telemetryAnalyticsEnabled(info)
}

func ensureLocalState(root string) (localState, bool, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return localState{}, false, err
	}

	path := filepath.Join(root, "state.json")
	data, err := os.ReadFile(path)
	if err == nil {
		var state localState
		if err := json.Unmarshal(data, &state); err != nil {
			return localState{}, false, err
		}
		if strings.TrimSpace(state.AnonymousID) != "" {
			return state, false, nil
		}
	} else if !os.IsNotExist(err) {
		return localState{}, false, err
	}

	state := localState{AnonymousID: uuid.NewString()}
	encoded, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return localState{}, false, err
	}
	encoded = append(encoded, '\n')
	if err := os.WriteFile(path, encoded, 0o644); err != nil {
		return localState{}, false, err
	}
	return state, true, nil
}

type exporterSettings struct {
	Endpoint string
	Headers  map[string]string
	Timeout  time.Duration
	URLPath  string
}

func newHTTPExporter(ctx context.Context, info BuildInfo) (sdklog.Exporter, error) {
	exportCtx, cancel := context.WithTimeout(ctx, exportTimeout)
	defer cancel()

	settings := telemetryExporterSettings(info)
	opts := []otlploghttp.Option{
		otlploghttp.WithEndpoint(settings.Endpoint),
		otlploghttp.WithHeaders(settings.Headers),
		otlploghttp.WithTimeout(settings.Timeout),
	}
	if settings.URLPath != "" {
		opts = append(opts, otlploghttp.WithURLPath(settings.URLPath))
	}

	return otlploghttp.New(exportCtx, opts...)
}

func newHTTPAnalyticsExporter(info BuildInfo, _ string) (analyticsExporter, error) {
	baseURL, err := telemetryBaseURL(info)
	if err != nil {
		return nil, err
	}
	return &httpAnalyticsExporter{
		baseURL: baseURL,
		apiKey:  strings.TrimSpace(info.TelemetryToken),
		client: &http.Client{
			Timeout: exportTimeout,
		},
	}, nil
}

func defaultString(value string, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func telemetryURLPath(info BuildInfo) string {
	return strings.TrimSpace(info.TelemetryURLPath)
}

func telemetryBaseURL(info BuildInfo) (string, error) {
	endpoint := strings.TrimSpace(info.TelemetryEndpoint)
	if endpoint == "" {
		return "", errors.New("telemetry endpoint is empty")
	}
	if !strings.Contains(endpoint, "://") {
		endpoint = "https://" + endpoint
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("invalid telemetry endpoint %q", info.TelemetryEndpoint)
	}
	parsed.Path = ""
	parsed.RawPath = ""
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return strings.TrimRight(parsed.String(), "/"), nil
}

func telemetryExporterSettings(info BuildInfo) exporterSettings {
	return exporterSettings{
		Endpoint: strings.TrimSpace(info.TelemetryEndpoint),
		Headers: map[string]string{
			"Authorization": fmt.Sprintf("Bearer %s", strings.TrimSpace(info.TelemetryToken)),
		},
		Timeout: exportTimeout,
		URLPath: telemetryURLPath(info),
	}
}

func telemetryAnalyticsEnabled(info BuildInfo) bool {
	return strings.TrimSpace(info.TelemetryEndpoint) != "" &&
		strings.TrimSpace(info.TelemetryToken) != ""
}

func telemetryLogsEnabled(info BuildInfo) bool {
	return telemetryAnalyticsEnabled(info) && telemetryURLPath(info) != ""
}

func disabledManager() *Manager {
	return &Manager{}
}

func SetDefault(manager *Manager) {
	defaultManagerMu.Lock()
	defer defaultManagerMu.Unlock()
	defaultManager = manager
}

func ShutdownTimeout() time.Duration {
	return shutdownTimeout
}

func (cfg SetupConfig) envLookup() func(string) string {
	if cfg.EnvLookup != nil {
		return cfg.EnvLookup
	}
	return os.Getenv
}

func (cfg SetupConfig) stderr() io.Writer {
	if cfg.Stderr != nil {
		return cfg.Stderr
	}
	return io.Discard
}

func (m *Manager) enqueueAnalytics(event analyticsEvent) {
	if m == nil || m.analytics == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, event)
}

func CaptureWorkflowEvent(event string, properties map[string]any) {
	defaultManagerMu.RLock()
	manager := defaultManager
	defaultManagerMu.RUnlock()
	if manager == nil {
		return
	}
	manager.CaptureWorkflowEvent(event, properties)
}

func (m *Manager) CaptureWorkflowEvent(event string, properties map[string]any) {
	if m == nil || strings.TrimSpace(event) == "" {
		return
	}

	startedAt := time.Now().UTC()
	m.enqueueAnalytics(analyticsEvent{
		Type:       "capture",
		UUID:       uuid.NewString(),
		Timestamp:  startedAt,
		DistinctID: m.anonID,
		Event:      strings.TrimSpace(event),
		Properties: m.workflowProperties(properties),
	})
}

func (m *Manager) commandProperties(commandName string) map[string]any {
	commandGroup := commandGroup(commandName)
	return map[string]any{
		"$lib":          "vigilante-cli",
		"$lib_version":  m.version,
		"app_version":   m.version,
		"command_name":  commandName,
		"command_group": commandGroup,
		"distro":        m.distro,
		"feature_area":  commandFeatureArea(commandGroup),
		"invocation":    "cli",
		"platform_arch": runtime.GOARCH,
		"platform_os":   runtime.GOOS,
	}
}

func (m *Manager) commandCompletedProperties(commandName string, exitCode int, durationMs int64) map[string]any {
	properties := m.commandProperties(commandName)
	properties["duration_ms"] = durationMs
	properties["exit_code"] = exitCode
	properties["result"] = commandResult(exitCode)
	properties["success"] = exitCode == 0
	return properties
}

func (m *Manager) workflowProperties(properties map[string]any) map[string]any {
	out := map[string]any{
		"$lib":          "vigilante-cli",
		"$lib_version":  m.version,
		"app_version":   m.version,
		"distro":        m.distro,
		"platform_arch": runtime.GOARCH,
		"platform_os":   runtime.GOOS,
	}
	for key, value := range properties {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		out[key] = value
	}
	return out
}

func commandGroup(commandName string) string {
	parts := strings.Fields(commandName)
	if len(parts) == 0 {
		return "root"
	}
	return parts[0]
}

func commandFeatureArea(commandGroup string) string {
	switch commandGroup {
	case "setup":
		return "setup"
	case "watch", "unwatch", "list":
		return "watch_management"
	case "status", "service":
		return "service_management"
	case "daemon":
		return "daemon"
	case "cleanup":
		return "cleanup"
	case "resume", "redispatch":
		return "issue_session"
	case "completion", "help", "root":
		return "operator_cli"
	default:
		return "operator_cli"
	}
}

func commandResult(exitCode int) string {
	if exitCode == 0 {
		return "success"
	}
	return "failure"
}

type httpAnalyticsExporter struct {
	baseURL string
	apiKey  string
	client  *http.Client
}

func (e *httpAnalyticsExporter) Export(ctx context.Context, events []analyticsEvent) error {
	if len(events) == 0 {
		return nil
	}

	messages := make([]json.RawMessage, 0, len(events))
	for _, event := range events {
		encoded, err := json.Marshal(event)
		if err != nil {
			return err
		}
		messages = append(messages, encoded)
	}

	payload, err := json.Marshal(analyticsBatch{
		APIKey:   e.apiKey,
		Messages: messages,
	})
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.baseURL+"/batch/", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := e.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return fmt.Errorf("analytics export failed with status %s", resp.Status)
	}
	return fmt.Errorf("analytics export failed with status %s: %s", resp.Status, strings.TrimSpace(string(body)))
}
