package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func InstallService(ctx context.Context, env *Environment, state *StateStore) error {
	switch env.OS {
	case "darwin":
		return installLaunchdService(ctx, env, state)
	case "linux":
		return installSystemdUserService(ctx, env, state)
	default:
		return fmt.Errorf("unsupported OS %q", env.OS)
	}
}

func installLaunchdService(ctx context.Context, env *Environment, state *StateStore) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	dir := filepath.Join(home, "Library", "LaunchAgents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dir, "com.vigilante.agent.plist")
	if err := os.WriteFile(path, []byte(renderLaunchdPlist(state)), 0o644); err != nil {
		return err
	}
	_, _ = env.Runner.Run(ctx, "", "launchctl", "unload", path)
	if _, err := env.Runner.Run(ctx, "", "launchctl", "load", path); err != nil {
		return err
	}
	return nil
}

func installSystemdUserService(ctx context.Context, env *Environment, state *StateStore) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	dir := filepath.Join(home, ".config", "systemd", "user")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dir, "vigilante.service")
	if err := os.WriteFile(path, []byte(renderSystemdUnit(state)), 0o644); err != nil {
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

func renderLaunchdPlist(state *StateStore) string {
	args := []string{executablePath(), "daemon", "run"}
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
`, args[0], args[1], args[2], state.LogsDir(), state.LogsDir())) + "\n"
}

func renderSystemdUnit(state *StateStore) string {
	return strings.TrimSpace(fmt.Sprintf(`
[Unit]
Description=Vigilante issue watcher

[Service]
ExecStart=%s daemon run
Restart=on-failure
WorkingDirectory=%s
StandardOutput=append:%s/vigilante.log
StandardError=append:%s/vigilante.err.log

[Install]
WantedBy=default.target
`, executablePath(), state.Root(), state.LogsDir(), state.LogsDir())) + "\n"
}

func serviceFilePath(goos string) (string, error) {
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
