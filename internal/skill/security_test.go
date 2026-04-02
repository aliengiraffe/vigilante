package skill

import (
	"strings"
	"testing"

	"github.com/nicobistolfi/vigilante/internal/repo"
)

func TestSecurityGuidanceForNodeJSRepo(t *testing.T) {
	classification := repo.Classification{
		Shape:      repo.ShapeTraditional,
		TechStacks: []repo.TechStack{repo.TechStackNodeJS},
		ProcessHints: repo.ProcessHints{
			NodePackageManagers: []string{"npm"},
			NodeLockFiles:       []string{"package-lock.json"},
			TypeScriptConfigs:   []string{"tsconfig.json"},
		},
	}

	guidance := securityGuidanceForClassification(classification)

	for _, text := range []string{
		"JS/TS/Node security guidance",
		"Dependency & supply-chain",
		"frozen-lockfile",
		"npm hardening",
		"Runtime security",
		"prototype-pollution",
		"child_process.execFile",
		"TypeScript safety",
		"strict: true",
		"CI/CD & secrets",
		"pin GitHub Actions",
		"Static analysis",
		"ESLint",
	} {
		if !strings.Contains(guidance, text) {
			t.Fatalf("guidance missing %q", text)
		}
	}
}

func TestSecurityGuidanceForNodeJSRepoWithPnpm(t *testing.T) {
	classification := repo.Classification{
		Shape:      repo.ShapeTraditional,
		TechStacks: []repo.TechStack{repo.TechStackNodeJS},
		ProcessHints: repo.ProcessHints{
			NodePackageManagers: []string{"pnpm"},
			NodeLockFiles:       []string{"pnpm-lock.yaml"},
		},
	}

	guidance := securityGuidanceForClassification(classification)

	if !strings.Contains(guidance, "pnpm hardening") {
		t.Fatalf("guidance missing pnpm-specific content")
	}
	if strings.Contains(guidance, "- npm hardening:") {
		t.Fatalf("guidance should not include npm-specific hardening section for pnpm-only repo")
	}
}

func TestSecurityGuidanceForNodeJSRepoWithYarn(t *testing.T) {
	classification := repo.Classification{
		Shape:      repo.ShapeTraditional,
		TechStacks: []repo.TechStack{repo.TechStackNodeJS},
		ProcessHints: repo.ProcessHints{
			NodePackageManagers: []string{"yarn"},
			NodeLockFiles:       []string{"yarn.lock"},
		},
	}

	guidance := securityGuidanceForClassification(classification)

	if !strings.Contains(guidance, "Yarn hardening") {
		t.Fatalf("guidance missing yarn-specific content")
	}
}

func TestSecurityGuidanceOmitsTypeScriptWhenNotDetected(t *testing.T) {
	classification := repo.Classification{
		Shape:      repo.ShapeTraditional,
		TechStacks: []repo.TechStack{repo.TechStackNodeJS},
		ProcessHints: repo.ProcessHints{
			NodePackageManagers: []string{"npm"},
		},
	}

	guidance := securityGuidanceForClassification(classification)

	if strings.Contains(guidance, "TypeScript safety") {
		t.Fatalf("guidance should not include TypeScript section when no tsconfig detected")
	}
}

func TestSecurityGuidanceIncludesMonorepoForMonorepoShape(t *testing.T) {
	classification := repo.Classification{
		Shape:         repo.ShapeMonorepo,
		MonorepoStack: repo.MonorepoStackTurborepo,
		TechStacks:    []repo.TechStack{repo.TechStackNodeJS},
		ProcessHints: repo.ProcessHints{
			NodePackageManagers: []string{"pnpm"},
			NodeLockFiles:       []string{"pnpm-lock.yaml"},
		},
	}

	guidance := securityGuidanceForClassification(classification)

	if !strings.Contains(guidance, "Monorepo security") {
		t.Fatalf("guidance missing monorepo security section")
	}
	if !strings.Contains(guidance, "phantom dependencies") {
		t.Fatalf("guidance missing phantom dependency warning")
	}
}

