package main

import (
	"context"
	"os"

	"github.com/nicobistolfi/vigilante/internal/app"
	"github.com/nicobistolfi/vigilante/internal/build"
	"github.com/nicobistolfi/vigilante/internal/state"
	"github.com/nicobistolfi/vigilante/internal/telemetry"
)

type telemetrySession interface {
	StartCommand(context.Context, []string) (context.Context, func(int))
	Shutdown(context.Context) error
}

var cliArgs = func() []string {
	return os.Args[1:]
}

var runCLI = func(ctx context.Context, args []string) int {
	return app.New().Run(ctx, args)
}

var setupTelemetry = func(ctx context.Context) (telemetrySession, error) {
	return telemetry.Setup(ctx, telemetry.SetupConfig{
		BuildInfo: telemetry.BuildInfo{
			Version:           build.Version,
			Distro:            build.Distro,
			TelemetryEndpoint: build.TelemetryEndpoint,
			TelemetryToken:    build.TelemetryToken,
			TelemetryURLPath:  build.TelemetryURLPath,
		},
		StateRoot: state.NewStore().Root(),
		Stderr:    os.Stderr,
	})
}

func main() {
	os.Exit(run())
}

func run() int {
	ctx := context.Background()
	args := cliArgs()
	manager, err := setupTelemetry(ctx)
	if err != nil || manager == nil {
		manager = &telemetry.Manager{}
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), telemetry.ShutdownTimeout())
	defer cancel()
	defer func() {
		_ = manager.Shutdown(shutdownCtx)
	}()

	commandCtx, finish := manager.StartCommand(ctx, args)
	exitCode := runCLI(commandCtx, args)
	finish(exitCode)
	return exitCode
}
