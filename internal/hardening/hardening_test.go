package hardening

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeRunner struct {
	outputs map[string]string
	errors  map[string]error
}

func (f fakeRunner) Run(_ context.Context, _ string, name string, args ...string) (string, error) {
	cmd := strings.TrimSpace(name + " " + strings.Join(args, " "))
	if err, ok := f.errors[cmd]; ok {
		out := f.outputs[cmd]
		return out, err
	}
	if output, ok := f.outputs[cmd]; ok {
		return output, nil
	}
	return "", nil
}

func (f fakeRunner) LookPath(file string) (string, error) {
	return file, nil
}

func TestRunNoLockfile(t *testing.T) {
	dir := t.TempDir()
	writePackageJSON(t, dir, "package.json", `{"name":"test","dependencies":{"lodash":"^4.0.0"}}`)

	runner := fakeRunner{}
	result := Run(context.Background(), runner, dir, []string{"package.json"})

	if result.LockfilePresent {
		t.Error("expected lockfile to be absent")
	}
	if !result.HasFindings() {
		t.Fatal("expected findings for missing lockfile")
	}

	foundLockfileCheck := false
	for _, f := range result.Findings {
		if f.Check == "lockfile-present" {
			foundLockfileCheck = true
			if f.Severity != SeverityHigh {
				t.Errorf("lockfile check severity = %s, want high", f.Severity)
			}
		}
	}
	if !foundLockfileCheck {
		t.Error("missing lockfile-present finding")
	}
}

func TestRunWithLockfileNoVulnerabilities(t *testing.T) {
	dir := t.TempDir()
	writePackageJSON(t, dir, "package.json", `{"name":"test","dependencies":{"lodash":"4.17.21"}}`)
	writeFile(t, dir, "package-lock.json", `{}`)

	runner := fakeRunner{
		outputs: map[string]string{
			"npm audit --json": `{"vulnerabilities":{}}`,
		},
	}
	result := Run(context.Background(), runner, dir, []string{"package.json"})

	if !result.LockfilePresent {
		t.Error("expected lockfile to be present")
	}
	if result.PackageManager != "npm" {
		t.Errorf("package manager = %q, want npm", result.PackageManager)
	}
	if !result.AuditRan {
		t.Error("expected audit to have run")
	}

	// With exact version and no vulnerabilities, only CI findings may remain.
	for _, f := range result.Findings {
		if f.Check == "lockfile-present" || f.Check == "npm-audit-vulnerabilities" || f.Check == "non-exact-ranges" {
			t.Errorf("unexpected finding: %s", f.Check)
		}
	}
}

func TestRunWithVulnerabilities(t *testing.T) {
	dir := t.TempDir()
	writePackageJSON(t, dir, "package.json", `{"name":"test","dependencies":{"lodash":"^4.0.0"}}`)
	writeFile(t, dir, "package-lock.json", `{}`)

	auditOutput := `{"vulnerabilities":{"lodash":{"name":"lodash","severity":"high","title":"Prototype Pollution"}}}`
	runner := fakeRunner{
		outputs: map[string]string{
			"npm audit --json": auditOutput,
		},
		errors: map[string]error{
			"npm audit --json": errExitCode1,
		},
	}
	result := Run(context.Background(), runner, dir, []string{"package.json"})

	if !result.AuditRan {
		t.Error("expected audit to have run despite exit code 1")
	}

	foundAudit := false
	for _, f := range result.Findings {
		if f.Check == "npm-audit-vulnerabilities" {
			foundAudit = true
			if f.Severity != SeverityHigh {
				t.Errorf("audit finding severity = %s, want high", f.Severity)
			}
			if !strings.Contains(f.Message, "lodash") {
				t.Errorf("audit message missing lodash: %s", f.Message)
			}
		}
	}
	if !foundAudit {
		t.Error("missing npm-audit-vulnerabilities finding")
	}
}

