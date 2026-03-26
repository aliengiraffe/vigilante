package scripts

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestVerifyNightlyReleaseSucceedsWhenReleaseIsPublishedAndAssetsAreReachable(t *testing.T) {
	t.Parallel()

	f := newVerifyNightlyFixture(t)
	f.writeTool(t, "gh", `#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "$TEST_TMPDIR/gh.log"
cat "$TEST_TMPDIR/release.json"
`)
	f.writeTool(t, "curl", `#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "$TEST_TMPDIR/curl.log"
`)
	f.writeTool(t, "sleep", `#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "$TEST_TMPDIR/sleep.log"
`)
	f.writeReleaseJSON(t, false, true, f.expectedAssets())

	result := f.run(t)
	if result.exitCode != 0 {
		t.Fatalf("expected success, got exit %d\noutput:\n%s", result.exitCode, result.output)
	}
	for _, asset := range f.expectedAssets() {
		want := fmt.Sprintf("--fail --silent --show-error --location --head https://github.com/aliengiraffe/vigilante/releases/download/main-nightly/%s", asset)
		if !strings.Contains(f.readLog(t, "curl.log"), want) {
			t.Fatalf("missing curl check for %s\n%s", asset, f.readLog(t, "curl.log"))
		}
	}
}

func TestVerifyNightlyReleaseFailsWhenReleaseStaysDraft(t *testing.T) {
	t.Parallel()

	f := newVerifyNightlyFixture(t)
	f.writeTool(t, "gh", `#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "$TEST_TMPDIR/gh.log"
cat "$TEST_TMPDIR/release.json"
`)
	f.writeTool(t, "curl", `#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "$TEST_TMPDIR/curl.log"
`)
	f.writeTool(t, "sleep", `#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "$TEST_TMPDIR/sleep.log"
`)
	f.writeReleaseJSON(t, true, true, f.expectedAssets())
	f.maxAttempts = "2"
	f.sleepSeconds = "0"

	result := f.run(t)
	if result.exitCode == 0 {
		t.Fatalf("expected draft release verification to fail\noutput:\n%s", result.output)
	}
	if strings.Contains(f.readLogMaybe(t, "curl.log"), "https://github.com") {
		t.Fatalf("curl should not run while release is still draft\n%s", f.readLogMaybe(t, "curl.log"))
	}
}

func TestVerifyNightlyReleaseFailsWhenAssetURLIsUnavailable(t *testing.T) {
	t.Parallel()

	f := newVerifyNightlyFixture(t)
	f.writeTool(t, "gh", `#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "$TEST_TMPDIR/gh.log"
cat "$TEST_TMPDIR/release.json"
`)
	f.writeTool(t, "curl", `#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "$TEST_TMPDIR/curl.log"
if [[ "$*" == *"Linux_amd64"* ]]; then
  exit 22
fi
`)
	f.writeTool(t, "sleep", `#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "$TEST_TMPDIR/sleep.log"
`)
	f.writeReleaseJSON(t, false, true, f.expectedAssets())
	f.maxAttempts = "2"
	f.sleepSeconds = "0"

	result := f.run(t)
	if result.exitCode == 0 {
		t.Fatalf("expected unreachable asset verification to fail\noutput:\n%s", result.output)
	}
	if !strings.Contains(result.output, "asset URL not reachable yet") {
		t.Fatalf("expected asset failure in output\n%s", result.output)
	}
}

type verifyNightlyFixture struct {
	tempDir      string
	binDir       string
	maxAttempts  string
	sleepSeconds string
}

func newVerifyNightlyFixture(t *testing.T) verifyNightlyFixture {
	t.Helper()

	tempDir := t.TempDir()
	binDir := filepath.Join(tempDir, "bin")
	if err := os.Mkdir(binDir, 0o755); err != nil {
		t.Fatal(err)
	}

	return verifyNightlyFixture{
		tempDir:      tempDir,
		binDir:       binDir,
		maxAttempts:  "1",
		sleepSeconds: "0",
	}
}

func (f verifyNightlyFixture) writeTool(t *testing.T, name string, body string) {
	t.Helper()

	path := filepath.Join(f.binDir, name)
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func (f verifyNightlyFixture) writeReleaseJSON(t *testing.T, draft bool, prerelease bool, assets []string) {
	t.Helper()

	quoted := make([]string, 0, len(assets))
	for _, asset := range assets {
		quoted = append(quoted, fmt.Sprintf(`{"name":%q}`, asset))
	}
	jsonBody := fmt.Sprintf("{\"draft\":%t,\"prerelease\":%t,\"assets\":[%s]}\n", draft, prerelease, strings.Join(quoted, ","))
	if err := os.WriteFile(filepath.Join(f.tempDir, "release.json"), []byte(jsonBody), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (f verifyNightlyFixture) expectedAssets() []string {
	const version = "0.0.0-nightly.20260326170625.503113fdfe81"
	return []string{
		"vigilante_" + version + "_Linux_amd64.tar.gz",
		"vigilante_" + version + "_macOS_amd64.tar.gz",
		"vigilante_" + version + "_macOS_arm64.tar.gz",
	}
}

func (f verifyNightlyFixture) run(t *testing.T) verifyNightlyResult {
	t.Helper()

	cmd := exec.Command("/bin/bash", "./scripts/verify-nightly-release.sh")
	cmd.Dir = repoRoot(t)
	cmd.Env = append(os.Environ(),
		"GITHUB_REPOSITORY=aliengiraffe/vigilante",
		"NIGHTLY_TAG=main-nightly",
		"NIGHTLY_VERSION=0.0.0-nightly.20260326170625.503113fdfe81",
		"MAX_ATTEMPTS="+f.maxAttempts,
		"SLEEP_SECONDS="+f.sleepSeconds,
		"TEST_TMPDIR="+f.tempDir,
		"PATH="+f.binDir+":"+os.Getenv("PATH"),
	)
	output, err := cmd.CombinedOutput()
	if err == nil {
		return verifyNightlyResult{output: string(output)}
	}

	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("run verify script: %v\n%s", err, output)
	}
	return verifyNightlyResult{exitCode: exitErr.ExitCode(), output: string(output)}
}

func (f verifyNightlyFixture) readLog(t *testing.T, name string) string {
	t.Helper()

	body, err := os.ReadFile(filepath.Join(f.tempDir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func (f verifyNightlyFixture) readLogMaybe(t *testing.T, name string) string {
	t.Helper()

	body, err := os.ReadFile(filepath.Join(f.tempDir, name))
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

type verifyNightlyResult struct {
	exitCode int
	output   string
}
