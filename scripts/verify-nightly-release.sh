#!/usr/bin/env bash

set -euo pipefail

: "${NIGHTLY_TAG:?NIGHTLY_TAG is required}"
: "${NIGHTLY_VERSION:?NIGHTLY_VERSION is required}"

repo="${GITHUB_REPOSITORY:-}"
if [[ -z "$repo" ]]; then
  printf 'GITHUB_REPOSITORY is required\n' >&2
  exit 1
fi

max_attempts="${MAX_ATTEMPTS:-10}"
sleep_seconds="${SLEEP_SECONDS:-6}"

expected_assets=(
  "vigilante_${NIGHTLY_VERSION}_Linux_amd64.tar.gz"
  "vigilante_${NIGHTLY_VERSION}_macOS_amd64.tar.gz"
  "vigilante_${NIGHTLY_VERSION}_macOS_arm64.tar.gz"
)

release_api="repos/${repo}/releases/tags/${NIGHTLY_TAG}"
base_download_url="https://github.com/${repo}/releases/download/${NIGHTLY_TAG}"

release_is_ready() {
  local release_json="$1"
  local draft prerelease asset_name

  draft="$(jq -r '.draft' <<<"$release_json")"
  prerelease="$(jq -r '.prerelease' <<<"$release_json")"

  if [[ "$draft" != "false" || "$prerelease" != "true" ]]; then
    printf 'release state not ready yet: draft=%s prerelease=%s\n' "$draft" "$prerelease" >&2
    return 1
  fi

  for asset_name in "${expected_assets[@]}"; do
    if ! jq -e --arg name "$asset_name" 'any(.assets[]?; .name == $name)' <<<"$release_json" >/dev/null; then
      printf 'expected asset not present yet: %s\n' "$asset_name" >&2
      return 1
    fi
  done

  return 0
}

asset_urls_are_reachable() {
  local asset_name asset_url

  for asset_name in "${expected_assets[@]}"; do
    asset_url="${base_download_url}/${asset_name}"
    if ! curl --fail --silent --show-error --location --head "$asset_url" >/dev/null; then
      printf 'asset URL not reachable yet: %s\n' "$asset_url" >&2
      return 1
    fi
  done

  return 0
}

for ((attempt = 1; attempt <= max_attempts; attempt++)); do
  printf 'verification attempt %d/%d\n' "$attempt" "$max_attempts"
  release_json="$(gh api "$release_api")"

  if release_is_ready "$release_json" && asset_urls_are_reachable; then
    printf 'nightly release %s is published and downloadable\n' "$NIGHTLY_TAG"
    exit 0
  fi

  if (( attempt < max_attempts )); then
    sleep "$sleep_seconds"
  fi
done

printf 'nightly release %s did not become published and downloadable in time\n' "$NIGHTLY_TAG" >&2
exit 1
