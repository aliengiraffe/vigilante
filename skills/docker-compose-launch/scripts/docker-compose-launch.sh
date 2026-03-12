#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: docker-compose-launch.sh --worktree <path> --services <csv> [--compose-file <path>] [--service-map <csv>]
EOF
}

fail() {
  printf '%s\n' "$*" >&2
  exit 1
}

trim() {
  local value=$1
  value=${value#"${value%%[![:space:]]*}"}
  value=${value%"${value##*[![:space:]]}"}
  printf '%s' "$value"
}

sanitize_service() {
  local value
  value=$(printf '%s' "$1" | tr '[:upper:]' '[:lower:]')
  case "$value" in
    mongo) value=mongodb ;;
  esac
  printf '%s' "$value"
}

require_supported_service() {
  case "$1" in
    mysql|mariadb|postgres|mongodb) ;;
    *) fail "unsupported service requested: $1" ;;
  esac
}

compose_command_string() {
  if command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1; then
    printf 'docker compose'
    return 0
  fi
  if command -v docker-compose >/dev/null 2>&1 && docker-compose version >/dev/null 2>&1; then
    printf 'docker-compose'
    return 0
  fi
  return 1
}

project_name_for_worktree() {
  local worktree base sum
  worktree=$1
  base=${worktree##*/}
  base=$(printf '%s' "$base" | tr '[:upper:]' '[:lower:]' | tr ' _/' '-' | tr -cd 'a-z0-9-')
  [ -n "$base" ] || base=worktree
  sum=$(printf '%s' "$worktree" | cksum | awk '{printf "%08x", $1}')
  printf '%s-%s' "$base" "${sum:0:8}"
}

default_container_port() {
  case "$1" in
    mysql|mariadb) printf '3306' ;;
    postgres) printf '5432' ;;
    mongodb) printf '27017' ;;
  esac
}

default_connection_uri() {
  local service=$1 port=$2
  case "$service" in
    mysql|mariadb) printf 'mysql://app:app@127.0.0.1:%s/app' "$port" ;;
    postgres) printf 'postgres://app:app@127.0.0.1:%s/app?sslmode=disable' "$port" ;;
    mongodb) printf 'mongodb://127.0.0.1:%s/app' "$port" ;;
  esac
}

emit_service_output() {
  local service compose_service port key
  service=$1
  compose_service=$2
  port=$3
  key=$(printf '%s' "$service" | tr '[:lower:]' '[:upper:]')
  printf '%s_SERVICE=%s\n' "$key" "$compose_service"
  printf '%s_PORT=%s\n' "$key" "$port"
  printf '%s_URL=%s\n' "$key" "$(default_connection_uri "$service" "$port")"
}

find_repository_compose() {
  find "$1" -type f \( -name 'compose.yaml' -o -name 'compose.yml' -o -name 'docker-compose.yaml' -o -name 'docker-compose.yml' \) 2>/dev/null | sort | head -n 1
}

generate_compose_file() {
  local compose_file=$1
  shift
  mkdir -p "$(dirname "$compose_file")"
  {
    printf 'services:\n'
    for service in "$@"; do
      case "$service" in
        mysql)
          cat <<'EOF'
  mysql:
    image: mysql:8.4
    environment:
      MYSQL_DATABASE: app
      MYSQL_USER: app
      MYSQL_PASSWORD: app
      MYSQL_ROOT_PASSWORD: root
    ports:
      - "127.0.0.1::3306"
    volumes:
      - mysql-data:/var/lib/mysql
EOF
          ;;
        mariadb)
          cat <<'EOF'
  mariadb:
    image: mariadb:11
    environment:
      MARIADB_DATABASE: app
      MARIADB_USER: app
      MARIADB_PASSWORD: app
      MARIADB_ROOT_PASSWORD: root
    ports:
      - "127.0.0.1::3306"
    volumes:
      - mariadb-data:/var/lib/mysql
EOF
          ;;
        postgres)
          cat <<'EOF'
  postgres:
    image: postgres:16
    environment:
      POSTGRES_DB: app
      POSTGRES_USER: app
      POSTGRES_PASSWORD: app
    ports:
      - "127.0.0.1::5432"
    volumes:
      - postgres-data:/var/lib/postgresql/data
