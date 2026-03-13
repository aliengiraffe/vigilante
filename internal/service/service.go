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
	dir := filepath.Join(cfg.HomeDir, "Library", "LaunchAgents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dir, "com.vigilante.agent.plist")
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
	path := filepath.Join(dir, "vigilante.service")
	if err := os.WriteFile(path, []byte(RenderSystemdUnit(store, cfg)), 0o644); err != nil {
		return err
	}
	for _, args := range [][]string{
		{"--user", "daemon-reload"},
		{"--user", "enable", "--now", "vigilante.service"},
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
  <string>com.vigilante.agent</string>
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
`, args[0], args[1], args[2], cfg.HomeDir, cfg.PathEnv, store.LogsDir(), store.LogsDir())) + "\n"
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
		return filepath.Join(home, "Library", "LaunchAgents", "com.vigilante.agent.plist"), nil
	case "linux":
		return filepath.Join(home, ".config", "systemd", "user", "vigilante.service"), nil
	default:
		return "", errors.New("unsupported OS")
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
