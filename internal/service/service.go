package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/nicobistolfi/vigilante/internal/environment"
	"github.com/nicobistolfi/vigilante/internal/provider"
	"github.com/nicobistolfi/vigilante/internal/state"
)

type Config struct {
	Executable string
	PathEnv    string
	HomeDir    string
}

const (
	launchdLabel    = "com.vigilante.agent"
	systemdUnitName = "vigilante.service"
)

const (
	StatusNotInstalled = "not-installed"
	StatusStopped      = "stopped"
	StatusRunning      = "running"
)

type Status struct {
	Manager   string
	Service   string
	FilePath  string
	State     string
	Installed bool
	Running   bool
}

func Install(ctx context.Context, env *environment.Environment, store *state.Store, selectedProvider provider.Provider) error {
	cfg, err := BuildConfig(ctx, env, selectedProvider)
	if err != nil {
		return err
	}

	switch env.OS {
	case "darwin":
		return installLaunchdService(ctx, env, store, cfg)
	case "linux":
		return installSystemdUserService(ctx, env, store, cfg)
	default:
		return fmt.Errorf("unsupported OS %q", env.OS)
	}
}

func installLaunchdService(ctx context.Context, env *environment.Environment, store *state.Store, cfg Config) error {
	if err := prepareMacOSDaemonBinary(ctx, env.Runner, cfg.Executable); err != nil {
		return err
	}
	dir := filepath.Join(cfg.HomeDir, "Library", "LaunchAgents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dir, launchdLabel+".plist")
	if err := os.WriteFile(path, []byte(RenderLaunchdPlist(store, cfg)), 0o644); err != nil {
		return err
	}
	_, _ = env.Runner.Run(ctx, "", "launchctl", "unload", path)
	if _, err := env.Runner.Run(ctx, "", "launchctl", "load", path); err != nil {
		return err
	}
	return nil
}

func installSystemdUserService(ctx context.Context, env *environment.Environment, store *state.Store, cfg Config) error {
	dir := filepath.Join(cfg.HomeDir, ".config", "systemd", "user")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dir, systemdUnitName)
	if err := os.WriteFile(path, []byte(RenderSystemdUnit(store, cfg)), 0o644); err != nil {
		return err
	}
	for _, args := range [][]string{
		{"--user", "daemon-reload"},
		{"--user", "enable", "--now", systemdUnitName},
	} {
		if _, err := env.Runner.Run(ctx, "", "systemctl", args...); err != nil {
			return err
		}
	}
	return nil
}

func RenderLaunchdPlist(store *state.Store, cfg Config) string {
	args := []string{cfg.Executable, "daemon", "run"}
	return strings.TrimSpace(fmt.Sprintf(`
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple Computer//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>%s</string>
  <key>ProgramArguments</key>
  <array>
    <string>%s</string>
    <string>%s</string>
    <string>%s</string>
  </array>
  <key>EnvironmentVariables</key>
  <dict>
    <key>HOME</key>
    <string>%s</string>
    <key>PATH</key>
    <string>%s</string>
  </dict>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <true/>
  <key>StandardOutPath</key>
  <string>%s/vigilante.log</string>
  <key>StandardErrorPath</key>
  <string>%s/vigilante.err.log</string>
</dict>
</plist>
`, launchdLabel, args[0], args[1], args[2], cfg.HomeDir, cfg.PathEnv, store.LogsDir(), store.LogsDir())) + "\n"
}

func RenderSystemdUnit(store *state.Store, cfg Config) string {
	return strings.TrimSpace(fmt.Sprintf(`
[Unit]
Description=Vigilante issue watcher

[Service]
Environment=HOME=%s
Environment=PATH=%s
ExecStart=%s daemon run
Restart=on-failure
WorkingDirectory=%s
StandardOutput=append:%s/vigilante.log
StandardError=append:%s/vigilante.err.log

[Install]
WantedBy=default.target
`, cfg.HomeDir, cfg.PathEnv, cfg.Executable, store.Root(), store.LogsDir(), store.LogsDir())) + "\n"
}

func FilePath(goos string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	switch goos {
	case "darwin":
		return filepath.Join(home, "Library", "LaunchAgents", launchdLabel+".plist"), nil
	case "linux":
		return filepath.Join(home, ".config", "systemd", "user", systemdUnitName), nil
	default:
		return "", errors.New("unsupported OS")
	}
}

