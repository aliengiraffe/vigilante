package skill

import (
	"slices"
	"strings"

	"github.com/nicobistolfi/vigilante/internal/repo"
)

func securityGuidanceForClassification(classification repo.Classification) string {
	var sections []string
	if slices.Contains(classification.TechStacks, repo.TechStackNodeJS) {
		sections = append(sections, nodeJSSecurityGuidance(classification))
	}
	if slices.Contains(classification.TechStacks, repo.TechStackGo) {
		sections = append(sections, goSecurityGuidance(classification))
	}
	if slices.Contains(classification.TechStacks, repo.TechStackGitHubActions) {
		sections = append(sections, githubActionsSecurityGuidance())
	}
	return strings.Join(sections, "\n")
}

func nodeJSSecurityGuidance(classification repo.Classification) string {
	sections := []string{
		"JS/TS/Node security guidance for this repository (apply where relevant to touched code and workflow — do not broaden issue scope):",
	}

	sections = append(sections, dependencySupplyChainGuidance(classification)...)
	sections = append(sections, packageManagerGuidance(classification)...)
	sections = append(sections, runtimeSecureCodingGuidance()...)
	if len(classification.ProcessHints.TypeScriptConfigs) > 0 {
		sections = append(sections, typeScriptSafetyGuidance()...)
	}
	sections = append(sections, cicdSecretsGuidance()...)
	sections = append(sections, staticAnalysisGuidance()...)
	if classification.Shape == repo.ShapeMonorepo {
		sections = append(sections, monorepoSecurityGuidance()...)
	}

	return strings.Join(sections, "\n")
}

func dependencySupplyChainGuidance(classification repo.Classification) []string {
	lines := []string{
		"- Dependency & supply-chain: use frozen-lockfile installs (`npm ci`, `pnpm install --frozen-lockfile`, `yarn install --immutable`) in CI and agent runs. Review lockfile changes for unexpected registry or integrity shifts. Prefer packages with npm provenance or sigstore signatures when choosing new dependencies. Scope packages correctly (`@org/pkg`) to avoid dependency-confusion attacks. Minimize new dependencies — prefer built-in Node APIs when practical.",
	}
	return lines
}

func packageManagerGuidance(classification repo.Classification) []string {
	managers := classification.ProcessHints.NodePackageManagers
	lines := []string{}
	if slices.Contains(managers, "npm") {
		lines = append(lines, "- npm hardening: prefer `npm ci` over `npm install` for reproducible builds. Use `--ignore-scripts` when installing untrusted packages. Review `.npmrc` for `ignore-scripts=false` or registry overrides that may weaken supply-chain safety.")
	}
	if slices.Contains(managers, "pnpm") {
		lines = append(lines, "- pnpm hardening: rely on pnpm strict mode and its content-addressable store for integrity. Prefer `pnpm install --frozen-lockfile` in CI. Use `.npmrc` with `strict-peer-dependencies=true` when practical.")
	}
	if slices.Contains(managers, "yarn") {
		lines = append(lines, "- Yarn hardening: prefer `yarn install --immutable` in CI. Use `yarn dlx` cautiously for one-off scripts. Review `.yarnrc.yml` for `enableScripts` and `nodeLinker` settings.")
	}
	return lines
}

func runtimeSecureCodingGuidance() []string {
	return []string{
		"- Runtime security: avoid prototype-pollution patterns (do not merge untrusted objects with spread/Object.assign into prototypes; prefer `Object.create(null)` or Map for lookup tables). Use `child_process.execFile` or `child_process.spawn` with explicit argv arrays instead of `child_process.exec` with string interpolation to prevent command injection. Use `crypto.randomUUID()` or `crypto.getRandomValues` instead of `Math.random()` for security-sensitive values. Validate untrusted input at system boundaries (user input, API payloads, URL parameters) with schema validation. Be aware of SSRF, path-traversal (`path.resolve` + prefix check), and ReDoS risks when handling external data.",
	}
}

func typeScriptSafetyGuidance() []string {
	return []string{
		"- TypeScript safety: prefer `strict: true` compiler settings. Avoid `any` as an escape hatch for untrusted data — use `unknown` with runtime validation instead. Use branded types or validation libraries (zod, io-ts) at trust boundaries. Do not suppress type errors with `@ts-ignore` unless the suppression is documented and scoped.",
	}
}

