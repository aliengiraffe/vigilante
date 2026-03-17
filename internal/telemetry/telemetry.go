package telemetry

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	otellog "go.opentelemetry.io/otel/log"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

const exportTimeout = 500 * time.Millisecond

type BuildInfo struct {
	Version           string
	Distro            string
	TelemetryEndpoint string
	TelemetryToken    string
	TelemetryURLPath  string
}

type SetupConfig struct {
	BuildInfo   BuildInfo
	StateRoot   string
	Stderr      io.Writer
	EnvLookup   func(string) string
	NewExporter func(context.Context, BuildInfo) (sdklog.Exporter, error)
}

type Manager struct {
	provider *sdklog.LoggerProvider
	logger   otellog.Logger
	version  string
	distro   string
}

type localState struct {
	AnonymousID string `json:"anonymous_id"`
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

	exporterFactory := cfg.NewExporter
	if exporterFactory == nil {
		exporterFactory = newHTTPExporter
	}
	exp, err := exporterFactory(ctx, cfg.BuildInfo)
	if err != nil {
		return nil, err
	}

	provider := sdklog.NewLoggerProvider(
		sdklog.WithProcessor(
			sdklog.NewBatchProcessor(
				exp,
				sdklog.WithExportInterval(100*time.Millisecond),
				sdklog.WithExportTimeout(exportTimeout),
			),
		),
		sdklog.WithResource(res),
	)

	return &Manager{
		provider: provider,
		logger:   provider.Logger("github.com/nicobistolfi/vigilante/internal/telemetry"),
		version:  defaultString(cfg.BuildInfo.Version, "dev"),
		distro:   defaultString(cfg.BuildInfo.Distro, "direct"),
	}, nil
}

func (m *Manager) Shutdown(ctx context.Context) error {
	if m == nil || m.provider == nil {
		return nil
	}
	return m.provider.Shutdown(ctx)
}

func (m *Manager) StartCommand(ctx context.Context, args []string) (context.Context, func(int)) {
	if m == nil || m.provider == nil {
		return ctx, func(int) {}
	}

	commandName := CommandName(args)
	startedAt := time.Now()

	return ctx, func(exitCode int) {
		record := otellog.Record{}
		record.SetEventName("cli.command")
		record.SetTimestamp(startedAt)
		record.SetObservedTimestamp(time.Now())
		record.SetBody(otellog.StringValue("command completed"))
		record.AddAttributes(
			otellog.KeyValueFromAttribute(attribute.Int("command.exit_code", exitCode)),
			otellog.KeyValueFromAttribute(attribute.String("command.name", commandName)),
			otellog.KeyValueFromAttribute(attribute.Int64("command.duration_ms", time.Since(startedAt).Milliseconds())),
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
	return strings.TrimSpace(info.TelemetryEndpoint) == "" ||
		strings.TrimSpace(info.TelemetryToken) == "" ||
		telemetryURLPath(info) == ""
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

func defaultString(value string, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func telemetryURLPath(info BuildInfo) string {
	return strings.TrimSpace(info.TelemetryURLPath)
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

func disabledManager() *Manager {
	return &Manager{}
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
