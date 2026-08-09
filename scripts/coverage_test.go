package scripts

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The threshold is a ratchet, so the check that a below-threshold total actually
// fails is the single most important behavior in the script. A ratchet that
// reports and exits 0 is decoration.
func TestCoverageFailsBelowThreshold(t *testing.T) {
	t.Parallel()

	profile := writeProfile(t, map[string][]statement{
		"github.com/nicobistolfi/vigilante/internal/a": {{count: 1, statements: 7}, {count: 0, statements: 3}},
	})

	result := runCoverage(t, "--profile", profile, "--threshold", "80")

	if result.exitCode == 0 {
		t.Fatalf("expected a non-zero exit for 70%% against an 80%% threshold, got 0\n%s", result.output)
	}
	if !strings.Contains(result.output, "below the 80% threshold") {
		t.Fatalf("expected a below-threshold error, got:\n%s", result.output)
	}
}

func TestCoveragePassesAtOrAboveThreshold(t *testing.T) {
	t.Parallel()

	profile := writeProfile(t, map[string][]statement{
		"github.com/nicobistolfi/vigilante/internal/a": {{count: 1, statements: 8}, {count: 0, statements: 2}},
	})

	// Exactly at the threshold must pass: the ratchet is a floor, not a
	// strict inequality, or committing the measured baseline would fail
	// immediately on the very run that measured it.
	result := runCoverage(t, "--profile", profile, "--threshold", "80")

	if result.exitCode != 0 {
		t.Fatalf("expected exit 0 for 80%% against an 80%% threshold, got %d\n%s", result.exitCode, result.output)
	}
	if !strings.Contains(result.output, "meets the committed threshold") {
		t.Fatalf("expected a success line, got:\n%s", result.output)
	}
}

// Coverage is statements covered over statements total, aggregated per directory.
// Getting this wrong by, say, averaging per-file percentages would let a tiny
// fully-covered file mask a large uncovered one.
func TestCoverageAggregatesStatementsPerPackage(t *testing.T) {
	t.Parallel()

	profile := writeProfile(t, map[string][]statement{
		"github.com/nicobistolfi/vigilante/internal/covered": {{count: 1, statements: 10}},
		"github.com/nicobistolfi/vigilante/internal/partial": {{count: 1, statements: 1}, {count: 0, statements: 9}},
	})

	result := runCoverage(t, "--profile", profile, "--threshold", "0")

	if result.exitCode != 0 {
		t.Fatalf("coverage script failed: exit %d\n%s", result.exitCode, result.output)
	}
	assertPackagePercent(t, result.output, "internal/covered", 100.0)
	assertPackagePercent(t, result.output, "internal/partial", 10.0)
	// 11 covered of 20 total, not the 55% an average of 100% and 10% would give.
	assertTotalPercent(t, result.output, 55.0)
}

// Worst-first ordering is why the table is worth printing at all: it is the
// working list for closing the coverage gap.
func TestCoverageListsPackagesWorstFirst(t *testing.T) {
	t.Parallel()

	profile := writeProfile(t, map[string][]statement{
		"github.com/nicobistolfi/vigilante/internal/high":   {{count: 1, statements: 9}, {count: 0, statements: 1}},
		"github.com/nicobistolfi/vigilante/internal/low":    {{count: 1, statements: 1}, {count: 0, statements: 9}},
		"github.com/nicobistolfi/vigilante/internal/middle": {{count: 1, statements: 5}, {count: 0, statements: 5}},
	})

	result := runCoverage(t, "--profile", profile, "--threshold", "0")

	if result.exitCode != 0 {
		t.Fatalf("coverage script failed: exit %d\n%s", result.exitCode, result.output)
	}

	lowIndex := strings.Index(result.output, "internal/low")
	middleIndex := strings.Index(result.output, "internal/middle")
	highIndex := strings.Index(result.output, "internal/high")
	if lowIndex < 0 || middleIndex < 0 || highIndex < 0 {
		t.Fatalf("expected all three packages in the table, got:\n%s", result.output)
	}
	if !(lowIndex < middleIndex && middleIndex < highIndex) {
		t.Fatalf("expected ascending coverage order low < middle < high, got:\n%s", result.output)
	}
}

// The threshold file is the single source of truth. If the script silently fell
// back to some built-in default when it could not read the file, CI and a local
// run could enforce different numbers without anyone noticing.
func TestCoverageReadsCommittedThresholdIgnoringComments(t *testing.T) {
	t.Parallel()

	body, err := os.ReadFile(filepath.Join(repoRoot(t), "scripts", "coverage-threshold"))
	if err != nil {
		t.Fatal(err)
	}

	var value string
	for _, line := range strings.Split(string(body), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if value != "" {
			t.Fatalf("threshold file must contain exactly one value, found another: %q", trimmed)
		}
		value = trimmed
	}
	if value == "" {
		t.Fatal("threshold file contains no value")
	}
	committed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		t.Fatalf("committed threshold %q is not a number: %v", value, err)
	}

	// A profile sitting just under the committed threshold must fail, which
	// proves the script parsed the file rather than defaulting to zero.
	profile := writeProfile(t, map[string][]statement{
		"github.com/nicobistolfi/vigilante/internal/a": {{count: 0, statements: 1000}},
	})
	result := runCoverage(t, "--profile", profile)

	if result.exitCode == 0 {
		t.Fatalf("expected 0%% coverage to fail the committed %.1f%% threshold\n%s", committed, result.output)
	}
	if !strings.Contains(result.output, value) {
		t.Fatalf("expected the committed threshold %q in the output, got:\n%s", value, result.output)
	}
}