func cicdSecretsGuidance() []string {
	return []string{
		"- CI/CD & secrets: pin GitHub Actions to full commit SHAs rather than mutable tags. Use `permissions:` blocks with least privilege in workflow files. Never echo, log, or interpolate secrets in shell commands — use environment variables or secret-masking. Avoid storing tokens, keys, or credentials in source files, `.env` committed to git, or workflow logs.",
	}
}

func staticAnalysisGuidance() []string {
	return []string{
		"- Static analysis: when the repository already uses ESLint, preserve or enable security-focused rules (eslint-plugin-security, eslint-plugin-no-unsanitized). When CodeQL, Semgrep, or similar SAST tools are configured, do not weaken or disable their rulesets. When proposing new tooling, prefer tools already present in the ecosystem over adding new dependencies.",
	}
}

func monorepoSecurityGuidance() []string {
	return []string{
		"- Monorepo security: respect workspace dependency boundaries — do not introduce phantom dependencies (imports that rely on hoisting rather than explicit package.json declarations). Validate that shared packages do not leak internal secrets or credentials through exports. Be cautious with remote build caches that may expose environment variables or sensitive build artifacts. Ensure workspace publish workflows verify package integrity before publishing to registries.",
	}
}

func goSecurityGuidance(classification repo.Classification) string {
	sections := []string{
		"Go security and tooling guidance for this repository (apply where relevant to touched code and workflow — do not broaden issue scope):",
	}
	sections = append(sections, goFormattingGuidance()...)
	sections = append(sections, goTestingGuidance()...)
	sections = append(sections, goVetGuidance()...)
	sections = append(sections, goVulncheckGuidance()...)
	sections = append(sections, goLintGuidance()...)
	sections = append(sections, goEffectiveGoGuidance()...)
	sections = append(sections, goCryptoSecurityGuidance()...)
	sections = append(sections, goFuzzingGuidance()...)
	sections = append(sections, goDependencyGuidance()...)
	if isMixedLanguageGoRepo(classification) {
		sections = append(sections, goMixedLanguageGuidance()...)
	}
	return strings.Join(sections, "\n")
}

func goFormattingGuidance() []string {
	return []string{
		"- Formatting: run `gofmt` or `go fmt` on all touched Go files before committing. Do not hand-format Go code — let the standard formatter handle layout. If the repository uses `goimports`, use that instead.",
	}
}

func goTestingGuidance() []string {
	return []string{
		"- Testing: run targeted `go test ./path/to/package/...` for changed packages first. Use broader `go test ./...` when changes cross package boundaries or when the targeted scope is unclear. Use `-race` when practical to catch data races. Use `-count=1` to disable test caching when investigating flaky results.",
	}
}

func goVetGuidance() []string {
	return []string{
		"- Static analysis: run `go vet ./...` on touched packages to catch common mistakes such as printf format mismatches, unreachable code, and incorrect struct tags.",
	}
}

func goVulncheckGuidance() []string {
	return []string{
		"- Vulnerability checking: run `govulncheck ./...` when it is installed and the change touches dependencies or security-sensitive code. `govulncheck` reports only vulnerabilities that affect actually-called code paths. If `govulncheck` is not available, do not fabricate its output — note its absence and continue with other validation.",
	}
}

func goLintGuidance() []string {
	return []string{
		"- Linting: use the repository's established lint tooling. When `golangci-lint` is configured (`.golangci.yml` or `.golangci.yaml`), run `golangci-lint run` on touched packages. When `staticcheck` is the project standard, use that. Do not introduce a different linter unless the issue specifically requires it. If no project linter is configured, `go vet` is sufficient.",
	}
}

func goEffectiveGoGuidance() []string {
	return []string{
		"- Idiomatic Go: follow Effective Go conventions. Use MixedCaps (not underscores) for multi-word names. Keep exported identifiers descriptive and unexported identifiers short. Return errors rather than panicking. Use short variable declarations (`:=`) where type is clear from context. Prefer simple, straightforward error handling with early returns. Write doc comments on exported identifiers starting with the identifier name. Keep packages focused and APIs minimal.",
	}
}

func goCryptoSecurityGuidance() []string {
	return []string{
		"- Cryptography and secrets: use `crypto/rand` instead of `math/rand` for security-sensitive values. Prefer standard library crypto packages (`crypto/tls`, `crypto/aes`, `crypto/sha256`) over third-party alternatives unless there is a specific, documented reason. Do not store secrets, tokens, or credentials in source files. Use `crypto/subtle.ConstantTimeCompare` for comparing secret values to avoid timing attacks.",
	}
}

