// Package hardening provides deterministic JS/TS package manifest checks
// for pull requests that modify package.json files. All checks are code-driven
// and do not rely on LLM analysis.
package hardening

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/nicobistolfi/vigilante/internal/environment"
)

// Severity indicates the severity of a hardening finding.
type Severity string

const (
	SeverityHigh   Severity = "high"
	SeverityMedium Severity = "medium"
	SeverityLow    Severity = "low"
	SeverityInfo   Severity = "info"
)

// Finding represents a single deterministic hardening check result.
type Finding struct {
	Check       string   `json:"check"`
	Severity    Severity `json:"severity"`
	Message     string   `json:"message"`
	Remediation string   `json:"remediation,omitempty"`
}

// Result contains the aggregated output of a package hardening scan.
type Result struct {
	Findings        []Finding `json:"findings"`
	LockfilePresent bool      `json:"lockfile_present"`
	PackageManager  string    `json:"package_manager"`
	AuditRan        bool      `json:"audit_ran"`
	AuditAvailable  bool      `json:"audit_available"`
}

// HasFindings returns true when the scan produced at least one finding.
func (r Result) HasFindings() bool {
	return len(r.Findings) > 0
}

// HighestSeverity returns the highest severity among all findings.
func (r Result) HighestSeverity() Severity {
	order := map[Severity]int{SeverityHigh: 3, SeverityMedium: 2, SeverityLow: 1, SeverityInfo: 0}
	best := SeverityInfo
	for _, f := range r.Findings {
		if order[f.Severity] > order[best] {
			best = f.Severity
		}
	}
	return best
}

// packageJSON is a minimal representation of a package.json file for
// deterministic dependency analysis.
type packageJSON struct {
	Name            string            `json:"name"`
	Dependencies    map[string]string `json:"dependencies"`
	DevDependencies map[string]string `json:"devDependencies"`
	Overrides       map[string]string `json:"overrides"`
	Scripts         map[string]string `json:"scripts"`
}

// npmAuditOutput is a minimal representation of npm audit --json output.
type npmAuditOutput struct {
	Vulnerabilities map[string]npmAuditVuln `json:"vulnerabilities"`
}

type npmAuditVuln struct {
	Name     string `json:"name"`
	Severity string `json:"severity"`
	Title    string `json:"title"`
	URL      string `json:"url"`
	Range    string `json:"range"`
	Via      json.RawMessage
}

// Run performs deterministic package hardening checks on the given worktree
// path. It examines package.json files at the listed paths relative to the
// worktree root and runs lockfile and audit checks.
func Run(ctx context.Context, runner environment.Runner, worktreePath string, changedPackageJSONPaths []string) Result {
	result := Result{}

	lockfile, pm := detectLockfileAndManager(worktreePath)
	result.LockfilePresent = lockfile != ""
	result.PackageManager = pm

	if !result.LockfilePresent {
		result.Findings = append(result.Findings, Finding{
			Check:       "lockfile-present",
			Severity:    SeverityHigh,
			Message:     "No lockfile found (package-lock.json, pnpm-lock.yaml, or yarn.lock). Dependency resolution is non-deterministic.",
			Remediation: "Run `npm install`, `pnpm install`, or `yarn install` to generate a lockfile and commit it.",
		})
	}

	for _, relPath := range changedPackageJSONPaths {
		absPath := filepath.Join(worktreePath, relPath)
		checkPackageJSON(absPath, relPath, &result)
	}

	if pm == "npm" && result.LockfilePresent {
		runNPMAudit(ctx, runner, worktreePath, &result)
	} else if pm == "npm" && !result.LockfilePresent {
		result.AuditAvailable = true
		result.AuditRan = false
		result.Findings = append(result.Findings, Finding{
			Check:       "npm-audit-skipped",
			Severity:    SeverityMedium,
			Message:     "npm audit was skipped because no lockfile is present. Audit results require a lockfile for deterministic resolution.",
			Remediation: "Generate a lockfile first, then re-run the package hardening scan.",
		})
	}

	checkCIAuditPath(worktreePath, pm, &result)

	sort.Slice(result.Findings, func(i, j int) bool {
		order := map[Severity]int{SeverityHigh: 3, SeverityMedium: 2, SeverityLow: 1, SeverityInfo: 0}
		return order[result.Findings[i].Severity] > order[result.Findings[j].Severity]
	})

	return result
}

