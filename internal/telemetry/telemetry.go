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
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

const exportTimeout = 500 * time.Millisecond

type BuildInfo struct {
	Version           string
	Distro            string
	TelemetryEndpoint string
	TelemetryToken    string
}

type SetupConfig struct {
	BuildInfo   BuildInfo
	StateRoot   string
	Stderr      io.Writer
	EnvLookup   func(string) string
	NewExporter func(context.Context, BuildInfo) (sdktrace.SpanExporter, error)
}

type Manager struct {
	provider *sdktrace.TracerProvider
	tracer   trace.Tracer
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

	provider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(
			exp,
			sdktrace.WithBatchTimeout(100*time.Millisecond),
			sdktrace.WithExportTimeout(exportTimeout),
		),
		sdktrace.WithResource(res),
	)

	return &Manager{
		provider: provider,
		tracer:   provider.Tracer("github.com/nicobistolfi/vigilante/internal/telemetry"),
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
	ctx, span := m.tracer.Start(ctx, "cli.command", trace.WithAttributes(
		attribute.String("command.name", commandName),
	))

	return ctx, func(exitCode int) {
		span.SetAttributes(
			attribute.Int("command.exit_code", exitCode),
			attribute.String("command.name", commandName),
			attribute.String("platform.os", runtime.GOOS),
			attribute.String("platform.arch", runtime.GOARCH),
			attribute.String("app.version", m.version),
			attribute.String("distro", m.distro),
		)
		if exitCode != 0 {
			span.SetStatus(codes.Error, fmt.Sprintf("exit code %d", exitCode))
		}
		span.End()
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
	return strings.TrimSpace(info.TelemetryEndpoint) == "" || strings.TrimSpace(info.TelemetryToken) == ""
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

func newHTTPExporter(ctx context.Context, info BuildInfo) (sdktrace.SpanExporter, error) {
	exportCtx, cancel := context.WithTimeout(ctx, exportTimeout)
	defer cancel()

	return otlptracehttp.New(exportCtx,
		otlptracehttp.WithEndpointURL(info.TelemetryEndpoint),
		otlptracehttp.WithHeaders(map[string]string{
			"Authorization": fmt.Sprintf("Bearer %s", info.TelemetryToken),
		}),
		otlptracehttp.WithTimeout(exportTimeout),
	)
}

func defaultString(value string, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
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
