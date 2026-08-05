#!/usr/bin/env bash
#
# sign-macos-binary.sh — sign a macOS binary with Apple's native `codesign`,
# verify it, EXECUTE it, and (optionally) notarize it.
#
# Called from .goreleaser.yml as a `builds` post hook, so it runs once per built
# target, after the binary is linked and BEFORE the archive is built and before
# anything is published — a nonzero exit here aborts the release.
#
# The execution step is the point of this script, not a bonus. Signature
# *verification* is exactly what fails to catch a bad designated requirement:
# `codesign --verify` re-hashes the file using the CodeDirectory's own
# parameters and never asks the kernel whether it would load the thing. Running
# the binary is the only check that exercises AMFI.
#
# We deliberately do NOT use GoReleaser's OSS `notarize.macos` pipe
# (anchore/quill). It embeds an unsatisfiable designated requirement
# (`certificate root[field.1.2.840.113635.100.6.2.6]`, where the marker actually
# lives on the Developer ID intermediate, so Apple writes `certificate 1[...]`).
# AMFI SIGKILLs such a binary at exec while `codesign -dv` still prints a
# healthy-looking summary. That is how our sibling `lander` project shipped two
# releases whose binaries could not run at all. See docs/releasing.md.
#
# Usage: sign-macos-binary.sh <path-to-binary> [goos]
#
# Inputs (env, all optional — the script degrades to what is available):
#   APPLE_DEVELOPER_ID_IDENTITY   "Developer ID Application: … (TEAMID)". When
#                                 unset, the identity is discovered from
#                                 VIGILANTE_SIGN_KEYCHAIN.
#   VIGILANTE_SIGN_KEYCHAIN       keychain holding the Developer ID Application
#                                 identity (the throwaway keychain CI creates).
#   APPLE_NOTARY_KEY_P8           App Store Connect .p8 path  ┐ notarization runs
#   APPLE_NOTARY_KEY_ID           notary key id               ├ only when all
#   APPLE_NOTARY_ISSUER_ID        notary issuer id            ┘ three are set.
#   VIGILANTE_SKIP_NOTARIZE       set to 1/true to sign and gate but skip the
#                                 notary submission (escape hatch for when
#                                 Apple's notary service is degraded).
#
# With no identity available (fork PRs, local snapshot builds, no secrets) the
# binary is signed AD-HOC so the execution gate can still run: on Apple Silicon
# an unsigned arm64 Mach-O is itself SIGKILLed by the kernel, so "unsigned" is
# not a testable state. Ad-hoc artifacts are not shippable and the script says
# so loudly.
set -euo pipefail

BIN="${1:?usage: sign-macos-binary.sh <path-to-binary> [goos]}"
GOOS_HINT="${2:-}"

# ------------------------------------------------------------------- skips ----
# This hook fires for every target in the build matrix, including linux/amd64,
# and on the Linux runners used by PR validation. Skip loudly rather than fail.
if [ -n "$GOOS_HINT" ] && [ "$GOOS_HINT" != "darwin" ]; then
  echo "==> $BIN targets $GOOS_HINT, not darwin. Skipping the sign + execute gate."
  exit 0
fi

if [ "$(uname -s)" != "Darwin" ]; then
  echo "==> $(uname -s) runner: no codesign, and darwin binaries are not executable here."
  echo "    Skipping the sign + execute gate for $BIN."
  echo "    NOTE: the exec gate only runs on the macOS jobs (release, nightly, PR smoke test)."
  exit 0
fi

[ -f "$BIN" ] || { echo "ERROR: $BIN does not exist" >&2; exit 1; }

# Belt and braces for the case where GoReleaser gives us no GOOS: a non-Mach-O
# file (the linux artifact) has no architectures to report.
if ! ARCHS=$(lipo -archs "$BIN" 2>/dev/null) || [ -z "$ARCHS" ]; then
  echo "==> $BIN is not a Mach-O binary. Skipping the sign + execute gate."
  exit 0
fi

# macos-latest ships bash 3.2, where expanding an empty array under `set -u` is
# an error — hence the `${arr[@]+"${arr[@]}"}` guard at the use site below.
KEYCHAIN_ARGS=()
[ -n "${VIGILANTE_SIGN_KEYCHAIN:-}" ] && KEYCHAIN_ARGS=(--keychain "$VIGILANTE_SIGN_KEYCHAIN")

