package scripts

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestSignMacOSBinarySkipsNonDarwinTarget(t *testing.T) {
	t.Parallel()

	f := newSignFixture(t)
	result := f.run(t, "linux")

	if result.exitCode != 0 {
		t.Fatalf("expected success, got exit %d\noutput:\n%s", result.exitCode, result.output)
	}
	if !strings.Contains(result.output, "targets linux, not darwin") {
		t.Fatalf("expected the linux skip notice, got:\n%s", result.output)
	}
	if log := f.readLogMaybe(t, "codesign.log"); log != "" {
		t.Fatalf("codesign must not run for a linux target, got:\n%s", log)
	}
}

func TestSignMacOSBinarySkipsNonDarwinRunner(t *testing.T) {
	t.Parallel()

	f := newSignFixture(t)
	f.unameSystem = "Linux"
	result := f.run(t, "")

	if result.exitCode != 0 {
		t.Fatalf("expected success, got exit %d\noutput:\n%s", result.exitCode, result.output)
	}
	if !strings.Contains(result.output, "Skipping the sign + execute gate") {
		t.Fatalf("expected the runner skip notice, got:\n%s", result.output)
	}
	if log := f.readLogMaybe(t, "codesign.log"); log != "" {
		t.Fatalf("codesign must not run on a Linux runner, got:\n%s", log)
	}
}

// The build hook also fires for the linux artifact, which GoReleaser may hand
// over without a usable GOOS. Detecting a non-Mach-O file is the backstop.
func TestSignMacOSBinarySkipsNonMachOInput(t *testing.T) {
	t.Parallel()

	f := newSignFixture(t)
	f.lipoArchs = ""
	f.lipoExitCode = 1
	result := f.run(t, "")

	if result.exitCode != 0 {
		t.Fatalf("expected success, got exit %d\noutput:\n%s", result.exitCode, result.output)
	}
	if !strings.Contains(result.output, "is not a Mach-O binary") {
		t.Fatalf("expected the Mach-O skip notice, got:\n%s", result.output)
	}
	if log := f.readLogMaybe(t, "codesign.log"); log != "" {
		t.Fatalf("codesign must not run for a non-Mach-O input, got:\n%s", log)
	}
}

func TestSignMacOSBinaryFailsWhenBinaryIsMissing(t *testing.T) {
	t.Parallel()

	f := newSignFixture(t)
	f.binaryPath = filepath.Join(f.tempDir, "absent")
	f.skipBinary = true
	result := f.run(t, "darwin")

	if result.exitCode == 0 {
		t.Fatalf("expected failure for a missing binary, got exit 0\noutput:\n%s", result.output)
	}
	if !strings.Contains(result.output, "does not exist") {
		t.Fatalf("expected a missing-binary error, got:\n%s", result.output)
	}
}

func TestSignMacOSBinaryAdHocSignsWithoutIdentityAndSkipsNotarization(t *testing.T) {
	t.Parallel()

	f := newSignFixture(t)
	// Notary credentials are present, but an ad-hoc artifact must never be
	// submitted to Apple.
	f.notaryEnv = true
	result := f.run(t, "darwin")

	if result.exitCode != 0 {
		t.Fatalf("expected success, got exit %d\noutput:\n%s", result.exitCode, result.output)
	}
	if !strings.Contains(result.output, "signing AD-HOC") {
		t.Fatalf("expected the ad-hoc notice, got:\n%s", result.output)
	}

	codesignLog := f.readLog(t, "codesign.log")
	if !strings.Contains(codesignLog, "--force --sign - --identifier vigilante --options runtime") {
		t.Fatalf("expected an ad-hoc codesign invocation, got:\n%s", codesignLog)
	}
	if log := f.readLogMaybe(t, "xcrun.log"); log != "" {
		t.Fatalf("an ad-hoc binary must not be notarized, got:\n%s", log)
	}
}

func TestSignMacOSBinarySignsWithDeveloperIDAndHardenedRuntime(t *testing.T) {
	t.Parallel()

	f := newSignFixture(t)
	f.identity = "Developer ID Application: Example (TEAMID)"
	result := f.run(t, "darwin")

	if result.exitCode != 0 {
		t.Fatalf("expected success, got exit %d\noutput:\n%s", result.exitCode, result.output)
	}

	codesignLog := f.readLog(t, "codesign.log")
	for _, want := range []string{
		"--sign Developer ID Application: Example (TEAMID)",
		"--identifier vigilante",
		"--options runtime",
		"--timestamp",
		"--verify --strict",
	} {
		if !strings.Contains(codesignLog, want) {
			t.Fatalf("missing %q in codesign invocations:\n%s", want, codesignLog)
		}
	}
	if strings.Contains(codesignLog, "--sign -") {
		t.Fatalf("must not fall back to ad-hoc signing when an identity is set:\n%s", codesignLog)
	}
}