// detectLockfileAndManager returns the lockfile path (relative to root) and
// the inferred package manager. Returns empty strings if no lockfile is found.
func detectLockfileAndManager(worktreePath string) (string, string) {
	candidates := []struct {
		file    string
		manager string
	}{
		{"package-lock.json", "npm"},
		{"pnpm-lock.yaml", "pnpm"},
		{"yarn.lock", "yarn"},
	}
	for _, c := range candidates {
		if _, err := os.Stat(filepath.Join(worktreePath, c.file)); err == nil {
			return c.file, c.manager
		}
	}
	if _, err := os.Stat(filepath.Join(worktreePath, "package.json")); err == nil {
		return "", "npm"
	}
	return "", ""
}

func checkPackageJSON(absPath string, relPath string, result *Result) {
	data, err := os.ReadFile(absPath)
	if err != nil {
		return
	}
	var pkg packageJSON
	if err := json.Unmarshal(data, &pkg); err != nil {
		result.Findings = append(result.Findings, Finding{
			Check:    "package-json-parse",
			Severity: SeverityMedium,
			Message:  fmt.Sprintf("Failed to parse %s: %s", relPath, err.Error()),
		})
		return
	}

	checkNonExactRanges(pkg.Dependencies, relPath, "dependencies", result)
	checkNonExactRanges(pkg.DevDependencies, relPath, "devDependencies", result)
}

// checkNonExactRanges flags dependency ranges that are not pinned to exact
// versions in the package.json. While lockfiles typically pin transitive
// dependencies, non-exact ranges in the manifest mean different lockfile
// regenerations can resolve to different versions.
func checkNonExactRanges(deps map[string]string, relPath string, section string, result *Result) {
	var loose []string
	for name, version := range deps {
		version = strings.TrimSpace(version)
		if version == "" {
			continue
		}
		if isNonExactRange(version) {
			loose = append(loose, fmt.Sprintf("%s@%s", name, version))
		}
	}
	if len(loose) == 0 {
		return
	}
	sort.Strings(loose)
	truncated := loose
	if len(truncated) > 10 {
		truncated = truncated[:10]
	}
	result.Findings = append(result.Findings, Finding{
		Check:       "non-exact-ranges",
		Severity:    SeverityLow,
		Message:     fmt.Sprintf("%s in %s.%s contains %d non-exact dependency ranges: %s", relPath, relPath, section, len(loose), strings.Join(truncated, ", ")),
		Remediation: "Consider using exact versions or ensure the lockfile is committed and CI uses deterministic installs (npm ci, pnpm install --frozen-lockfile, yarn install --immutable).",
	})
}

// isNonExactRange returns true when a version specifier is not an exact
// pinned version. Ranges starting with ^, ~, >, <, or containing || or *
// are considered non-exact.
func isNonExactRange(version string) bool {
	if version == "" {
		return false
	}
	first := version[0]
	if first == '^' || first == '~' || first == '>' || first == '<' {
		return true
	}
	if strings.Contains(version, "||") || strings.Contains(version, "*") || strings.Contains(version, "x") {
		return true
	}
	if strings.HasPrefix(version, ">=") || strings.HasPrefix(version, "<=") {
		return true
	}
	return false
}

func runNPMAudit(ctx context.Context, runner environment.Runner, worktreePath string, result *Result) {
	result.AuditAvailable = true
	output, err := runner.Run(ctx, worktreePath, "npm", "audit", "--json")
	// npm audit returns exit code 1 when vulnerabilities are found,
	// which is not an error for our purposes.
	if err != nil && strings.TrimSpace(output) == "" {
		result.AuditRan = false
		result.Findings = append(result.Findings, Finding{
			Check:    "npm-audit-error",
			Severity: SeverityMedium,
			Message:  fmt.Sprintf("npm audit failed to execute: %s", summarizeError(err)),
		})
		return
	}
	result.AuditRan = true

	var audit npmAuditOutput
	if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &audit); err != nil {
		result.Findings = append(result.Findings, Finding{
			Check:    "npm-audit-parse",
			Severity: SeverityMedium,
			Message:  fmt.Sprintf("Failed to parse npm audit output: %s", err.Error()),
		})
		return
	}

	if len(audit.Vulnerabilities) == 0 {
		return
	}

	var vulns []string
	for name, v := range audit.Vulnerabilities {
		vulns = append(vulns, fmt.Sprintf("%s (%s)", name, v.Severity))
	}
	sort.Strings(vulns)
	truncated := vulns
	if len(truncated) > 15 {
		truncated = truncated[:15]
	}

	severity := SeverityMedium
	for _, v := range audit.Vulnerabilities {
		if strings.EqualFold(v.Severity, "critical") || strings.EqualFold(v.Severity, "high") {
			severity = SeverityHigh
			break
		}
	}

	result.Findings = append(result.Findings, Finding{
		Check:       "npm-audit-vulnerabilities",
		Severity:    severity,
		Message:     fmt.Sprintf("npm audit found %d known vulnerabilities: %s", len(audit.Vulnerabilities), strings.Join(truncated, ", ")),
		Remediation: "Run `npm audit fix` to apply compatible patches, or use `overrides` in package.json to patch vulnerable transitive dependencies when upstream maintainers have not yet released fixes.",
	})
}