# ---------------------------------------------------------------- identity ----
IDENTITY="${APPLE_DEVELOPER_ID_IDENTITY:-}"
if [ -z "$IDENTITY" ] && [ -n "${VIGILANTE_SIGN_KEYCHAIN:-}" ]; then
  # Prefer the identity's SHA-1 hash over its display name: it is unambiguous
  # even when several Developer ID certificates are present.
  IDENTITY=$(security find-identity -v -p codesigning "$VIGILANTE_SIGN_KEYCHAIN" 2>/dev/null \
    | awk '/Developer ID Application:/ { print $2; exit }')
  [ -n "$IDENTITY" ] && echo "==> discovered Developer ID Application identity $IDENTITY in $VIGILANTE_SIGN_KEYCHAIN"
fi

# -------------------------------------------------------------------- sign ----
if [ -n "$IDENTITY" ]; then
  echo "==> codesign (Developer ID, hardened runtime, secure timestamp): $BIN [$ARCHS]"
  # --options runtime  : hardened runtime, required for notarization
  # --timestamp        : secure timestamp from Apple's TSA, also required
  # --identifier       : pin it instead of letting codesign derive it from the
  #                      filename, so the signing identity is deterministic
  codesign \
    --force \
    --sign "$IDENTITY" \
    --identifier vigilante \
    --options runtime \
    --timestamp \
    ${KEYCHAIN_ARGS[@]+"${KEYCHAIN_ARGS[@]}"} \
    --verbose=2 \
    "$BIN"
  SIGNED_FOR_RELEASE=yes
else
  echo "==> no Developer ID identity available — signing AD-HOC."
  echo "    This artifact is NOT shippable; it exists so the execution gate below"
  echo "    can run (an unsigned arm64 Mach-O cannot exec on Apple Silicon)."
  codesign --force --sign - --identifier vigilante --options runtime "$BIN"
  SIGNED_FOR_RELEASE=no
fi

# ------------------------------------------------------------------ verify ----
# `codesign --verify` evaluates the signature AND the code's own designated
# requirement. That second part is what quill gets wrong, and it is the check
# that catches the failure mode described at the top of this file.
verify_failed=0
echo "==> codesign --verify --strict (signature + designated requirement)"
if ! codesign --verify --strict --verbose=2 "$BIN"; then
  verify_failed=1
  echo "::error::codesign --verify --strict rejected $BIN" >&2
  echo "  If this says 'does not satisfy its designated Requirement', the signer" >&2
  echo "  wrote a bad DR. Compare against Apple's canonical Developer ID form:" >&2
  echo "    identifier X and anchor apple generic and \\" >&2
  echo "    certificate 1[field.1.2.840.113635.100.6.2.6] and \\" >&2
  echo "    certificate leaf[field.1.2.840.113635.100.6.1.13] and \\" >&2
  echo "    certificate leaf[subject.OU] = TEAMID" >&2
  echo "  Actual DR embedded in this binary:" >&2
  codesign --display --requirements - "$BIN" 2>&1 | tail -3 >&2 || true
fi

echo "==> signature summary"
codesign --display --verbose=4 "$BIN" 2>&1 | grep -vE '^ *-?[0-9]+=' || true

# ------------------------------------------------- THE GATE: execute it -------
# A binary with a bad designated requirement reaches its entry point and is
# killed with SIGKILL (137) before printing a byte. Anything other than a clean
# exit 0 fails the release.
#
# `--help` is the cheapest subcommand that exits 0 (vigilante has no `version`
# command). DO_NOT_TRACK keeps CI gate runs out of telemetry — see
# internal/telemetry/telemetry.go.
run_gate() {
  local label="$1" path="$2" out rc
  set +e
  out=$(DO_NOT_TRACK=1 "$path" --help 2>&1)
  rc=$?
  set -e
  if [ "$rc" -ne 0 ]; then
    echo "::error::EXECUTION GATE FAILED ($label): '$path --help' exited $rc" >&2
    if [ "$rc" -eq 137 ]; then
      echo "  exit 137 = SIGKILL. The kernel (AMFI) refused to load this binary." >&2
      echo "  Userland verification passing does NOT mean the kernel accepts it." >&2
      echo "  Capture the kernel's reason on a Mac with:" >&2
      echo "    sudo log show --last 5m --predicate 'sender == \"AppleMobileFileIntegrity\" OR eventMessage CONTAINS \"code signature\"'" >&2
    fi
    echo "  output: ${out:-<none>}" >&2
    codesign --display --verbose=4 "$path" 2>&1 | grep -vE '^ *-?[0-9]+=' >&2 || true
    return 1
  fi
  echo "    OK ($label): exit 0"
  return 0
}