func ServiceStatus(ctx context.Context, env *environment.Environment) (Status, error) {
	status, err := baseStatus(env.OS)
	if err != nil {
		return Status{}, fmt.Errorf("unsupported OS %q", env.OS)
	}

	if _, err := os.Stat(status.FilePath); err != nil {
		if os.IsNotExist(err) {
			return status, nil
		}
		return Status{}, err
	}

	status.Installed = true

	switch env.OS {
	case "darwin":
		output, err := env.Runner.Run(ctx, "", "launchctl", "print", launchdTarget())
		if err != nil {
			if isLaunchdServiceMissing(output, err) {
				status.State = StatusStopped
				return status, nil
			}
			return Status{}, fmt.Errorf("query launchd service status: %w", err)
		}
		status.Running = launchdOutputIndicatesRunning(output)
	case "linux":
		output, err := env.Runner.Run(ctx, "", "systemctl", "--user", "show", "--property=LoadState,ActiveState", systemdUnitName)
		if err != nil {
			return Status{}, fmt.Errorf("query systemd user service status: %w", err)
		}
		loadState, activeState := parseSystemdShow(output)
		if loadState == "not-found" {
			status.Installed = false
			status.State = StatusNotInstalled
			return status, nil
		}
		status.Running = activeState == "active"
	}

	if status.Running {
		status.State = StatusRunning
	} else {
		status.State = StatusStopped
	}

	return status, nil
}

func Restart(ctx context.Context, env *environment.Environment) error {
	status, err := ServiceStatus(ctx, env)
	if err != nil {
		return err
	}
	if !status.Installed {
		return fmt.Errorf("service is not installed at %s", status.FilePath)
	}

	switch env.OS {
	case "darwin":
		_, err = env.Runner.Run(ctx, "", "launchctl", "kickstart", "-k", launchdTarget())
	case "linux":
		_, err = env.Runner.Run(ctx, "", "systemctl", "--user", "restart", systemdUnitName)
	default:
		return fmt.Errorf("unsupported OS %q", env.OS)
	}
	if err != nil {
		return err
	}
	return nil
}

func baseStatus(goos string) (Status, error) {
	path, err := FilePath(goos)
	if err != nil {
		return Status{}, err
	}
	switch goos {
	case "darwin":
		return Status{Manager: "launchd", Service: launchdLabel, FilePath: path, State: StatusNotInstalled}, nil
	case "linux":
		return Status{Manager: "systemd", Service: systemdUnitName, FilePath: path, State: StatusNotInstalled}, nil
	default:
		return Status{}, errors.New("unsupported OS")
	}
}

func BuildConfig(ctx context.Context, env *environment.Environment, selectedProvider provider.Provider) (Config, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Config{}, err
	}

	cfg := Config{
		Executable: environment.ExecutablePath(),
		PathEnv:    os.Getenv("PATH"),
		HomeDir:    home,
	}

	if shellPath := os.Getenv("SHELL"); shellPath != "" {
		pathValue, err := shellDerivedPath(ctx, env.Runner, shellPath)
		if err != nil {
			return Config{}, err
		}
		cfg.PathEnv = pathValue
	}

	if err := validateDaemonTooling(ctx, env.Runner, cfg.PathEnv, selectedProvider); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

func shellDerivedPath(ctx context.Context, runner environment.Runner, shellPath string) (string, error) {
	output, err := runner.Run(ctx, "", shellPath, "-lic", `printf "%s" "$PATH"`)
	if err != nil {
		return "", fmt.Errorf("derive PATH from shell %q: %w", shellPath, err)
	}
	pathValue := strings.TrimSpace(output)
	if pathValue == "" {
		return "", fmt.Errorf("shell %q returned an empty PATH", shellPath)
	}
	return pathValue, nil
}