func TestCoverageFailsOnMissingProfile(t *testing.T) {
	t.Parallel()

	result := runCoverage(t, "--profile", filepath.Join(t.TempDir(), "absent.out"), "--threshold", "0")

	if result.exitCode == 0 {
		t.Fatalf("expected failure for a missing profile, got exit 0\n%s", result.output)
	}
	if !strings.Contains(result.output, "does not exist") {
		t.Fatalf("expected a missing-profile error, got:\n%s", result.output)
	}
}

// An empty or header-only profile means the measurement did not happen. Treating
// it as 0% would fail confusingly, and treating it as 100% would be a silent
// hole in the gate, so it is its own error.
func TestCoverageFailsOnProfileWithoutStatements(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "coverage.out")
	if err := os.WriteFile(path, []byte("mode: atomic\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	result := runCoverage(t, "--profile", path, "--threshold", "0")

	if result.exitCode == 0 {
		t.Fatalf("expected failure for a profile with no statements, got exit 0\n%s", result.output)
	}
	if !strings.Contains(result.output, "no statements") {
		t.Fatalf("expected a no-statements error, got:\n%s", result.output)
	}
}

func TestCoverageRejectsNonNumericThreshold(t *testing.T) {
	t.Parallel()

	profile := writeProfile(t, map[string][]statement{
		"github.com/nicobistolfi/vigilante/internal/a": {{count: 1, statements: 1}},
	})

	result := runCoverage(t, "--profile", profile, "--threshold", "eighty")

	if result.exitCode == 0 {
		t.Fatalf("expected failure for a non-numeric threshold, got exit 0\n%s", result.output)
	}
	if !strings.Contains(result.output, "must be a number") {
		t.Fatalf("expected a threshold validation error, got:\n%s", result.output)
	}
}

func TestCoverageRejectsUnknownArguments(t *testing.T) {
	t.Parallel()

	result := runCoverage(t, "--nope")

	if result.exitCode == 0 {
		t.Fatalf("expected failure for an unknown argument, got exit 0\n%s", result.output)
	}
	if !strings.Contains(result.output, "unknown argument") {
		t.Fatalf("expected an unknown-argument error, got:\n%s", result.output)
	}
}

// CI must not be able to drift from `task coverage`. Both have to reach the same
// script, so assert the wiring rather than trusting it.
func TestCoverageScriptIsTheOnlyCoverageCommand(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	for _, rel := range []string{"Taskfile.yml", filepath.Join(".github", "workflows", "ci.yml")} {
		body, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(body), "scripts/coverage.sh") {
			t.Errorf("%s must invoke scripts/coverage.sh so the coverage command has one definition", rel)
		}
		if strings.Contains(string(body), "-coverprofile") {
			t.Errorf("%s defines its own -coverprofile command; coverage must go through scripts/coverage.sh", rel)
		}
	}
}

type statement struct {
	count      int
	statements int
}

// writeProfile builds a synthetic Go coverage profile so the reporting and
// threshold logic can be exercised without a real `go test ./...` run.
func writeProfile(t *testing.T, packages map[string][]statement) string {
	t.Helper()

	var b strings.Builder
	b.WriteString("mode: atomic\n")
	line := 1
	for pkg, statements := range packages {
		for i, s := range statements {
			b.WriteString(fmt.Sprintf("%s/file%d.go:%d.1,%d.2 %d %d\n", pkg, i, line, line+1, s.statements, s.count))
			line += 2
		}
	}

	path := filepath.Join(t.TempDir(), "coverage.out")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func runCoverage(t *testing.T, args ...string) coverageResult {
	t.Helper()

	cmd := exec.Command("/bin/bash", append([]string{"./scripts/coverage.sh"}, args...)...)
	cmd.Dir = repoRoot(t)
	output, err := cmd.CombinedOutput()
	if err == nil {
		return coverageResult{output: string(output)}
	}

	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("run coverage script: %v\n%s", err, output)
	}
	return coverageResult{exitCode: exitErr.ExitCode(), output: string(output)}
}

func assertPackagePercent(t *testing.T, output string, pkgSuffix string, want float64) {
	t.Helper()

	pattern := regexp.MustCompile(`(?m)^\s*([0-9.]+)%\s+\S*` + regexp.QuoteMeta(pkgSuffix) + `\s*$`)
	match := pattern.FindStringSubmatch(output)
	if match == nil {
		t.Fatalf("no coverage line for %q in:\n%s", pkgSuffix, output)
	}
	got, err := strconv.ParseFloat(match[1], 64)
	if err != nil {
		t.Fatalf("unparseable percentage %q: %v", match[1], err)
	}
	if got != want {
		t.Fatalf("%s coverage = %.1f%%, want %.1f%%", pkgSuffix, got, want)
	}
}

func assertTotalPercent(t *testing.T, output string, want float64) {
	t.Helper()

	pattern := regexp.MustCompile(`(?m)^TOTAL:\s+([0-9.]+)%`)
	match := pattern.FindStringSubmatch(output)
	if match == nil {
		t.Fatalf("no TOTAL line in:\n%s", output)
	}
	got, err := strconv.ParseFloat(match[1], 64)
	if err != nil {
		t.Fatalf("unparseable total %q: %v", match[1], err)
	}
	if got != want {
		t.Fatalf("total coverage = %.1f%%, want %.1f%%", got, want)
	}
}

type coverageResult struct {
	exitCode int
	output   string
}
