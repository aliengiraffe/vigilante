package build

import (
	"runtime/debug"
	"testing"
)

func TestResolveCommitPrefersStampedValueAndFallsBackToBuildInfo(t *testing.T) {
	if got := resolveCommit("abcdef1234567890", nil); got != "abcdef123456" {
		t.Fatalf("stamped: %q", got)
	}
	none := func() (*debug.BuildInfo, bool) { return nil, false }
	if got := resolveCommit("", none); got != "unknown" {
		t.Fatalf("no build info: %q", got)
	}
	info := func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "0123456789abcdef"}, {Key: "vcs.modified", Value: "true"}}}, true
	}
	if got := resolveCommit("", info); got != "0123456789ab-dirty" {
		t.Fatalf("build info: %q", got)
	}
	empty := func() (*debug.BuildInfo, bool) { return &debug.BuildInfo{}, true }
	if got := resolveCommit("", empty); got != "unknown" {
		t.Fatalf("empty build info: %q", got)
	}
	if CommitHash() == "" {
		t.Fatal("CommitHash must never be empty")
	}
}