func TestSignMacOSBinaryDiscoversIdentityFromKeychain(t *testing.T) {
	t.Parallel()

	f := newSignFixture(t)
	f.keychain = filepath.Join(f.tempDir, "signing.keychain-db")
	f.keychainIdentity = "ABC123DEF456"
	result := f.run(t, "darwin")

	if result.exitCode != 0 {
		t.Fatalf("expected success, got exit %d\noutput:\n%s", result.exitCode, result.output)
	}
	if !strings.Contains(result.output, "discovered Developer ID Application identity ABC123DEF456") {
		t.Fatalf("expected keychain identity discovery, got:\n%s", result.output)
	}

	codesignLog := f.readLog(t, "codesign.log")
	if !strings.Contains(codesignLog, "--sign ABC123DEF456") {
		t.Fatalf("expected the discovered identity to be used:\n%s", codesignLog)
	}
	if !strings.Contains(codesignLog, "--keychain "+f.keychain) {
		t.Fatalf("expected the throwaway keychain to be passed to codesign:\n%s", codesignLog)
	}
}

// The execution gate is the whole point of the script: a binary can carry a
// signature that every `codesign -d` field reports as healthy and still be
// refused by the kernel.
func TestSignMacOSBinaryFailsWhenExecutionGateFails(t *testing.T) {
	t.Parallel()

	f := newSignFixture(t)
	f.binaryExitCode = 137
	f.notaryEnv = true
	f.identity = "Developer ID Application: Example (TEAMID)"
	result := f.run(t, "darwin")

	if result.exitCode == 0 {
		t.Fatalf("expected failure when the binary cannot run, got exit 0\noutput:\n%s", result.output)
	}
	if !strings.Contains(result.output, "EXECUTION GATE FAILED") {
		t.Fatalf("expected an execution gate error, got:\n%s", result.output)
	}
	if !strings.Contains(result.output, "exit 137 = SIGKILL") {
		t.Fatalf("expected SIGKILL triage guidance, got:\n%s", result.output)
	}
	if log := f.readLogMaybe(t, "xcrun.log"); log != "" {
		t.Fatalf("a binary that failed the gate must not be notarized, got:\n%s", log)
	}
}

func TestSignMacOSBinaryFailsWhenVerificationFails(t *testing.T) {
	t.Parallel()

	f := newSignFixture(t)
	f.identity = "Developer ID Application: Example (TEAMID)"
	f.verifyExitCode = 3
	result := f.run(t, "darwin")

	if result.exitCode == 0 {
		t.Fatalf("expected failure when verification fails, got exit 0\noutput:\n%s", result.output)
	}
	if !strings.Contains(result.output, "codesign --verify --strict rejected") {
		t.Fatalf("expected a verification error, got:\n%s", result.output)
	}
	if !strings.Contains(result.output, "certificate 1[field.1.2.840.113635.100.6.2.6]") {
		t.Fatalf("expected the canonical designated requirement hint, got:\n%s", result.output)
	}
}

func TestSignMacOSBinaryRunsTheGateWithoutTelemetry(t *testing.T) {
	t.Parallel()

	f := newSignFixture(t)
	result := f.run(t, "darwin")

	if result.exitCode != 0 {
		t.Fatalf("expected success, got exit %d\noutput:\n%s", result.exitCode, result.output)
	}

	binaryLog := f.readLog(t, "binary.log")
	if !strings.Contains(binaryLog, "args=--help") {
		t.Fatalf("expected the gate to invoke --help, got:\n%s", binaryLog)
	}
	if !strings.Contains(binaryLog, "do_not_track=1") {
		t.Fatalf("expected the gate to suppress telemetry, got:\n%s", binaryLog)
	}
}

func TestSignMacOSBinaryNotarizesWhenCredentialsArePresent(t *testing.T) {
	t.Parallel()

	f := newSignFixture(t)
	f.identity = "Developer ID Application: Example (TEAMID)"
	f.notaryEnv = true
	result := f.run(t, "darwin")

	if result.exitCode != 0 {
		t.Fatalf("expected success, got exit %d\noutput:\n%s", result.exitCode, result.output)
	}

	xcrunLog := f.readLog(t, "xcrun.log")
	for _, want := range []string{
		"notarytool submit",
		"--key " + filepath.Join(f.tempDir, "notary.p8"),
		"--key-id NOTARYKEYID",
		"--issuer NOTARYISSUERID",
		"--wait",
		"--timeout 30m",
	} {
		if !strings.Contains(xcrunLog, want) {
			t.Fatalf("missing %q in notarytool invocation:\n%s", want, xcrunLog)
		}
	}
	if !strings.Contains(f.readLog(t, "ditto.log"), "-c -k --keepParent") {
		t.Fatalf("expected the binary to be zipped for notarytool:\n%s", f.readLog(t, "ditto.log"))
	}

	// The gate must run again on the final shipped artifact.
	if !strings.Contains(result.output, "post-notarization re-check") {
		t.Fatalf("expected a post-notarization gate re-check, got:\n%s", result.output)
	}
	if got := strings.Count(f.readLog(t, "binary.log"), "args=--help"); got != 2 {
		t.Fatalf("expected the gate to run twice (pre and post notarization), ran %d times", got)
	}
}

