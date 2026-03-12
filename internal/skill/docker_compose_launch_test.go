package skill

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const testSystemPath = "/usr/bin:/bin:/usr/sbin:/sbin"

func TestDockerComposeLaunchSelectsDockerComposePlugin(t *testing.T) {
	scriptPath := filepath.Join(repoRoot(), "skills", DockerComposeLaunch, "scripts", "docker-compose-launch.sh")
	worktree := t.TempDir()
	binDir := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "docker.log")

	writeShellExecutable(t, filepath.Join(binDir, "docker"), `#!/bin/sh
printf '%s\n' "$*" >>"$FAKE_DOCKER_LOG"
if [ "$1" = "compose" ] && [ "$2" = "version" ]; then
  echo "Docker Compose version v2.29.0"
  exit 0
fi
if [ "$1" = "compose" ]; then
  shift
  while [ "$#" -gt 0 ]; do
    case "$1" in
      -p|-f)
        shift 2
        ;;
      *)
        break
        ;;
    esac
  done
  case "$1" in
    up)
      exit 0
      ;;
    port)
      echo "127.0.0.1:15432"
      exit 0
      ;;
  esac
fi
exit 1
`)

	output := runLaunchScript(t, scriptPath, worktree, map[string]string{
		"PATH":            binDir + string(os.PathListSeparator) + os.Getenv("PATH"),
		"FAKE_DOCKER_LOG": logPath,
	}, "--services", "postgres")

	if !strings.Contains(output, "COMPOSE_COMMAND=docker compose") {
		t.Fatalf("expected docker compose plugin to be selected, got: %s", output)
	}
	if !strings.Contains(output, "POSTGRES_PORT=15432") {
		t.Fatalf("expected postgres port in output, got: %s", output)
	}

	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(logData), "up -d postgres") {
		t.Fatalf("expected compose up call in log, got: %s", string(logData))
	}
}

func TestDockerComposeLaunchFallsBackToLegacyDockerCompose(t *testing.T) {
	scriptPath := filepath.Join(repoRoot(), "skills", DockerComposeLaunch, "scripts", "docker-compose-launch.sh")
	worktree := t.TempDir()
	binDir := t.TempDir()

	writeShellExecutable(t, filepath.Join(binDir, "docker-compose"), `#!/bin/sh
if [ "$1" = "version" ]; then
  echo "docker-compose version 1.29.2"
  exit 0
fi
while [ "$#" -gt 0 ]; do
  case "$1" in
    -p|-f)
      shift 2
      ;;
    *)
      break
      ;;
  esac
done
case "$1" in
  up)
    exit 0
    ;;
  port)
    echo "127.0.0.1:27018"
    exit 0
    ;;
esac
exit 1
`)

	output := runLaunchScript(t, scriptPath, worktree, map[string]string{
		"PATH": binDir + string(os.PathListSeparator) + testSystemPath,
	}, "--services", "mongo")

	if !strings.Contains(output, "COMPOSE_COMMAND=docker-compose") {
		t.Fatalf("expected docker-compose fallback, got: %s", output)
	}
	if !strings.Contains(output, "MONGODB_PORT=27018") {
		t.Fatalf("expected mongo port in output, got: %s", output)
	}
}

func TestDockerComposeLaunchGeneratesComposeFile(t *testing.T) {
	scriptPath := filepath.Join(repoRoot(), "skills", DockerComposeLaunch, "scripts", "docker-compose-launch.sh")
	worktree := t.TempDir()
	binDir := t.TempDir()

	writeShellExecutable(t, filepath.Join(binDir, "docker"), `#!/bin/sh
if [ "$1" = "compose" ] && [ "$2" = "version" ]; then
  exit 0
fi
if [ "$1" = "compose" ]; then
  shift
  while [ "$#" -gt 0 ]; do
    case "$1" in
      -p|-f)
        shift 2
        ;;
      *)
        break
        ;;
    esac
  done
  case "$1" in
    up)
      exit 0
      ;;
    port)
      case "$2:$3" in
        mysql:3306)
          echo "127.0.0.1:13306"
          ;;
        postgres:5432)
          echo "127.0.0.1:15432"
          ;;
        *)
          exit 1
          ;;
      esac
      exit 0
      ;;
  esac
fi
exit 1
`)

	output := runLaunchScript(t, scriptPath, worktree, map[string]string{
		"PATH": binDir + string(os.PathListSeparator) + os.Getenv("PATH"),
	}, "--services", "mysql,postgres")

	composeFile := filepath.Join(worktree, ".vigilante", "docker-compose-launch", "docker-compose.yml")
	data, err := os.ReadFile(composeFile)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{
		"image: mysql:8.4",
		"MYSQL_DATABASE: app",
		`- "127.0.0.1::3306"`,
		"image: postgres:16",
		"POSTGRES_DB: app",
		`- "127.0.0.1::5432"`,
		"mysql-data:",
		"postgres-data:",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected generated compose file to contain %q, got: %s", want, text)
		}
	}
	if !strings.Contains(output, "SOURCE=generated") {
		t.Fatalf("expected generated source output, got: %s", output)
	}
}

