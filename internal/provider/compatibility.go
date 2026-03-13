package provider

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/nicobistolfi/vigilante/internal/environment"
)

var versionPattern = regexp.MustCompile(`\b(v?\d+\.\d+\.\d+)\b`)

type cliVersion struct {
	major int
	minor int
	patch int
}

type compatibilityContract struct {
	minInclusive string
	maxExclusive string
}

var compatibilityContracts = map[string]compatibilityContract{
	DefaultID: {minInclusive: "0.114.0", maxExclusive: "2.0.0"},
	ClaudeID:  {minInclusive: "1.0.0", maxExclusive: "2.0.0"},
	GeminiID:  {minInclusive: "1.0.0", maxExclusive: "2.0.0"},
}

func ValidateRuntimeCompatibility(ctx context.Context, runner environment.Runner, selectedProvider Provider) error {
	tool := runtimeTool(selectedProvider)
	output, err := runner.Run(ctx, "", tool, "--version")
	if err != nil {
		return fmt.Errorf("detect %s CLI version: %w", tool, err)
	}
	return ValidateVersionOutput(selectedProvider, output)
}

func ValidateVersionOutput(selectedProvider Provider, output string) error {
	versionText, version, err := parseVersionOutput(selectedProvider.ID(), output)
	if err != nil {
		return err
	}
	contract, err := compatibilityFor(selectedProvider.ID())
	if err != nil {
		return err
	}
	min, err := parseVersion(contract.minInclusive)
	if err != nil {
		return err
	}
	max, err := parseVersion(contract.maxExclusive)
	if err != nil {
		return err
	}
	if compareVersions(version, min) < 0 || compareVersions(version, max) >= 0 {
		return fmt.Errorf(
			"%s CLI version %s is incompatible with this Vigilante build (supported: %s); install a compatible %s CLI version or use a matching Vigilante build",
			runtimeTool(selectedProvider),
			versionText,
			describeCompatibility(selectedProvider.ID()),
			runtimeTool(selectedProvider),
		)
	}
	return nil
}

func describeCompatibility(providerID string) string {
	contract, ok := compatibilityContracts[providerID]
	if !ok {
		return "unknown"
	}
	return fmt.Sprintf(">=%s, <%s", contract.minInclusive, contract.maxExclusive)
}

func parseVersionOutput(providerID string, output string) (string, cliVersion, error) {
	matches := versionPattern.FindStringSubmatch(output)
	if len(matches) < 2 {
		return "", cliVersion{}, fmt.Errorf(
			"could not parse %s CLI version from output %q (supported: %s); install a compatible %s CLI version or use a matching Vigilante build",
			providerID,
			strings.TrimSpace(output),
			describeCompatibility(providerID),
			providerID,
		)
	}
	versionText := strings.TrimPrefix(matches[1], "v")
	version, err := parseVersion(versionText)
	if err != nil {
		return "", cliVersion{}, fmt.Errorf(
			"could not parse %s CLI version %q (supported: %s): %w",
			providerID,
			versionText,
			describeCompatibility(providerID),
			err,
		)
	}
	return versionText, version, nil
}

func compatibilityFor(providerID string) (compatibilityContract, error) {
	contract, ok := compatibilityContracts[providerID]
	if !ok {
		return compatibilityContract{}, fmt.Errorf("no compatibility contract defined for provider %q", providerID)
	}
	return contract, nil
}

func runtimeTool(selectedProvider Provider) string {
	tools := selectedProvider.RequiredTools()
	if len(tools) == 0 {
		return selectedProvider.ID()
	}
	return strings.TrimSpace(tools[0])
}

func parseVersion(raw string) (cliVersion, error) {
	parts := strings.Split(strings.TrimSpace(raw), ".")
	if len(parts) != 3 {
		return cliVersion{}, fmt.Errorf("expected major.minor.patch")
	}
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return cliVersion{}, err
	}
	minor, err := strconv.Atoi(parts[1])
	if err != nil {
		return cliVersion{}, err
	}
	patch, err := strconv.Atoi(parts[2])
	if err != nil {
		return cliVersion{}, err
	}
	return cliVersion{major: major, minor: minor, patch: patch}, nil
}

func compareVersions(a cliVersion, b cliVersion) int {
	switch {
	case a.major != b.major:
		return compareInt(a.major, b.major)
	case a.minor != b.minor:
		return compareInt(a.minor, b.minor)
	default:
		return compareInt(a.patch, b.patch)
	}
}

func compareInt(a int, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}
