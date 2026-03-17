package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/nicobistolfi/vigilante/internal/app"
	"github.com/nicobistolfi/vigilante/internal/build"
	"github.com/nicobistolfi/vigilante/internal/state"
	"github.com/nicobistolfi/vigilante/internal/telemetry"
)

func main() {
	os.Exit(run())
}

func run() int {
	ctx := context.Background()
	manager, err := telemetry.Setup(ctx, telemetry.SetupConfig{
		BuildInfo: telemetry.BuildInfo{
			Version:           build.Version,
			Distro:            build.Distro,
			TelemetryEndpoint: build.TelemetryEndpoint,
			TelemetryToken:    build.TelemetryToken,
		},
		StateRoot: state.NewStore().Root(),
		Stderr:    os.Stderr,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "warning: telemetry disabled:", err)
		manager = &telemetry.Manager{}
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	defer func() {
		if err := manager.Shutdown(shutdownCtx); err != nil {
			fmt.Fprintln(os.Stderr, "warning: telemetry shutdown failed:", err)
		}
	}()

	commandCtx, finish := manager.StartCommand(ctx, os.Args[1:])
	exitCode := app.New().Run(commandCtx, os.Args[1:])
	finish(exitCode)
	return exitCode
}