func TestSecurityGuidanceOmitsMonorepoForTraditionalShape(t *testing.T) {
	classification := repo.Classification{
		Shape:      repo.ShapeTraditional,
		TechStacks: []repo.TechStack{repo.TechStackNodeJS},
		ProcessHints: repo.ProcessHints{
			NodePackageManagers: []string{"npm"},
		},
	}

	guidance := securityGuidanceForClassification(classification)

	if strings.Contains(guidance, "Monorepo security") {
		t.Fatalf("guidance should not include monorepo section for traditional repo")
	}
}

func TestSecurityGuidanceEmptyForNonNodeJSRepo(t *testing.T) {
	classification := repo.Classification{
		Shape: repo.ShapeTraditional,
	}

	guidance := securityGuidanceForClassification(classification)

	if guidance != "" {
		t.Fatalf("expected empty guidance for non-Node repo, got %q", guidance)
	}
}

func TestSecurityGuidanceEmptyForGoRepo(t *testing.T) {
	classification := repo.Classification{
		Shape: repo.ShapeTraditional,
		ProcessHints: repo.ProcessHints{
			GradleSettingsFiles: []string{"settings.gradle"},
		},
	}

	guidance := securityGuidanceForClassification(classification)

	if guidance != "" {
		t.Fatalf("expected empty guidance for non-Node repo, got %q", guidance)
	}
}

func TestSecurityGuidanceDoesNotBroadenScope(t *testing.T) {
	classification := repo.Classification{
		Shape:      repo.ShapeTraditional,
		TechStacks: []repo.TechStack{repo.TechStackNodeJS},
		ProcessHints: repo.ProcessHints{
			NodePackageManagers: []string{"npm"},
		},
	}

	guidance := securityGuidanceForClassification(classification)

	if !strings.Contains(guidance, "do not broaden issue scope") {
		t.Fatalf("guidance missing scope-limiting instruction")
	}
}

func TestSecurityGuidanceForGoRepo(t *testing.T) {
	classification := repo.Classification{
		Shape:      repo.ShapeTraditional,
		TechStacks: []repo.TechStack{repo.TechStackGo},
	}

	guidance := securityGuidanceForClassification(classification)

	for _, text := range []string{
		"Go security and tooling guidance",
		"gofmt",
		"go test",
		"go vet",
		"govulncheck",
		"golangci-lint",
		"MixedCaps",
		"crypto/rand",
		"crypto/subtle.ConstantTimeCompare",
		"FuzzXxx",
		"go mod tidy",
		"do not broaden issue scope",
	} {
		if !strings.Contains(guidance, text) {
			t.Fatalf("Go guidance missing %q", text)
		}
	}
}

func TestSecurityGuidanceEmptyForGoRepoWithoutGoMod(t *testing.T) {
	classification := repo.Classification{
		Shape: repo.ShapeTraditional,
	}

	guidance := securityGuidanceForClassification(classification)

	if strings.Contains(guidance, "Go security and tooling guidance") {
		t.Fatalf("guidance should not include Go section for non-Go repo")
	}
}

func TestSecurityGuidanceForPythonRepo(t *testing.T) {
	classification := repo.Classification{
		Shape:      repo.ShapeTraditional,
		TechStacks: []repo.TechStack{repo.TechStackPython},
	}

	guidance := securityGuidanceForClassification(classification)

	for _, text := range []string{
		"Python security and tooling guidance",
		"python -m venv .venv",
		"ruff format",
		"black",
		"mypy",
		"pytest",
		"pip-audit",
		"pickle",
		"subprocess",
		"shell=True",
		"secrets",
		"do not broaden issue scope",
	} {
		if !strings.Contains(guidance, text) {
			t.Fatalf("Python guidance missing %q", text)
		}
	}
}