func TestRunNonExactRanges(t *testing.T) {
	dir := t.TempDir()
	writePackageJSON(t, dir, "package.json", `{
		"name":"test",
		"dependencies":{"lodash":"^4.0.0","express":"~4.18.0"},
		"devDependencies":{"jest":">=29.0.0"}
	}`)
	writeFile(t, dir, "package-lock.json", `{}`)

	runner := fakeRunner{
		outputs: map[string]string{
			"npm audit --json": `{"vulnerabilities":{}}`,
		},
	}
	result := Run(context.Background(), runner, dir, []string{"package.json"})

	foundNonExact := 0
	for _, f := range result.Findings {
		if f.Check == "non-exact-ranges" {
			foundNonExact++
		}
	}
	if foundNonExact == 0 {
		t.Error("expected non-exact-ranges findings")
	}
}

func TestRunExactVersionsOnly(t *testing.T) {
	dir := t.TempDir()
	writePackageJSON(t, dir, "package.json", `{
		"name":"test",
		"dependencies":{"lodash":"4.17.21","express":"4.18.2"}
	}`)
	writeFile(t, dir, "package-lock.json", `{}`)

	runner := fakeRunner{
		outputs: map[string]string{
			"npm audit --json": `{"vulnerabilities":{}}`,
		},
	}
	result := Run(context.Background(), runner, dir, []string{"package.json"})

	for _, f := range result.Findings {
		if f.Check == "non-exact-ranges" {
			t.Errorf("unexpected non-exact-ranges finding: %s", f.Message)
		}
	}
}

func TestRunPnpmLockfile(t *testing.T) {
	dir := t.TempDir()
	writePackageJSON(t, dir, "package.json", `{"name":"test","dependencies":{"lodash":"4.17.21"}}`)
	writeFile(t, dir, "pnpm-lock.yaml", "lockfileVersion: '6.0'")

	runner := fakeRunner{}
	result := Run(context.Background(), runner, dir, []string{"package.json"})

	if !result.LockfilePresent {
		t.Error("expected pnpm lockfile to be detected")
	}
	if result.PackageManager != "pnpm" {
		t.Errorf("package manager = %q, want pnpm", result.PackageManager)
	}
	// npm audit should not run for pnpm repos.
	if result.AuditRan {
		t.Error("npm audit should not run for pnpm repos")
	}
}

func TestRunYarnLockfile(t *testing.T) {
	dir := t.TempDir()
	writePackageJSON(t, dir, "package.json", `{"name":"test","dependencies":{"lodash":"4.17.21"}}`)
	writeFile(t, dir, "yarn.lock", "# yarn lockfile v1")

	runner := fakeRunner{}
	result := Run(context.Background(), runner, dir, []string{"package.json"})

	if !result.LockfilePresent {
		t.Error("expected yarn lockfile to be detected")
	}
	if result.PackageManager != "yarn" {
		t.Errorf("package manager = %q, want yarn", result.PackageManager)
	}
}

func TestRunSubdirectoryPackageJSON(t *testing.T) {
	dir := t.TempDir()
	subDir := filepath.Join(dir, "packages", "core")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writePackageJSON(t, dir, "packages/core/package.json", `{"name":"core","dependencies":{"lodash":"^4.0.0"}}`)
	writeFile(t, dir, "package-lock.json", `{}`)

	runner := fakeRunner{
		outputs: map[string]string{
			"npm audit --json": `{"vulnerabilities":{}}`,
		},
	}
	result := Run(context.Background(), runner, dir, []string{"packages/core/package.json"})

	foundNonExact := false
	for _, f := range result.Findings {
		if f.Check == "non-exact-ranges" {
			foundNonExact = true
			if !strings.Contains(f.Message, "packages/core/package.json") {
				t.Errorf("finding message should reference subdir path: %s", f.Message)
			}
		}
	}
	if !foundNonExact {
		t.Error("expected non-exact-ranges finding for subdirectory package.json")
	}
}

func TestRunNoPackageJSONPaths(t *testing.T) {
	dir := t.TempDir()
	runner := fakeRunner{}
	result := Run(context.Background(), runner, dir, nil)

	// No package.json paths means no findings from package checks,
	// but lockfile check still runs.
	for _, f := range result.Findings {
		if f.Check == "non-exact-ranges" || f.Check == "npm-audit-vulnerabilities" {
			t.Errorf("unexpected finding when no package.json paths: %s", f.Check)
		}
	}
}