func TestSignMacOSBinarySkipsNotarizationWhenOptedOut(t *testing.T) {
	t.Parallel()

	f := newSignFixture(t)
	f.identity = "Developer ID Application: Example (TEAMID)"
	f.notaryEnv = true
	f.skipNotarize = "true"
	result := f.run(t, "darwin")

	if result.exitCode != 0 {
		t.Fatalf("expected success, got exit %d\noutput:\n%s", result.exitCode, result.output)
	}
	if !strings.Contains(result.output, "VIGILANTE_SKIP_NOTARIZE is set") {
		t.Fatalf("expected the opt-out notice, got:\n%s", result.output)
	}
	if log := f.readLogMaybe(t, "xcrun.log"); log != "" {
		t.Fatalf("notarization must be skipped when opted out, got:\n%s", log)
	}
}

// macos-latest is arm64. The amd64 artifact still gets signed and verified, but
// the script must say plainly that it was not executed instead of implying the
// gate covered it.
func TestSignMacOSBinaryReportsUncoveredExecutionForForeignArch(t *testing.T) {
	t.Parallel()

	f := newSignFixture(t)
	f.lipoArchs = "x86_64"
	f.unameMachine = "arm64"
	f.rosettaAvailable = false
	result := f.run(t, "darwin")

	if result.exitCode != 0 {
		t.Fatalf("expected success, got exit %d\noutput:\n%s", result.exitCode, result.output)
	}
	if !strings.Contains(result.output, "NOT COVERED") {
		t.Fatalf("expected an uncovered-execution notice, got:\n%s", result.output)
	}
	if !strings.Contains(result.output, "execution not covered on this runner") {
		t.Fatalf("expected the summary to admit the gate did not run, got:\n%s", result.output)
	}
	if log := f.readLogMaybe(t, "binary.log"); log != "" {
		t.Fatalf("the foreign-arch binary must not be executed, got:\n%s", log)
	}
	if !strings.Contains(f.readLog(t, "codesign.log"), "--verify --strict") {
		t.Fatalf("the foreign-arch binary must still be verified:\n%s", f.readLog(t, "codesign.log"))
	}
}

func TestSignMacOSBinaryRunsForeignArchUnderRosetta(t *testing.T) {
	t.Parallel()

	f := newSignFixture(t)
	f.lipoArchs = "x86_64"
	f.unameMachine = "arm64"
	f.rosettaAvailable = true
	result := f.run(t, "darwin")

	if result.exitCode != 0 {
		t.Fatalf("expected success, got exit %d\noutput:\n%s", result.exitCode, result.output)
	}
	if !strings.Contains(result.output, "under Rosetta") {
		t.Fatalf("expected the Rosetta gate to run, got:\n%s", result.output)
	}
	if !strings.Contains(f.readLog(t, "binary.log"), "args=--help") {
		t.Fatalf("expected the binary to be executed under Rosetta:\n%s", f.readLog(t, "binary.log"))
	}
}

type signFixture struct {
	tempDir          string
	binDir           string
	binaryPath       string
	binaryExitCode   int
	identity         string
	keychain         string
	keychainIdentity string
	lipoArchs        string
	lipoExitCode     int
	verifyExitCode   int
	unameSystem      string
	unameMachine     string
	rosettaAvailable bool
	notaryEnv        bool
	skipNotarize     string
	skipBinary       bool
}