func TestSecurityGuidanceForGoAndPythonRepo(t *testing.T) {
	classification := repo.Classification{
		Shape:      repo.ShapeTraditional,
		TechStacks: []repo.TechStack{repo.TechStackGo, repo.TechStackPython},
	}

	guidance := securityGuidanceForClassification(classification)

	if !strings.Contains(guidance, "Go security and tooling guidance") {
		t.Fatalf("guidance missing Go section for dual-stack repo")
	}
	if !strings.Contains(guidance, "Python security and tooling guidance") {
		t.Fatalf("guidance missing Python section for dual-stack repo")
	}
}

func TestSecurityGuidanceForGoAndNodeJSRepo(t *testing.T) {
	classification := repo.Classification{
		Shape:      repo.ShapeTraditional,
		TechStacks: []repo.TechStack{repo.TechStackNodeJS, repo.TechStackGo},
		ProcessHints: repo.ProcessHints{
			NodePackageManagers: []string{"npm"},
		},
	}

	guidance := securityGuidanceForClassification(classification)

	if !strings.Contains(guidance, "JS/TS/Node security guidance") {
		t.Fatalf("guidance missing Node.js section for dual-stack repo")
	}
	if !strings.Contains(guidance, "Go security and tooling guidance") {
		t.Fatalf("guidance missing Go section for dual-stack repo")
	}
}

func TestSecurityGuidanceForDotNetRepo(t *testing.T) {
	classification := repo.Classification{
		Shape:      repo.ShapeTraditional,
		TechStacks: []repo.TechStack{repo.TechStackDotNet},
	}

	guidance := securityGuidanceForClassification(classification)

	for _, text := range []string{
		".NET/C# security and tooling guidance",
		"dotnet format",
		"dotnet test",
		"built-in .NET analyzers",
		"nullable reference type",
		"NuGet",
		"user secrets",
		"ASP.NET",
		"do not broaden issue scope",
	} {
		if !strings.Contains(guidance, text) {
			t.Fatalf(".NET guidance missing %q", text)
		}
	}
}

func TestSecurityGuidanceForDotNetAndNodeJSRepo(t *testing.T) {
	classification := repo.Classification{
		Shape:      repo.ShapeTraditional,
		TechStacks: []repo.TechStack{repo.TechStackDotNet, repo.TechStackNodeJS},
		ProcessHints: repo.ProcessHints{
			NodePackageManagers: []string{"npm"},
		},
	}

	guidance := securityGuidanceForClassification(classification)

	if !strings.Contains(guidance, "JS/TS/Node security guidance") {
		t.Fatalf("guidance missing Node.js section for dual-stack dotnet repo")
	}
	if !strings.Contains(guidance, ".NET/C# security and tooling guidance") {
		t.Fatalf("guidance missing .NET section for dual-stack repo")
	}
	if !strings.Contains(guidance, "Mixed-language scope") {
		t.Fatalf("guidance missing mixed-language .NET section")
	}
}

func TestGoSecurityGuidanceDoesNotBroadenScope(t *testing.T) {
	classification := repo.Classification{
		Shape:      repo.ShapeTraditional,
		TechStacks: []repo.TechStack{repo.TechStackGo},
	}

	guidance := securityGuidanceForClassification(classification)

	if !strings.Contains(guidance, "do not broaden issue scope") {
		t.Fatalf("Go guidance missing scope-limiting instruction")
	}
}

func TestSecurityGuidanceForKubernetesRepo(t *testing.T) {
	classification := repo.Classification{
		Shape:      repo.ShapeTraditional,
		TechStacks: []repo.TechStack{repo.TechStackKubernetes},
	}

	guidance := securityGuidanceForClassification(classification)

	for _, text := range []string{
		"Kubernetes manifest and workload security guidance",
		"Service accounts",
		"automountServiceAccountToken",
		"Security context",
		"runAsNonRoot",
		"allowPrivilegeEscalation",
		"RBAC",
		"least-privilege",
		"Image security",
		"image digests",
		"NetworkPolicy",
		"do not broaden issue scope",
	} {
		if !strings.Contains(guidance, text) {
			t.Fatalf("Kubernetes guidance missing %q", text)
		}
	}
}