func checkCIAuditPath(worktreePath string, pm string, result *Result) {
	ciDirs := []string{
		filepath.Join(worktreePath, ".github", "workflows"),
		filepath.Join(worktreePath, ".circleci"),
		filepath.Join(worktreePath, ".gitlab-ci.yml"),
	}

	hasCIConfig := false
	for _, dir := range ciDirs {
		if info, err := os.Stat(dir); err == nil {
			if info.IsDir() || !info.IsDir() {
				hasCIConfig = true
				break
			}
		}
	}

	if !hasCIConfig {
		return
	}

	// Check GitHub Actions workflows for deterministic install and audit commands.
	workflowDir := filepath.Join(worktreePath, ".github", "workflows")
	entries, err := os.ReadDir(workflowDir)
	if err != nil {
		return
	}

	hasDetInstall := false
	hasAuditStep := false
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := strings.ToLower(entry.Name())
		if !strings.HasSuffix(name, ".yml") && !strings.HasSuffix(name, ".yaml") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(workflowDir, entry.Name()))
		if err != nil {
			continue
		}
		content := string(data)
		lower := strings.ToLower(content)

		switch pm {
		case "npm":
			if strings.Contains(lower, "npm ci") || strings.Contains(lower, "npm install --frozen-lockfile") {
				hasDetInstall = true
			}
			if strings.Contains(lower, "npm audit") {
				hasAuditStep = true
			}
		case "pnpm":
			if strings.Contains(lower, "pnpm install --frozen-lockfile") {
				hasDetInstall = true
			}
			if strings.Contains(lower, "pnpm audit") {
				hasAuditStep = true
			}
		case "yarn":
			if strings.Contains(lower, "yarn install --immutable") || strings.Contains(lower, "yarn install --frozen-lockfile") {
				hasDetInstall = true
			}
			if strings.Contains(lower, "yarn audit") || strings.Contains(lower, "yarn npm audit") {
				hasAuditStep = true
			}
		}
	}

	if !hasDetInstall {
		var cmd string
		switch pm {
		case "npm":
			cmd = "`npm ci`"
		case "pnpm":
			cmd = "`pnpm install --frozen-lockfile`"
		case "yarn":
			cmd = "`yarn install --immutable`"
		default:
			cmd = "a deterministic install command"
		}
		result.Findings = append(result.Findings, Finding{
			Check:       "ci-deterministic-install",
			Severity:    SeverityMedium,
			Message:     fmt.Sprintf("No deterministic install command (%s) detected in CI workflows. Builds may resolve different dependency versions.", cmd),
			Remediation: fmt.Sprintf("Add %s to your CI workflow to ensure reproducible builds.", cmd),
		})
	}

	if !hasAuditStep {
		result.Findings = append(result.Findings, Finding{
			Check:       "ci-audit-step",
			Severity:    SeverityLow,
			Message:     "No dependency audit step detected in CI workflows.",
			Remediation: fmt.Sprintf("Add `%s audit` to your CI pipeline to catch known vulnerabilities on every PR.", pm),
		})
	}
}

// ExtractPackageJSONPathsFromDiff uses git diff to find package.json files
// that were changed in the worktree branch relative to the base branch.
// This avoids querying the GitHub API for PR file lists.
func ExtractPackageJSONPathsFromDiff(ctx context.Context, runner environment.Runner, worktreePath string, baseBranch string) ([]string, error) {
	if baseBranch == "" {
		baseBranch = "main"
	}
	output, err := runner.Run(ctx, worktreePath, "git", "diff", "--name-only", "origin/"+baseBranch+"...HEAD")
	if err != nil {
		return nil, fmt.Errorf("git diff failed: %w", err)
	}
	var paths []string
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		base := filepath.Base(line)
		if strings.EqualFold(base, "package.json") {
			paths = append(paths, line)
		}
	}
	return paths, nil
}

func summarizeError(err error) string {
	if err == nil {
		return ""
	}
	text := strings.TrimSpace(err.Error())
	if len(text) > 200 {
		return text[:200]
	}
	return text
}
