package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpdateNightlyCaskIncludesMacOSPostflightRemediation(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	checksumsPath := filepath.Join(dir, "checksums.txt")
	outputPath := filepath.Join(dir, "vigilante-nightly.rb")

	const version = "0.0.0-nightly.20260319173306.bd5c4a0696cc"
	checksums := strings.Join([]string{
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa  vigilante_" + version + "_macOS_amd64.tar.gz",
		"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb  vigilante_" + version + "_macOS_arm64.tar.gz",
		"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc  vigilante_" + version + "_Linux_amd64.tar.gz",
		"",
	}, "\n")
	if err := os.WriteFile(checksumsPath, []byte(checksums), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("/bin/bash", "./scripts/update-nightly-cask.sh")
	cmd.Dir = repoRoot(t)
	cmd.Env = append(os.Environ(),
		"CHECKSUMS_FILE="+checksumsPath,
		"NIGHTLY_TAG=main-nightly",
		"NIGHTLY_VERSION="+version,
		"OUTPUT_FILE="+outputPath,
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generate cask: %v\n%s", err, output)
	}

	generated, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	body := string(generated)

	for _, want := range []string{
		"postflight do",
		`system_command "/usr/bin/xattr",`,
		`args:         ["-dr", "com.apple.provenance", staged_path.to_s],`,
		`args:         ["-dr", "com.apple.quarantine", staged_path.to_s],`,
		`system_command "/usr/bin/codesign",`,
		`args:         ["--force", "--sign", "-", "#{staged_path}/vigilante"],`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("generated cask missing %q\n%s", want, body)
		}
	}
}
