// Package build holds the version metadata stamped into the binary at link time
// by GoReleaser. It exists as its own package so that both the CLI and the
// telemetry layer can read build information without importing each other.
package build

var (
	Version           = "dev"
	Distro            = "direct"
	TelemetryEndpoint = ""
	TelemetryToken    = ""
	TelemetryURLPath  = ""
)