func TestDockerComposeLaunchReusesRepositoryComposeFile(t *testing.T) {
	scriptPath := filepath.Join(repoRoot(), "skills", DockerComposeLaunch, "scripts", "docker-compose-launch.sh")
	worktree := t.TempDir()
	binDir := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "docker.log")
	repoCompose := filepath.Join(worktree, "docker-compose.yml")
	if err := os.WriteFile(repoCompose, []byte("services:\n  db:\n    image: postgres:16\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	writeShellExecutable(t, filepath.Join(binDir, "docker"), `#!/bin/sh
printf '%s\n' "$*" >>"$FAKE_DOCKER_LOG"
if [ "$1" = "compose" ] && [ "$2" = "version" ]; then
  exit 0
fi
if [ "$1" = "compose" ]; then
  shift
  while [ "$#" -gt 0 ]; do
    case "$1" in
      -p|-f)
        shift 2
        ;;
      *)
        break
        ;;
    esac
  done
  case "$1" in
    up)
      exit 0
      ;;
    port)
      echo "127.0.0.1:25432"
      exit 0
      ;;
  esac
fi
exit 1
`)

	output := runLaunchScript(t, scriptPath, worktree, map[string]string{
		"PATH":            binDir + string(os.PathListSeparator) + os.Getenv("PATH"),
		"FAKE_DOCKER_LOG": logPath,
	}, "--services", "postgres", "--compose-file", repoCompose, "--service-map", "postgres=db")

	if !strings.Contains(output, "SOURCE=repository") {
		t.Fatalf("expected repository source output, got: %s", output)
	}
	if !strings.Contains(output, "POSTGRES_SERVICE=db") {
		t.Fatalf("expected mapped compose service output, got: %s", output)
	}

	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(logData), repoCompose) {
		t.Fatalf("expected repository compose file to be reused, got log: %s", string(logData))
	}
}

func TestDockerComposeLaunchFailsWithoutDocker(t *testing.T) {
	scriptPath := filepath.Join(repoRoot(), "skills", DockerComposeLaunch, "scripts", "docker-compose-launch.sh")
	worktree := t.TempDir()

	cmd := exec.Command("/bin/bash", scriptPath, "--worktree", worktree, "--services", "postgres")
	cmd.Env = append(os.Environ(), "PATH="+t.TempDir())
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected missing docker failure, got success: %s", string(output))
	}
	if !strings.Contains(string(output), "docker compose is not available") {
		t.Fatalf("expected missing docker error, got: %s", string(output))
	}
}

func TestDockerComposeLaunchFailsForUnsupportedService(t *testing.T) {
	scriptPath := filepath.Join(repoRoot(), "skills", DockerComposeLaunch, "scripts", "docker-compose-launch.sh")
	worktree := t.TempDir()
	binDir := t.TempDir()

	writeShellExecutable(t, filepath.Join(binDir, "docker"), `#!/bin/sh
if [ "$1" = "compose" ] && [ "$2" = "version" ]; then
  exit 0
fi
exit 1
`)

	cmd := exec.Command("/bin/bash", scriptPath, "--worktree", worktree, "--services", "redis")
	cmd.Env = append(os.Environ(), "PATH="+binDir+string(os.PathListSeparator)+testSystemPath)
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected unsupported service failure, got success: %s", string(output))
	}
	if !strings.Contains(string(output), "unsupported service requested: redis") {
		t.Fatalf("expected unsupported service error, got: %s", string(output))
	}
}

func TestDockerComposeLaunchSurfacesComposeUpFailures(t *testing.T) {
	scriptPath := filepath.Join(repoRoot(), "skills", DockerComposeLaunch, "scripts", "docker-compose-launch.sh")
	worktree := t.TempDir()
	binDir := t.TempDir()

	writeShellExecutable(t, filepath.Join(binDir, "docker"), `#!/bin/sh
if [ "$1" = "compose" ] && [ "$2" = "version" ]; then
  exit 0
fi
if [ "$1" = "compose" ]; then
  shift
  while [ "$#" -gt 0 ]; do
    case "$1" in
      -p|-f)
        shift 2
        ;;
      *)
        break
        ;;
    esac
  done
  if [ "$1" = "up" ]; then
    echo "Bind for 0.0.0.0:5432 failed: port is already allocated" >&2
    exit 1
  fi
fi
exit 1
`)

	cmd := exec.Command("/bin/bash", scriptPath, "--worktree", worktree, "--services", "postgres")
	cmd.Env = append(os.Environ(), "PATH="+binDir+string(os.PathListSeparator)+testSystemPath)
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected compose up failure, got success: %s", string(output))
	}
	if !strings.Contains(string(output), "docker compose up failed") || !strings.Contains(string(output), "port is already allocated") {
		t.Fatalf("expected compose failure to be surfaced, got: %s", string(output))
	}
}

func runLaunchScript(t *testing.T, scriptPath string, worktree string, env map[string]string, args ...string) string {
	t.Helper()

	cmd := exec.Command("/bin/bash", append([]string{scriptPath, "--worktree", worktree}, args...)...)
	cmd.Env = os.Environ()
	for key, value := range env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("expected script success, got error %v with output: %s", err, string(output))
	}
	return string(output)
}

func writeShellExecutable(t *testing.T, path string, body string) {
	t.Helper()

	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}