func TestRunCIDeterministicInstallDetection(t *testing.T) {
	dir := t.TempDir()
	writePackageJSON(t, dir, "package.json", `{"name":"test"}`)
	writeFile(t, dir, "package-lock.json", `{}`)

	workflowDir := filepath.Join(dir, ".github", "workflows")
	if err := os.MkdirAll(workflowDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, ".github/workflows/ci.yml", `
name: CI
on: push
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - run: npm install
`)

	runner := fakeRunner{
		outputs: map[string]string{
			"npm audit --json": `{"vulnerabilities":{}}`,
		},
	}
	result := Run(context.Background(), runner, dir, []string{"package.json"})

	foundDet := false
	for _, f := range result.Findings {
		if f.Check == "ci-deterministic-install" {
			foundDet = true
		}
	}
	if !foundDet {
		t.Error("expected ci-deterministic-install finding when CI uses npm install instead of npm ci")
	}
}

func TestRunCIDeterministicInstallPresent(t *testing.T) {
	dir := t.TempDir()
	writePackageJSON(t, dir, "package.json", `{"name":"test"}`)
	writeFile(t, dir, "package-lock.json", `{}`)

	workflowDir := filepath.Join(dir, ".github", "workflows")
	if err := os.MkdirAll(workflowDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, ".github/workflows/ci.yml", `
name: CI
on: push
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - run: npm ci
      - run: npm audit
`)

	runner := fakeRunner{
		outputs: map[string]string{
			"npm audit --json": `{"vulnerabilities":{}}`,
		},
	}
	result := Run(context.Background(), runner, dir, []string{"package.json"})

	for _, f := range result.Findings {
		if f.Check == "ci-deterministic-install" {
			t.Error("should not flag ci-deterministic-install when npm ci is present")
		}
		if f.Check == "ci-audit-step" {
			t.Error("should not flag ci-audit-step when npm audit is present")
		}
	}
}

func TestRunNPMAuditError(t *testing.T) {
	dir := t.TempDir()
	writePackageJSON(t, dir, "package.json", `{"name":"test"}`)
	writeFile(t, dir, "package-lock.json", `{}`)

	runner := fakeRunner{
		outputs: map[string]string{},
		errors: map[string]error{
			"npm audit --json": errExitCode1,
		},
	}
	result := Run(context.Background(), runner, dir, []string{"package.json"})

	if result.AuditRan {
		t.Error("audit should not have run successfully when output is empty")
	}

	foundAuditError := false
	for _, f := range result.Findings {
		if f.Check == "npm-audit-error" {
			foundAuditError = true
		}
	}
	if !foundAuditError {
		t.Error("expected npm-audit-error finding")
	}
}