func validateDaemonTooling(ctx context.Context, runner environment.Runner, pathEnv string, selectedProvider provider.Provider) error {
	missing := []string{}
	for _, tool := range provider.RequiredToolset(selectedProvider) {
		if err := validateToolInPath(ctx, runner, pathEnv, tool); err != nil {
			missing = append(missing, tool)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("daemon PATH does not resolve required tools: %s", strings.Join(missing, ", "))
	}
	if err := validateProviderVersionInPath(ctx, runner, pathEnv, selectedProvider); err != nil {
		return err
	}
	return nil
}

func validateToolInPath(ctx context.Context, runner environment.Runner, pathEnv string, tool string) error {
	shellPath := "/bin/sh"
	command := fmt.Sprintf("PATH=%q command -v %s", pathEnv, shellQuote(tool))
	_, err := runner.Run(ctx, "", shellPath, "-lc", command)
	return err
}

func validateProviderVersionInPath(ctx context.Context, runner environment.Runner, pathEnv string, selectedProvider provider.Provider) error {
	tool := shellQuote(runtimeTool(selectedProvider))
	command := fmt.Sprintf("PATH=%q %s --version", pathEnv, tool)
	output, err := runner.Run(ctx, "", "/bin/sh", "-lc", command)
	if err != nil {
		return fmt.Errorf("detect %s CLI version from daemon PATH: %w", runtimeTool(selectedProvider), err)
	}
	return provider.ValidateVersionOutput(selectedProvider, output)
}

func runtimeTool(selectedProvider provider.Provider) string {
	tools := selectedProvider.RequiredTools()
	if len(tools) == 0 {
		return selectedProvider.ID()
	}
	return tools[0]
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}

func prepareMacOSDaemonBinary(ctx context.Context, runner environment.Runner, executable string) error {
	resolvedPath, err := filepath.EvalSymlinks(executable)
	if err != nil {
		return fmt.Errorf("resolve macOS daemon binary %q: %w", executable, err)
	}

	if caskRoot, ok := homebrewCaskInstallRoot(resolvedPath); ok {
		if err := removeKnownMacOSAttributesRecursively(ctx, runner, caskRoot); err != nil {
			return fmt.Errorf("remove macOS extended attributes from Homebrew cask install root %q: %w", caskRoot, err)
		}
	}

	attrs, err := listExtendedAttributes(ctx, runner, resolvedPath)
	if err != nil {
		return fmt.Errorf("inspect macOS extended attributes for daemon binary %q: %w", resolvedPath, err)
	}

	removedAttrs := []string{}
	for _, attr := range []string{"com.apple.provenance", "com.apple.quarantine"} {
		if _, ok := attrs[attr]; !ok {
			continue
		}
		if _, err := runner.Run(ctx, "", "xattr", "-d", attr, resolvedPath); err != nil {
			return fmt.Errorf("remove macOS extended attribute %q from daemon binary %q: %w", attr, resolvedPath, err)
		}
		removedAttrs = append(removedAttrs, attr)
	}

	if _, err := runner.Run(ctx, "", "codesign", "--force", "--sign", "-", resolvedPath); err != nil {
		return fmt.Errorf("ad-hoc sign macOS daemon binary %s: %w", macOSBinaryContext(executable, resolvedPath, removedAttrs), err)
	}
	if _, err := runner.Run(ctx, "", "spctl", "--assess", "--type", "execute", "-vv", resolvedPath); err != nil {
		return fmt.Errorf("macOS rejected daemon binary %s: %w", macOSBinaryContext(executable, resolvedPath, removedAttrs), err)
	}

	return nil
}

func homebrewCaskInstallRoot(path string) (string, bool) {
	dir := filepath.Dir(path)
	tokenDir := filepath.Dir(dir)
	if filepath.Base(filepath.Dir(tokenDir)) != "Caskroom" {
		return "", false
	}
	return dir, true
}

func removeKnownMacOSAttributesRecursively(ctx context.Context, runner environment.Runner, path string) error {
	for _, attr := range []string{"com.apple.provenance", "com.apple.quarantine"} {
		command := fmt.Sprintf("xattr -dr %s %s >/dev/null 2>&1 || true", shellQuote(attr), shellQuote(path))
		if _, err := runner.Run(ctx, "", "/bin/sh", "-lc", command); err != nil {
			return err
		}
	}
	return nil
}

func listExtendedAttributes(ctx context.Context, runner environment.Runner, path string) (map[string]struct{}, error) {
	output, err := runner.Run(ctx, "", "xattr", path)
	if err != nil {
		return nil, err
	}

	attrs := map[string]struct{}{}
	for _, line := range strings.Split(output, "\n") {
		attr := strings.TrimSpace(line)
		if attr == "" {
			continue
		}
		attrs[attr] = struct{}{}
	}
	return attrs, nil
}

func macOSBinaryContext(executable string, resolvedPath string, removedAttrs []string) string {
	context := fmt.Sprintf("assessed_path=%q", resolvedPath)
	if filepath.Clean(executable) != filepath.Clean(resolvedPath) {
		context += fmt.Sprintf(" invoked_path=%q", executable)
	}
	if len(removedAttrs) == 0 {
		context += " removed_xattrs=none"
		return context
	}
	return context + fmt.Sprintf(" removed_xattrs=%s", strings.Join(removedAttrs, ","))
}

func launchdTarget() string {
	return fmt.Sprintf("gui/%d/%s", os.Getuid(), launchdLabel)
}

func isLaunchdServiceMissing(output string, err error) bool {
	combined := strings.ToLower(strings.TrimSpace(output + "\n" + err.Error()))
	return strings.Contains(combined, "could not find service") || strings.Contains(combined, "service not found")
}

func launchdOutputIndicatesRunning(output string) bool {
	lower := strings.ToLower(output)
	return strings.Contains(lower, "pid = ") || strings.Contains(lower, "\"pid\" =")
}

func parseSystemdShow(output string) (string, string) {
	var loadState string
	var activeState string
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "LoadState="):
			loadState = strings.TrimPrefix(line, "LoadState=")
		case strings.HasPrefix(line, "ActiveState="):
			activeState = strings.TrimPrefix(line, "ActiveState=")
		}
	}
	return loadState, activeState
}