func goFuzzingGuidance() []string {
	return []string{
		"- Fuzzing: when adding or modifying parsers, decoders, or input-handling logic, consider adding a fuzz test (`func FuzzXxx(f *testing.F)`) using Go's built-in fuzzing support (Go 1.18+). This is optional and should only be added when the changed code processes untrusted or complex input.",
	}
}

func goDependencyGuidance() []string {
	return []string{
		"- Dependencies: prefer standard library packages when they cover the need. When adding new dependencies, check the Go vulnerability database via `govulncheck` or https://vuln.go.dev. Keep `go.mod` and `go.sum` consistent by running `go mod tidy` after dependency changes.",
	}
}

func isMixedLanguageGoRepo(classification repo.Classification) bool {
	if !slices.Contains(classification.TechStacks, repo.TechStackGo) {
		return false
	}
	return slices.Contains(classification.TechStacks, repo.TechStackNodeJS) ||
		classification.Shape == repo.ShapeMonorepo
}

func goMixedLanguageGuidance() []string {
	return []string{
		"- Mixed-language scope: this repository contains Go code alongside other languages or frontend assets. Scope Go tooling (`gofmt`, `go test`, `go vet`, `govulncheck`, Go linters) to Go source files and packages only. When frontend or Node.js code is also present, respect its own toolchain for frontend-scoped changes. When an issue touches both Go and frontend code, validate each side with its respective toolchain.",
	}
}

func githubActionsSecurityGuidance() string {
	sections := []string{
		"GitHub Actions workflow security guidance for this repository (apply where relevant to touched workflow files — do not broaden issue scope):",
	}
	sections = append(sections, githubActionsPinnedActionsGuidance()...)
	sections = append(sections, githubActionsPermissionsGuidance()...)
	sections = append(sections, githubActionsSecretHandlingGuidance()...)
	sections = append(sections, githubActionsInjectionGuidance()...)
	sections = append(sections, githubActionsOIDCGuidance()...)
	sections = append(sections, githubActionsWorkflowLintingGuidance()...)
	return strings.Join(sections, "\n")
}

func githubActionsPinnedActionsGuidance() []string {
	return []string{
		"- Pinned actions: pin all third-party and first-party GitHub Actions to full commit SHAs, not mutable tags or branches. Add a trailing comment with the version for readability (e.g., `actions/checkout@<sha> # v4`). When updating an action, verify the new SHA corresponds to a reviewed release.",
	}
}

func githubActionsPermissionsGuidance() []string {
	return []string{
		"- Least-privilege permissions: always declare a top-level `permissions:` block in workflow files. Default to `contents: read` and add only the permissions each job requires. Scope permissions per job when different jobs need different access. Never use `permissions: write-all` or leave permissions unspecified.",
	}
}

func githubActionsSecretHandlingGuidance() []string {
	return []string{
		"- Secret handling: never echo, log, or interpolate secrets directly in `run:` shell commands — pass them through environment variables. Use `::add-mask::` to mask dynamic values that may appear in logs. Do not store secrets, tokens, or credentials in workflow files or committed configuration.",
	}
}

func githubActionsInjectionGuidance() []string {
	return []string{
		"- Injection prevention: never interpolate untrusted event data (e.g., `${{ github.event.pull_request.title }}`, `${{ github.event.issue.body }}`) directly into `run:` shell scripts. Use an intermediate environment variable to prevent script injection. Prefer `pull_request` over `pull_request_target` unless cross-fork access is explicitly required and the workflow is hardened.",
	}
}

func githubActionsOIDCGuidance() []string {
	return []string{
		"- OIDC authentication: prefer OIDC-based cloud authentication (e.g., `aws-actions/configure-aws-credentials` with `role-to-assume`, `google-github-actions/auth` with workload identity) over long-lived credentials stored as repository secrets. When OIDC is available, document the trust policy scope in a workflow comment.",
	}
}

func githubActionsWorkflowLintingGuidance() []string {
	return []string{
		"- Workflow validation: run `actionlint` on touched workflow files when it is available. If `actionlint` is not installed, note its absence and continue with manual review. Set `timeout-minutes` on jobs to prevent hung runners, and use `concurrency` groups to avoid redundant workflow runs.",
	}
}
