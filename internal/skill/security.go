package skill

import (
	"slices"
	"strings"

	"github.com/nicobistolfi/vigilante/internal/repo"
)

func securityGuidanceForClassification(classification repo.Classification) string {
	if !slices.Contains(classification.TechStacks, repo.TechStackNodeJS) {
		return ""
	}
	return nodeJSSecurityGuidance(classification)
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