gate_failed=0
gate_ran=no

# Each artifact is single-arch, so the gate can only run when the artifact's
# architecture matches the runner's (macos-latest is arm64; the amd64 artifact
# needs Rosetta). Report honestly when it cannot run rather than implying the
# artifact was covered.
if [ "$ARCHS" = "$(uname -m)" ]; then
  echo "==> EXECUTION GATE: running the signed binary"
  run_gate "$ARCHS binary" "$BIN" || gate_failed=1
  gate_ran=yes
elif [ "$ARCHS" = "x86_64" ] && arch -x86_64 /usr/bin/true >/dev/null 2>&1; then
  echo "==> EXECUTION GATE: running the signed binary under Rosetta"
  run_gate "$ARCHS binary (rosetta)" "$BIN" || gate_failed=1
  gate_ran=yes
else
  echo "==> NOT COVERED: the $ARCHS binary was signed and verified but not executed"
  echo "                 (runner is $(uname -m) and Rosetta is unavailable). Its"
  echo "                 signature is structurally identical to the native one's."
fi

# Report both signals before bailing out — a DR problem and a failed exec are the
# same defect seen from two sides, and seeing both makes triage obvious.
if [ "$verify_failed" -ne 0 ] || [ "$gate_failed" -ne 0 ]; then
  echo "::error::signing gate failed for $BIN (verify=$verify_failed exec=$gate_failed) — refusing to continue" >&2
  exit 1
fi

# ---------------------------------------------------------------- notarize ----
skip_notarize=no
case "$(printf '%s' "${VIGILANTE_SKIP_NOTARIZE:-}" | tr '[:upper:]' '[:lower:]')" in
  1 | true | yes) skip_notarize=yes ;;
esac

if [ "$SIGNED_FOR_RELEASE" = yes ] \
  && [ "$skip_notarize" = no ] \
  && [ -n "${APPLE_NOTARY_KEY_P8:-}" ] \
  && [ -n "${APPLE_NOTARY_KEY_ID:-}" ] \
  && [ -n "${APPLE_NOTARY_ISSUER_ID:-}" ]; then
  echo "==> notarize (notarytool --wait)"
  # notarytool only accepts .zip/.pkg/.dmg, so submit the binary in a zip. A bare
  # Mach-O cannot have a ticket stapled — the ticket lives with Apple, keyed by
  # cdhash, and Gatekeeper checks it online.
  zip_dir=$(mktemp -d)
  trap 'rm -rf "$zip_dir"' EXIT
  zip_path="$zip_dir/vigilante-notarize.zip"
  ditto -c -k --keepParent "$BIN" "$zip_path"
  xcrun notarytool submit "$zip_path" \
    --key "$APPLE_NOTARY_KEY_P8" \
    --key-id "$APPLE_NOTARY_KEY_ID" \
    --issuer "$APPLE_NOTARY_ISSUER_ID" \
    --wait \
    --timeout 30m

  # Notarization does not modify the binary, but re-run the gate so a release can
  # never publish a binary that was not executed in its final shipped form.
  if [ "$gate_ran" = yes ]; then
    echo "==> EXECUTION GATE (post-notarization re-check)"
    run_gate "notarized $ARCHS binary" "$BIN"
  fi
elif [ "$SIGNED_FOR_RELEASE" = yes ]; then
  if [ "$skip_notarize" = yes ]; then
    echo "==> VIGILANTE_SKIP_NOTARIZE is set — skipping notarization (signing-only run)"
  else
    echo "==> notary credentials not set — skipping notarization (signing-only run)"
  fi
fi

if [ "$gate_ran" = yes ]; then
  echo "==> $BIN signed, verified, and PROVEN TO EXECUTE"
else
  echo "==> $BIN signed and verified (execution not covered on this runner)"
fi