func TestSecurityGuidanceForJavaKotlinRepo(t *testing.T) {
	classification := repo.Classification{
		Shape:      repo.ShapeTraditional,
		TechStacks: []repo.TechStack{repo.TechStackJVM},
	}

	guidance := securityGuidanceForClassification(classification)

	for _, text := range []string{
		"Java/Kotlin security and tooling guidance",
		"Gradle or Maven",
		"Spotless",
		"Checkstyle",
		"SpotBugs",
		"detekt",
		"Kotlin coding conventions",
		"insecure deserialization",
		"SSRF",
		"Spring, Micronaut, Quarkus",
		"do not broaden issue scope",
	} {
		if !strings.Contains(guidance, text) {
			t.Fatalf("Java/Kotlin guidance missing %q", text)
		}
	}
}

func TestSecurityGuidanceEmptyForNonKubernetesRepo(t *testing.T) {
	classification := repo.Classification{
		Shape: repo.ShapeTraditional,
	}

	guidance := securityGuidanceForClassification(classification)

	if strings.Contains(guidance, "Kubernetes manifest") {
		t.Fatalf("guidance should not include Kubernetes section for non-K8s repo")
	}
}

func TestSecurityGuidanceForGradleJavaKotlinRepoMentionsGradle(t *testing.T) {
	classification := repo.Classification{
		Shape:      repo.ShapeTraditional,
		TechStacks: []repo.TechStack{repo.TechStackJVM},
		ProcessHints: repo.ProcessHints{
			GradleRootBuildFiles: []string{"build.gradle.kts"},
		},
	}

	guidance := securityGuidanceForClassification(classification)

	if !strings.Contains(guidance, "Gradle tasks") {
		t.Fatalf("expected Gradle-specific JVM guidance, got %q", guidance)
	}
	if strings.Contains(guidance, "actual Gradle or Maven tasks") {
		t.Fatalf("expected Gradle-specific wording when Gradle signals are present")
	}
}

func TestSecurityGuidanceForKubernetesAndGoRepo(t *testing.T) {
	classification := repo.Classification{
		Shape:      repo.ShapeTraditional,
		TechStacks: []repo.TechStack{repo.TechStackGo, repo.TechStackKubernetes},
	}

	guidance := securityGuidanceForClassification(classification)

	if !strings.Contains(guidance, "Go security and tooling guidance") {
		t.Fatalf("guidance missing Go section for Go+K8s repo")
	}
	if !strings.Contains(guidance, "Kubernetes manifest and workload security guidance") {
		t.Fatalf("guidance missing Kubernetes section for Go+K8s repo")
	}
}

func TestSecurityGuidanceForMixedJavaKotlinRepoIncludesMixedScope(t *testing.T) {
	classification := repo.Classification{
		Shape:      repo.ShapeTraditional,
		TechStacks: []repo.TechStack{repo.TechStackJVM, repo.TechStackNodeJS},
		ProcessHints: repo.ProcessHints{
			NodePackageManagers: []string{"npm"},
		},
	}

	guidance := securityGuidanceForClassification(classification)

	if !strings.Contains(guidance, "do not broaden issue scope") {
		t.Fatalf("guidance missing scope-limiting instruction")
	}
	if !strings.Contains(guidance, "Java/Kotlin security and tooling guidance") {
		t.Fatalf("guidance missing JVM section")
	}
	if !strings.Contains(guidance, "Mixed-language scope") {
		t.Fatalf("guidance missing mixed-language JVM section")
	}
}

func TestKubernetesSecurityGuidanceDoesNotBroadenScope(t *testing.T) {
	classification := repo.Classification{
		Shape:      repo.ShapeTraditional,
		TechStacks: []repo.TechStack{repo.TechStackKubernetes},
	}

	guidance := securityGuidanceForClassification(classification)

	if !strings.Contains(guidance, "do not broaden issue scope") {
		t.Fatalf("Kubernetes guidance missing scope-limiting instruction")
	}
}