func newSignFixture(t *testing.T) *signFixture {
	t.Helper()

	tempDir := t.TempDir()
	binDir := filepath.Join(tempDir, "bin")
	if err := os.Mkdir(binDir, 0o755); err != nil {
		t.Fatal(err)
	}

	f := &signFixture{
		tempDir:      tempDir,
		binDir:       binDir,
		binaryPath:   filepath.Join(tempDir, "vigilante"),
		lipoArchs:    "arm64",
		unameSystem:  "Darwin",
		unameMachine: "arm64",
	}
	if err := os.WriteFile(filepath.Join(tempDir, "notary.p8"), []byte("notary-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *signFixture) writeTool(t *testing.T, name string, body string) {
	t.Helper()

	path := filepath.Join(f.binDir, name)
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

// writeStubs installs the macOS toolchain the script depends on. Stubbing
// `uname` too keeps these tests runnable on the Linux CI runner.
func (f *signFixture) writeStubs(t *testing.T) {
	t.Helper()

	f.writeTool(t, "uname", `#!/usr/bin/env bash
set -euo pipefail
case "${1:-}" in
  -m) printf '%s\n' "$STUB_UNAME_MACHINE" ;;
  *) printf '%s\n' "$STUB_UNAME_SYSTEM" ;;
esac
`)

	f.writeTool(t, "lipo", `#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "$TEST_TMPDIR/lipo.log"
if [ "$STUB_LIPO_EXIT" -ne 0 ]; then
  exit "$STUB_LIPO_EXIT"
fi
printf '%s\n' "$STUB_LIPO_ARCHS"
`)

	f.writeTool(t, "codesign", `#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "$TEST_TMPDIR/codesign.log"
case "$*" in
  *--verify*) exit "$STUB_VERIFY_EXIT" ;;
  *--display*) printf 'Identifier=vigilante\n' ;;
esac
`)

	f.writeTool(t, "security", `#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "$TEST_TMPDIR/security.log"
if [ -n "$STUB_KEYCHAIN_IDENTITY" ]; then
  printf '  1) %s "Developer ID Application: Example (TEAMID)"\n' "$STUB_KEYCHAIN_IDENTITY"
fi
`)

	f.writeTool(t, "xcrun", `#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "$TEST_TMPDIR/xcrun.log"
`)

	f.writeTool(t, "ditto", `#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "$TEST_TMPDIR/ditto.log"
: > "${!#}"
`)

	f.writeTool(t, "arch", `#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "$TEST_TMPDIR/arch.log"
exit "$STUB_ROSETTA_EXIT"
`)
}

// writeBinary installs the artifact under test: a stub that records how the
// execution gate invoked it, including whether telemetry was suppressed.
func (f *signFixture) writeBinary(t *testing.T) {
	t.Helper()

	body := `#!/usr/bin/env bash
set -euo pipefail
printf 'args=%s do_not_track=%s\n' "$*" "${DO_NOT_TRACK:-unset}" >> "$TEST_TMPDIR/binary.log"
exit ` + strconv.Itoa(f.binaryExitCode) + `
`
	if err := os.WriteFile(f.binaryPath, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func (f *signFixture) run(t *testing.T, goos string) signResult {
	t.Helper()

	f.writeStubs(t)
	// The missing-binary test deliberately leaves the artifact uncreated.
	if !f.skipBinary {
		f.writeBinary(t)
	}

	args := []string{"./scripts/sign-macos-binary.sh", f.binaryPath}
	if goos != "" {
		args = append(args, goos)
	}

	rosettaExit := "1"
	if f.rosettaAvailable {
		rosettaExit = "0"
	}

	cmd := exec.Command("/bin/bash", args...)
	cmd.Dir = repoRoot(t)
	cmd.Env = append(os.Environ(),
		"TEST_TMPDIR="+f.tempDir,
		"PATH="+f.binDir+":"+os.Getenv("PATH"),
		"STUB_UNAME_SYSTEM="+f.unameSystem,
		"STUB_UNAME_MACHINE="+f.unameMachine,
		"STUB_LIPO_ARCHS="+f.lipoArchs,
		"STUB_LIPO_EXIT="+strconv.Itoa(f.lipoExitCode),
		"STUB_VERIFY_EXIT="+strconv.Itoa(f.verifyExitCode),
		"STUB_KEYCHAIN_IDENTITY="+f.keychainIdentity,
		"STUB_ROSETTA_EXIT="+rosettaExit,
		"APPLE_DEVELOPER_ID_IDENTITY="+f.identity,
		"VIGILANTE_SIGN_KEYCHAIN="+f.keychain,
		"VIGILANTE_SKIP_NOTARIZE="+f.skipNotarize,
	)
	if f.notaryEnv {
		cmd.Env = append(cmd.Env,
			"APPLE_NOTARY_KEY_P8="+filepath.Join(f.tempDir, "notary.p8"),
			"APPLE_NOTARY_KEY_ID=NOTARYKEYID",
			"APPLE_NOTARY_ISSUER_ID=NOTARYISSUERID",
		)
	}

	output, err := cmd.CombinedOutput()
	if err == nil {
		return signResult{output: string(output)}
	}

	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("run sign script: %v\n%s", err, output)
	}
	return signResult{exitCode: exitErr.ExitCode(), output: string(output)}
}

func (f *signFixture) readLog(t *testing.T, name string) string {
	t.Helper()

	body, err := os.ReadFile(filepath.Join(f.tempDir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func (f *signFixture) readLogMaybe(t *testing.T, name string) string {
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

type signResult struct {
	exitCode int
	output   string
}
