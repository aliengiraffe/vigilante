package scripts

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// The regression guard for this script's whole reason to exist: `cp` reuses the
// destination inode, which poisons the kernel's page cache for any binary a live
// process has mapped and makes every later exec fail with SIGKILL. A rename gives
// the destination a new inode instead.
func TestInstallBinaryReplacesDestinationInode(t *testing.T) {
	t.Parallel()

	f := newInstallFixture(t)
	f.writeSource(t, "first")
	if result := f.run(t); result.exitCode != 0 {
		t.Fatalf("first install failed: exit %d\n%s", result.exitCode, result.output)
	}
	firstInode := f.destInode(t)

	f.writeSource(t, "second")
	if result := f.run(t); result.exitCode != 0 {
		t.Fatalf("reinstall failed: exit %d\n%s", result.exitCode, result.output)
	}
	secondInode := f.destInode(t)

	if firstInode == secondInode {
		t.Fatalf("destination kept inode %d across a reinstall; the install is not atomic", firstInode)
	}
	if got := f.destContents(t); got != "second" {
		t.Fatalf("destination content = %q, want %q", got, "second")
	}
}

// A process holding the previous binary must keep running: the rename unlinks the
// old inode but does not disturb anyone who already has it open.
func TestInstallBinaryLeavesPreviousInodeIntactForOpenHandles(t *testing.T) {
	t.Parallel()

	f := newInstallFixture(t)
	f.writeSource(t, "original")
	if result := f.run(t); result.exitCode != 0 {
		t.Fatalf("install failed: exit %d\n%s", result.exitCode, result.output)
	}

	handle, err := os.Open(f.dest)
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Close()

	f.writeSource(t, "replacement")
	if result := f.run(t); result.exitCode != 0 {
		t.Fatalf("reinstall failed: exit %d\n%s", result.exitCode, result.output)
	}

	// Reading through the pre-existing handle reaches the old, now-unlinked
	// inode. `cp` would have rewritten that inode in place and this would come
	// back as "replacement".
	if _, err := handle.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 64)
	n, err := handle.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(buf[:n])); got != "original" {
		t.Fatalf("open handle saw %q, want the original inode's content", got)
	}
}

func TestInstallBinaryCreatesMissingDestinationDirectory(t *testing.T) {
	t.Parallel()

	f := newInstallFixture(t)
	f.dest = filepath.Join(f.tempDir, "nested", "deeper", "vigilante")
	f.writeSource(t, "payload")

	if result := f.run(t); result.exitCode != 0 {
		t.Fatalf("install failed: exit %d\n%s", result.exitCode, result.output)
	}
	if got := f.destContents(t); got != "payload" {
		t.Fatalf("destination content = %q, want %q", got, "payload")
	}
}

func TestInstallBinaryResultIsExecutable(t *testing.T) {
	t.Parallel()

	f := newInstallFixture(t)
	f.writeSource(t, "payload")
	if result := f.run(t); result.exitCode != 0 {
		t.Fatalf("install failed: exit %d\n%s", result.exitCode, result.output)
	}

	info, err := os.Stat(f.dest)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("destination mode %v is not executable", info.Mode().Perm())
	}
}

// A half-written binary must never be observable at the destination, and the
// temp file must not survive the run.
func TestInstallBinaryLeavesNoTemporaryFiles(t *testing.T) {
	t.Parallel()

	f := newInstallFixture(t)
	f.writeSource(t, "payload")
	if result := f.run(t); result.exitCode != 0 {
		t.Fatalf("install failed: exit %d\n%s", result.exitCode, result.output)
	}

	entries, err := os.ReadDir(filepath.Dir(f.dest))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() != filepath.Base(f.dest) {
			t.Fatalf("unexpected leftover %q in the destination directory", entry.Name())
		}
	}
}

func TestInstallBinaryFailsWhenSourceIsMissing(t *testing.T) {
	t.Parallel()

	f := newInstallFixture(t)
	// No source written.
	result := f.run(t)

	if result.exitCode == 0 {
		t.Fatalf("expected failure for a missing source, got exit 0\n%s", result.output)
	}
	if !strings.Contains(result.output, "does not exist") {
		t.Fatalf("expected a missing-source error, got:\n%s", result.output)
	}
	if _, err := os.Stat(f.dest); !os.IsNotExist(err) {
		t.Fatalf("destination must not be created when the source is missing")
	}
}

func TestInstallBinaryRequiresBothArguments(t *testing.T) {
	t.Parallel()

	f := newInstallFixture(t)
	f.writeSource(t, "payload")

	cmd := exec.Command("/bin/bash", "./scripts/install-binary.sh", f.source)
	cmd.Dir = repoRoot(t)
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected failure when the destination is omitted, got success:\n%s", output)
	}
	if !strings.Contains(string(output), "usage:") {
		t.Fatalf("expected a usage message, got:\n%s", output)
	}
}

type installFixture struct {
	tempDir string
	source  string
	dest    string
}

func newInstallFixture(t *testing.T) *installFixture {
	t.Helper()

	tempDir := t.TempDir()
	destDir := filepath.Join(tempDir, "bin")
	if err := os.Mkdir(destDir, 0o755); err != nil {
		t.Fatal(err)
	}
	return &installFixture{
		tempDir: tempDir,
		source:  filepath.Join(tempDir, "vigilante"),
		dest:    filepath.Join(destDir, "vigilante"),
	}
}

func (f *installFixture) writeSource(t *testing.T, content string) {
	t.Helper()

	if err := os.WriteFile(f.source, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

func (f *installFixture) run(t *testing.T) installResult {
	t.Helper()

	cmd := exec.Command("/bin/bash", "./scripts/install-binary.sh", f.source, f.dest)
	cmd.Dir = repoRoot(t)
	output, err := cmd.CombinedOutput()
	if err == nil {
		return installResult{output: string(output)}
	}

	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("run install script: %v\n%s", err, output)
	}
	return installResult{exitCode: exitErr.ExitCode(), output: string(output)}
}

func (f *installFixture) destInode(t *testing.T) uint64 {
	t.Helper()

	info, err := os.Stat(f.dest)
	if err != nil {
		t.Fatal(err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Skip("inode information is unavailable on this platform")
	}
	return uint64(stat.Ino)
}

func (f *installFixture) destContents(t *testing.T) string {
	t.Helper()

	body, err := os.ReadFile(f.dest)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(body))
}

type installResult struct {
	exitCode int
	output   string
}
