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
