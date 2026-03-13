package scripts

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetupDaemonPassesThroughOnLinux(t *testing.T) {
	t.Parallel()

	fixture := newSetupDaemonFixture(t)
	fixture.writeVigilanteScript(t, `#!/bin/sh
printf '%s\n' "$*" >"$TEST_LOG"
exit 0
`)

	result := fixture.run(t, "linux")
	if result.exitCode != 0 {
		t.Fatalf("expected success, got %d with output %s", result.exitCode, result.output)
	}
	if !strings.Contains(result.log, "setup -d") {
		t.Fatalf("expected direct setup invocation, log=%q", result.log)
	}
	if strings.Contains(result.log, "launchctl") {
		t.Fatalf("expected no launchctl cleanup, log=%q", result.log)
	}
}

func TestSetupDaemonRetriesAfterLaunchdCleanup(t *testing.T) {
	t.Parallel()

	fixture := newSetupDaemonFixture(t)
	fixture.touchPlist(t)
	fixture.writeVigilanteScript(t, `#!/bin/sh
count_file="$TMPDIR_BASE/vigilante-count"
count=0
if [ -f "$count_file" ]; then
  count=$(cat "$count_file")
fi
count=$((count + 1))
printf '%s' "$count" >"$count_file"
printf 'vigilante %s %s\n' "$count" "$*" >>"$TEST_LOG"
if [ "$count" -eq 1 ]; then
  exit 137
fi
exit 0
`)
	fixture.writeLaunchctlScript(t, `#!/bin/sh
printf 'launchctl %s\n' "$*" >>"$TEST_LOG"
exit 0
`)

	result := fixture.run(t, "darwin")
	if result.exitCode != 0 {
		t.Fatalf("expected success, got %d with output %s", result.exitCode, result.output)
	}
	if strings.Count(result.log, "vigilante ") != 2 {
		t.Fatalf("expected two setup attempts, log=%q", result.log)
	}
	if !strings.Contains(result.log, "launchctl bootout") || !strings.Contains(result.log, "launchctl remove") {
		t.Fatalf("expected launchctl cleanup, log=%q", result.log)
	}
	if !strings.Contains(result.output, "detected an existing launch agent") || !strings.Contains(result.output, "recovered after cleaning up") {
		t.Fatalf("expected recovery messaging, output=%q", result.output)
	}
}

func TestSetupDaemonFailsWithActionableHintAfterRetry(t *testing.T) {
	t.Parallel()

	fixture := newSetupDaemonFixture(t)
	fixture.touchPlist(t)
	fixture.writeVigilanteScript(t, `#!/bin/sh
printf 'vigilante %s\n' "$*" >>"$TEST_LOG"
exit 137
`)
	fixture.writeLaunchctlScript(t, `#!/bin/sh
printf 'launchctl %s\n' "$*" >>"$TEST_LOG"
exit 0
`)

	result := fixture.run(t, "darwin")
	if result.exitCode != 137 {
		t.Fatalf("expected exit 137, got %d with output %s", result.exitCode, result.output)
	}
	if !strings.Contains(result.output, "automatic recovery failed") {
		t.Fatalf("expected failure summary, output=%q", result.output)
	}
	if !strings.Contains(result.output, "launchctl bootout gui/") || !strings.Contains(result.output, "task setup-daemon") {
		t.Fatalf("expected actionable next step, output=%q", result.output)
	}
	if !strings.Contains(result.output, "refresh process was interrupted or killed") {
		t.Fatalf("expected interrupted hint, output=%q", result.output)
	}
}

type setupDaemonFixture struct {
	root        string
	home        string
	binDir      string
	logPath     string
	installPath string
	plistPath   string
}

type setupDaemonResult struct {
	exitCode int
	output   string
	log      string
}

func newSetupDaemonFixture(t *testing.T) setupDaemonFixture {
	t.Helper()

	root := t.TempDir()
	home := filepath.Join(root, "home")
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}

	return setupDaemonFixture{
		root:        root,
		home:        home,
		binDir:      binDir,
		logPath:     filepath.Join(root, "calls.log"),
		installPath: filepath.Join(binDir, "vigilante"),
		plistPath:   filepath.Join(home, "Library", "LaunchAgents", "com.vigilante.agent.plist"),
	}
}

func (f setupDaemonFixture) touchPlist(t *testing.T) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(f.plistPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.plistPath, []byte("plist"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (f setupDaemonFixture) writeVigilanteScript(t *testing.T, contents string) {
	t.Helper()
	writeExecutable(t, f.installPath, contents)
}

func (f setupDaemonFixture) writeLaunchctlScript(t *testing.T, contents string) {
	t.Helper()
	writeExecutable(t, filepath.Join(f.binDir, "launchctl"), contents)
}

func (f setupDaemonFixture) run(t *testing.T, osName string) setupDaemonResult {
	t.Helper()

	cmd := exec.Command("/bin/sh", "./scripts/setup-daemon.sh")
	cmd.Dir = repoRoot(t)
	cmd.Env = append(os.Environ(),
		"HOME="+f.home,
		"PATH="+f.binDir+":"+os.Getenv("PATH"),
		"TMPDIR_BASE="+f.root,
		"TEST_LOG="+f.logPath,
		"VIGILANTE_INSTALL_PATH="+f.installPath,
		"VIGILANTE_DAEMON_PLIST="+f.plistPath,
		"VIGILANTE_SETUP_DAEMON_OS="+osName,
	)
	output, err := cmd.CombinedOutput()

	exitCode := 0
	if err != nil {
		var exitErr *exec.ExitError
		if ok := errors.As(err, &exitErr); ok {
			exitCode = exitErr.ExitCode()
		} else {
			t.Fatalf("run script: %v", err)
		}
	}

	logData, readErr := os.ReadFile(f.logPath)
	if readErr != nil && !os.IsNotExist(readErr) {
		t.Fatal(readErr)
	}

	return setupDaemonResult{
		exitCode: exitCode,
		output:   string(output),
		log:      string(logData),
	}
}

func writeExecutable(t *testing.T, path string, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o755); err != nil {
		t.Fatal(err)
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()

	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Dir(dir)
}
