#!/usr/bin/env bash
#
# import-signing-certificate.sh — import the Apple Developer ID Application
# certificate into a throwaway keychain on a macOS CI runner.
#
# `codesign` needs a real keychain identity, so the certificate cannot simply be
# handed over as base64. The keychain created here is scoped to the runner and
# discarded with it; its password is random and never leaves the process.
#
# The keychain path is exported to the job via GITHUB_ENV as
# VIGILANTE_SIGN_KEYCHAIN, which scripts/sign-macos-binary.sh reads.
#
# Inputs (env):
#   APPLE_DEVELOPER_ID_CERT_P12_BASE64  base64 of the Developer ID Application
#                                       .p12 (certificate + private key)
#   APPLE_DEVELOPER_ID_CERT_PASSWORD    the .p12 export password
#   RUNNER_TEMP                         GitHub Actions temp dir (falls back to
#                                       a mktemp dir for local runs)
#   GITHUB_ENV                          GitHub Actions env file (optional)
#
# When the certificate secret is absent this exits 0 without creating anything.
# That is the fork-PR and unconfigured-repo path: sign-macos-binary.sh then falls
# back to ad-hoc signing so the execution gate still runs, and notarization is
# skipped. Releases stay unsigned rather than failing the build.
set -euo pipefail

if [ "$(uname -s)" != "Darwin" ]; then
  echo "==> $(uname -s) runner: no keychain to import into. Skipping."
  exit 0
fi

if [ -z "${APPLE_DEVELOPER_ID_CERT_P12_BASE64:-}" ]; then
  echo "::warning::APPLE_DEVELOPER_ID_CERT_P12_BASE64 is not set;" \
       "macOS artifacts will be ad-hoc signed and NOT notarized." \
       "See docs/releasing.md to configure signing."
  exit 0
fi

RUNNER_TEMP="${RUNNER_TEMP:-$(mktemp -d)}"
KEYCHAIN="$RUNNER_TEMP/vigilante-signing.keychain-db"
P12_PATH="$RUNNER_TEMP/application.p12"
KEYCHAIN_PASSWORD="$(openssl rand -base64 24)"

cleanup() {
  rm -f "$P12_PATH"
}
trap cleanup EXIT

security create-keychain -p "$KEYCHAIN_PASSWORD" "$KEYCHAIN"
# Keep it unlocked for the length of a release (6h) rather than the 5m default.
security set-keychain-settings -lut 21600 "$KEYCHAIN"
security unlock-keychain -p "$KEYCHAIN_PASSWORD" "$KEYCHAIN"

printf '%s' "$APPLE_DEVELOPER_ID_CERT_P12_BASE64" | base64 --decode > "$P12_PATH"
security import "$P12_PATH" -k "$KEYCHAIN" \
  -P "${APPLE_DEVELOPER_ID_CERT_PASSWORD:-}" -T /usr/bin/codesign
rm -f "$P12_PATH"

# Let codesign use the private key without an interactive prompt.
security set-key-partition-list \
  -S apple-tool:,apple:,codesign: -s -k "$KEYCHAIN_PASSWORD" "$KEYCHAIN" >/dev/null
# Make the throwaway keychain searchable so codesign resolves the identity.
security list-keychains -d user -s "$KEYCHAIN" login.keychain-db

if [ -n "${GITHUB_ENV:-}" ]; then
  echo "VIGILANTE_SIGN_KEYCHAIN=$KEYCHAIN" >> "$GITHUB_ENV"
fi

echo "==> imported Developer ID certificate into $KEYCHAIN"
echo "Available signing identities:"
security find-identity -v -p codesigning "$KEYCHAIN"