func TestIsNonExactRange(t *testing.T) {
	tests := []struct {
		version string
		want    bool
	}{
		{"^1.0.0", true},
		{"~1.0.0", true},
		{">1.0.0", true},
		{"<1.0.0", true},
		{">=1.0.0", true},
		{"<=1.0.0", true},
		{"1.0.0 || 2.0.0", true},
		{"*", true},
		{"1.x", true},
		{"1.0.0", false},
		{"0.0.0", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := isNonExactRange(tt.version); got != tt.want {
			t.Errorf("isNonExactRange(%q) = %v, want %v", tt.version, got, tt.want)
		}
	}
}

func TestResultHighestSeverity(t *testing.T) {
	tests := []struct {
		findings []Finding
		want     Severity
	}{
		{nil, SeverityInfo},
		{[]Finding{{Severity: SeverityLow}}, SeverityLow},
		{[]Finding{{Severity: SeverityLow}, {Severity: SeverityHigh}}, SeverityHigh},
		{[]Finding{{Severity: SeverityMedium}}, SeverityMedium},
	}
	for _, tt := range tests {
		result := Result{Findings: tt.findings}
		if got := result.HighestSeverity(); got != tt.want {
			t.Errorf("HighestSeverity() = %s, want %s", got, tt.want)
		}
	}
}

func TestDetectLockfileAndManager(t *testing.T) {
	t.Run("npm", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "package-lock.json", "{}")
		lockfile, pm := detectLockfileAndManager(dir)
		if lockfile != "package-lock.json" || pm != "npm" {
			t.Errorf("got (%q, %q), want (package-lock.json, npm)", lockfile, pm)
		}
	})
	t.Run("pnpm", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "pnpm-lock.yaml", "")
		lockfile, pm := detectLockfileAndManager(dir)
		if lockfile != "pnpm-lock.yaml" || pm != "pnpm" {
			t.Errorf("got (%q, %q), want (pnpm-lock.yaml, pnpm)", lockfile, pm)
		}
	})
	t.Run("yarn", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "yarn.lock", "")
		lockfile, pm := detectLockfileAndManager(dir)
		if lockfile != "yarn.lock" || pm != "yarn" {
			t.Errorf("got (%q, %q), want (yarn.lock, yarn)", lockfile, pm)
		}
	})
	t.Run("none_with_package_json", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "package.json", "{}")
		lockfile, pm := detectLockfileAndManager(dir)
		if lockfile != "" || pm != "npm" {
			t.Errorf("got (%q, %q), want ('', npm)", lockfile, pm)
		}
	})
	t.Run("empty", func(t *testing.T) {
		dir := t.TempDir()
		lockfile, pm := detectLockfileAndManager(dir)
		if lockfile != "" || pm != "" {
			t.Errorf("got (%q, %q), want ('', '')", lockfile, pm)
		}
	})
}

func TestExtractPackageJSONPathsFromDiff(t *testing.T) {
	tests := []struct {
		name       string
		diffOutput string
		diffErr    error
		want       int
		wantErr    bool
	}{
		{
			name:       "root package.json",
			diffOutput: "package.json\nsrc/index.ts\n",
			want:       1,
		},
		{
			name:       "nested package.json files",
			diffOutput: "packages/core/package.json\npackages/utils/package.json\nREADME.md\n",
			want:       2,
		},
		{
			name:       "no package.json",
			diffOutput: "src/index.ts\nREADME.md\n",
			want:       0,
		},
		{
			name:       "empty diff",
			diffOutput: "",
			want:       0,
		},
		{
			name:       "case insensitive",
			diffOutput: "Package.JSON\n",
			want:       1,
		},
		{
			name:       "not a package.json suffix",
			diffOutput: "notpackage.json\n",
			want:       0,
		},
		{
			name:    "git diff error",
			diffErr: &exitError{code: 128},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := fakeRunner{
				outputs: map[string]string{
					"git diff --name-only origin/main...HEAD": tt.diffOutput,
				},
			}
			if tt.diffErr != nil {
				runner.errors = map[string]error{
					"git diff --name-only origin/main...HEAD": tt.diffErr,
				}
			}
			paths, err := ExtractPackageJSONPathsFromDiff(context.Background(), runner, "/fake/path", "main")
			if tt.wantErr {
				if err == nil {
					t.Error("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(paths) != tt.want {
				t.Errorf("got %d paths, want %d", len(paths), tt.want)
			}
		})
	}
}

func TestExtractPackageJSONPathsFromDiffDefaultBranch(t *testing.T) {
	runner := fakeRunner{
		outputs: map[string]string{
			"git diff --name-only origin/main...HEAD": "package.json\n",
		},
	}
	paths, err := ExtractPackageJSONPathsFromDiff(context.Background(), runner, "/fake/path", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(paths) != 1 {
		t.Errorf("got %d paths, want 1 (should default to main)", len(paths))
	}
}

// errExitCode1 simulates npm audit returning exit code 1 for vulnerabilities.
var errExitCode1 = &exitError{code: 1}

type exitError struct {
	code int
}

func (e *exitError) Error() string {
	return "exit status 1"
}

func writePackageJSON(t *testing.T, dir string, relPath string, content string) {
	t.Helper()
	writeFile(t, dir, relPath, content)
}

func writeFile(t *testing.T, dir string, relPath string, content string) {
	t.Helper()
	absPath := filepath.Join(dir, relPath)
	if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(absPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
