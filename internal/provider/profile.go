package provider

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/nicobistolfi/vigilante/internal/environment"
	"github.com/nicobistolfi/vigilante/internal/skill"
	"github.com/nicobistolfi/vigilante/internal/state"
)

// ConfigDir describes the environment variable a provider's CLI reads to
// locate its configuration directory, and where that directory lives inside a
// sandbox container.
type ConfigDir struct {
	// EnvName is the variable the CLI itself honors. For Claude Code this is
	// CLAUDE_CONFIG_DIR, not the CLAUDE_HOME that only steers where Vigilante
	// installs skills for the default, no-profile invocation.
	EnvName string
	// ContainerPath is where the directory is mounted in a sandbox container.
	ContainerPath string
}

var configDirs = map[string]ConfigDir{
	ClaudeID: {EnvName: "CLAUDE_CONFIG_DIR", ContainerPath: "/root/.claude"},
	CodexID:  {EnvName: "CODEX_HOME", ContainerPath: "/root/.codex"},
}

// ConfigDirFor returns the config-directory convention for providerID. Only
// providers whose CLI documents such a variable have one.
func ConfigDirFor(providerID string) (ConfigDir, bool) {
	dir, ok := configDirs[providerID]
	return dir, ok
}

// ProfileConfigDir returns the host config directory an agent profile selects
// for providerID through its env, or "" when it does not override it.
func ProfileConfigDir(providerID string, profile state.AgentInvocation) string {
	dir, ok := configDirs[providerID]
	if !ok {
		return ""
	}
	return strings.TrimSpace(profile.Env[dir.EnvName])
}

// ForTarget returns p configured with target's agent profile. A target with no
// profile gets p back untouched, without reading config.json. A profile that
// is undefined or invalid is an error naming it, so the caller can stop the
// session instead of launching the agent under the default configuration.
//
// When the profile moves the provider's config directory, Vigilante's skills
// are installed there if missing, so they resolve before the session starts.
func ForTarget(store *state.Store, target state.WatchTarget, p Provider) (Provider, error) {
	name := target.AgentProfile
	if name == "" {
		return p, nil
	}
	config, err := store.LoadServiceConfig()
	if err != nil {
		return nil, fmt.Errorf("load config.json for agent profile %q: %w", name, err)
	}
	profile, err := config.AgentInvocation(name, p.ID())
	if err != nil {
		return nil, err
	}
	if dir := ProfileConfigDir(p.ID(), profile); dir != "" {
		if !filepath.IsAbs(dir) {
			configDir, _ := ConfigDirFor(p.ID())
			return nil, fmt.Errorf("agent profile %q: %s must be an absolute path, got %q", name, configDir.EnvName, dir)
		}
		if err := skill.EnsureInstalledIfMissing(p.ID(), dir); err != nil {
			return nil, fmt.Errorf("install vigilante skills for agent profile %q into %s: %w", name, dir, err)
		}
	}
	return WithProfile(p, profile), nil
}

// WithProfile wraps p so every invocation it builds carries profile's command,
// args, and env. A zero profile returns p itself.
func WithProfile(p Provider, profile state.AgentInvocation) Provider {
	if profile.IsZero() {
		return p
	}
	return profiledProvider{Provider: p, profile: profile}
}

// ApplyProfile applies p's agent profile, if it has one, to an invocation that
// was assembled outside the provider's own Build methods.
func ApplyProfile(p Provider, inv Invocation) Invocation {
	if profiled, ok := p.(profiledProvider); ok {
		return profiled.apply(inv)
	}
	return inv
}

// Run executes inv through runner, applying inv.Env when it is set. With no
// env this is exactly runner.Run, or RunStreaming when w is non-nil and the
// runner supports it.
func Run(ctx context.Context, runner environment.Runner, inv Invocation, w io.Writer) (string, error) {
	return environment.RunWithEnv(ctx, runner, inv.Dir, inv.Env, w, inv.Name, inv.Args...)
}

type profiledProvider struct {
	Provider
	profile state.AgentInvocation
}

// RequiredTools reports the profile's command in place of the provider binary
// so tooling and version checks target the binary that will actually run.
func (p profiledProvider) RequiredTools() []string {
	tools := p.Provider.RequiredTools()
	if p.profile.Command == "" || len(tools) == 0 {
		return tools
	}
	return append([]string{p.profile.Command}, tools[1:]...)
}

// EnsureRuntimeInstalled installs Vigilante's skills into the provider's
// default home and into the profile's config directory when it sets one.
func (p profiledProvider) EnsureRuntimeInstalled(store *state.Store) error {
	if err := p.Provider.EnsureRuntimeInstalled(store); err != nil {
		return err
	}
	if dir := ProfileConfigDir(p.ID(), p.profile); dir != "" {
		return skill.EnsureInstalled(p.ID(), dir)
	}
	return nil
}

func (p profiledProvider) BuildIssuePreflightInvocation(task IssueTask) (Invocation, error) {
	return p.applied(p.Provider.BuildIssuePreflightInvocation(task))
}

func (p profiledProvider) BuildIssueInvocation(task IssueTask) (Invocation, error) {
	return p.applied(p.Provider.BuildIssueInvocation(task))
}

func (p profiledProvider) BuildConflictResolutionInvocation(task ConflictTask) (Invocation, error) {
	return p.applied(p.Provider.BuildConflictResolutionInvocation(task))
}

func (p profiledProvider) BuildCIRemediationInvocation(task CIRemediationTask) (Invocation, error) {
	return p.applied(p.Provider.BuildCIRemediationInvocation(task))
}

func (p profiledProvider) BuildIssueCreateInvocation(task IssueCreateTask) (Invocation, error) {
	return p.applied(p.Provider.BuildIssueCreateInvocation(task))
}

func (p profiledProvider) BuildPackageRemediationInvocation(task PackageRemediationTask) (Invocation, error) {
	return p.applied(p.Provider.BuildPackageRemediationInvocation(task))
}

func (p profiledProvider) applied(inv Invocation, err error) (Invocation, error) {
	if err != nil {
		return inv, err
	}
	return p.apply(inv), nil
}

// apply overlays the profile on an invocation the provider built. Vigilante's
// own flags are never removed or reordered: profile args are inserted as one
// block directly before the prompt.
func (p profiledProvider) apply(inv Invocation) Invocation {
	if p.profile.Command != "" {
		inv.Name = p.profile.Command
	}
	if len(p.profile.Args) > 0 {
		at := promptIndex(p.ID(), inv.Args)
		args := make([]string, 0, len(inv.Args)+len(p.profile.Args))
		args = append(args, inv.Args[:at]...)
		args = append(args, p.profile.Args...)
		args = append(args, inv.Args[at:]...)
		inv.Args = args
	}
	if len(p.profile.Env) > 0 {
		env := make(map[string]string, len(inv.Env)+len(p.profile.Env))
		for key, value := range inv.Env {
			env[key] = value
		}
		for key, value := range p.profile.Env {
			env[key] = value
		}
		inv.Env = env
	}
	return inv
}

// promptIndex returns the position in args where the prompt begins: the
// --prompt flag for Gemini, which takes the prompt as a flag value, and the
// final positional argument for every other provider.
func promptIndex(providerID string, args []string) int {
	if providerID == GeminiID {
		for i, arg := range args {
			if arg == "--prompt" {
				return i
			}
		}
	}
	if len(args) == 0 {
		return 0
	}
	return len(args) - 1
}
