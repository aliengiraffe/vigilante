#!/bin/sh

set -u

INSTALL_PATH="${VIGILANTE_INSTALL_PATH:-$HOME/.local/bin/vigilante}"
DAEMON_LABEL="${VIGILANTE_DAEMON_LABEL:-com.vigilante.agent}"
DAEMON_PLIST="${VIGILANTE_DAEMON_PLIST:-$HOME/Library/LaunchAgents/${DAEMON_LABEL}.plist}"

current_os() {
  if [ -n "${VIGILANTE_SETUP_DAEMON_OS:-}" ]; then
    printf '%s\n' "$VIGILANTE_SETUP_DAEMON_OS"
    return
  fi

  uname -s | tr '[:upper:]' '[:lower:]'
}

run_setup() {
  "$INSTALL_PATH" setup -d
}

print_interrupted_hint() {
  status="$1"
  if [ "$status" -eq 137 ]; then
    printf '%s\n' "setup-daemon: the refresh process was interrupted or killed during launchd reload."
  fi
}

cleanup_launchd() {
  uid="$(id -u)"
  launchctl bootout "gui/$uid" "$DAEMON_PLIST" >/dev/null 2>&1 || \
    launchctl unload "$DAEMON_PLIST" >/dev/null 2>&1 || true
  launchctl remove "$DAEMON_LABEL" >/dev/null 2>&1 || true
}

main() {
  os_name="$(current_os)"
  if [ "$os_name" != "darwin" ]; then
    exec "$INSTALL_PATH" setup -d
  fi

  run_setup
  status=$?
  if [ "$status" -eq 0 ]; then
    exit 0
  fi

  if [ ! -f "$DAEMON_PLIST" ]; then
    print_interrupted_hint "$status"
    exit "$status"
  fi

  printf '%s\n' "setup-daemon: detected an existing launch agent, attempting cleanup and one retry..."
  cleanup_launchd

  run_setup
  retry_status=$?
  if [ "$retry_status" -eq 0 ]; then
    printf '%s\n' "setup-daemon: recovered after cleaning up the existing launch agent."
    exit 0
  fi

  print_interrupted_hint "$retry_status"
  uid="$(id -u)"
  printf '%s\n' "setup-daemon: automatic recovery failed."
  printf '%s\n' "Next step: run 'launchctl bootout gui/$uid $DAEMON_PLIST' and retry 'task setup-daemon'."
  exit "$retry_status"
}

main "$@"