EOF
          ;;
        mongodb)
          cat <<'EOF'
  mongodb:
    image: mongo:7
    ports:
      - "127.0.0.1::27017"
    volumes:
      - mongodb-data:/data/db
EOF
          ;;
      esac
    done
    printf 'volumes:\n'
    for service in "$@"; do
      printf '  %s-data:\n' "$service"
    done
  } >"$compose_file"
}

run_compose() {
  local compose_cmd=$1
  shift
  if [ "$compose_cmd" = "docker compose" ]; then
    docker compose "$@"
  else
    docker-compose "$@"
  fi
}

lookup_service_mapping() {
  local requested=$1 mapping key value
  if [ -z "${service_map_csv:-}" ]; then
    printf '%s' "$requested"
    return 0
  fi

  IFS=',' read -r -a mappings <<<"$service_map_csv"
  for mapping in "${mappings[@]}"; do
    key=${mapping%%=*}
    value=${mapping#*=}
    key=$(sanitize_service "$(trim "$key")")
    if [ "$key" = "$requested" ]; then
      printf '%s' "$(trim "$value")"
      return 0
    fi
  done

  printf '%s' "$requested"
}

worktree=
services_csv=
compose_file_override=
service_map_csv=

while [ $# -gt 0 ]; do
  case "$1" in
    --worktree)
      worktree=${2-}
      shift 2
      ;;
    --services)
      services_csv=${2-}
      shift 2
      ;;
    --compose-file)
      compose_file_override=${2-}
      shift 2
      ;;
    --service-map)
      service_map_csv=${2-}
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      usage >&2
      fail "unknown argument: $1"
      ;;
  esac
done

[ -n "$worktree" ] || fail "missing required --worktree"
[ -d "$worktree" ] || fail "worktree does not exist: $worktree"
[ -n "$services_csv" ] || fail "missing required --services"

compose_cmd=$(compose_command_string) || fail "docker compose is not available"
project_name=$(project_name_for_worktree "$worktree")

IFS=',' read -r -a raw_services <<<"$services_csv"
requested_services=()
for raw in "${raw_services[@]}"; do
  service=$(sanitize_service "$(trim "$raw")")
  require_supported_service "$service"
  requested_services+=("$service")
done

source=generated
compose_file=$compose_file_override
if [ -z "$compose_file" ]; then
  compose_file=$(find_repository_compose "$worktree" || true)
fi
if [ -n "$compose_file" ]; then
  source=repository
else
  compose_file="$worktree/.vigilante/docker-compose-launch/docker-compose.yml"
  generate_compose_file "$compose_file" "${requested_services[@]}"
fi

compose_workdir=$(dirname "$compose_file")

compose_services=()
for service in "${requested_services[@]}"; do
  mapped=$(lookup_service_mapping "$service")
  compose_services+=("$mapped")
done

up_output=
if ! up_output=$(run_compose "$compose_cmd" -f "$compose_file" -p "$project_name" up -d "${compose_services[@]}" 2>&1 >/dev/null); then
  printf '%s\n' "$up_output" >&2
  fail "docker compose up failed for ${compose_file}"
fi

printf 'SOURCE=%s\n' "$source"
printf 'COMPOSE_COMMAND=%s\n' "$compose_cmd"
printf 'COMPOSE_FILE=%s\n' "$compose_file"
printf 'COMPOSE_WORKDIR=%s\n' "$compose_workdir"
printf 'PROJECT_NAME=%s\n' "$project_name"
printf 'CLEANUP_COMMAND=%s\n' "$compose_cmd -f $compose_file -p $project_name down -v"

for i in "${!requested_services[@]}"; do
  service=${requested_services[$i]}
  compose_service=${compose_services[$i]}
  host_port=$(run_compose "$compose_cmd" -f "$compose_file" -p "$project_name" port "$compose_service" "$(default_container_port "$service")" | awk -F: 'NF {print $NF; exit}')
  [ -n "$host_port" ] || fail "failed to resolve host port for ${compose_service}"
  emit_service_output "$service" "$compose_service" "$host_port"
done
