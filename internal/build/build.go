// Package build holds the version metadata stamped into the binary at link time
// by GoReleaser. It exists as its own package so that both the CLI and the
// telemetry layer can read build information without importing each other.
package build

import (
	"runtime/debug"
	"strings"
	"sync"
)

var (
	Version           = "dev"
	Distro            = "direct"
	TelemetryEndpoint = ""
	TelemetryToken    = ""
	TelemetryURLPath  = ""
	// Commit is the short VCS revision stamped at link time. When it is empty,
	// CommitHash falls back to the module build information embedded by the Go
	// toolchain so locally built `dev` binaries remain identifiable in logs.
	Commit = ""
)

var (
	commitOnce   sync.Once
	commitCached string
)

// CommitHash returns the short commit identifying this binary, or "unknown".
func CommitHash() string {
	commitOnce.Do(func() {
		commitCached = resolveCommit(Commit, debug.ReadBuildInfo)
	})
	return commitCached
}

func resolveCommit(stamped string, readBuildInfo func() (*debug.BuildInfo, bool)) string {
	if trimmed := strings.TrimSpace(stamped); trimmed != "" {
		return shortCommit(trimmed)
	}
	info, ok := readBuildInfo()
	if !ok || info == nil {
		return "unknown"
	}
	revision, modified := "", false
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			revision = setting.Value
		case "vcs.modified":
			modified = setting.Value == "true"
		}
	}
	if revision == "" {
		return "unknown"
	}
	if modified {
		return shortCommit(revision) + "-dirty"
	}
	return shortCommit(revision)
}

func shortCommit(revision string) string {
	if len(revision) > 12 {
		return revision[:12]
	}
	return revision
}
