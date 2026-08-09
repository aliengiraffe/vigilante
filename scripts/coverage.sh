#!/usr/bin/env bash
#
# coverage.sh — produce a coverage profile, report per-package coverage, and
# enforce the committed ratchet threshold.
#
# This is the only place the coverage command lives. `task coverage` and the CI
# workflow both call this script so a local run and a CI run can never disagree
# about the number, which is the whole point of a ratchet: the threshold in
# scripts/coverage-threshold is only meaningful if everyone measures identically.
#
# The percentage is computed from the profile rather than scraped from `go test`
# output, because parsing the profile also gives per-package rollups, which
# `go tool cover -func` does not.
#
# Those rollups match `go test`'s own per-package percentages. The total differs
# from `go tool cover -func` by about 0.1pp: -func attributes blocks to declared
# functions and drops what it cannot place, giving it a slightly smaller
# denominator. This script counts every block in the profile. See
# scripts/coverage-threshold.
#
# Deliberately NOT using `-coverpkg=./...`. That flag re-bases the denominator on
# every package in the module rather than the package under test, which produces
# a different (lower) number and would make the committed threshold and the
# per-package figures incomparable to anything measured before it was added.
#
# Usage:
#   coverage.sh                       # run tests, write coverage.out, enforce threshold
#   coverage.sh --profile <path>      # report on an existing profile, skip `go test`
#   coverage.sh --threshold <pct>     # override the committed threshold
#
# --profile exists so the reporting and threshold logic can be tested against
# fixture profiles without paying for a full `go test ./...` run.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
THRESHOLD_FILE="$REPO_ROOT/scripts/coverage-threshold"

PROFILE=""
THRESHOLD=""
RUN_TESTS=true

while [ $# -gt 0 ]; do
  case "$1" in
    --profile)
      PROFILE="${2:?--profile requires a path}"
      RUN_TESTS=false
      shift 2
      ;;
    --threshold)
      THRESHOLD="${2:?--threshold requires a percentage}"
      shift 2
      ;;
    -h|--help)
      sed -n '2,28p' "${BASH_SOURCE[0]}"
      exit 0
      ;;
    *)
      echo "ERROR: unknown argument $1" >&2
      exit 2
      ;;
  esac
done

if [ -z "$THRESHOLD" ]; then
  [ -f "$THRESHOLD_FILE" ] || { echo "ERROR: missing threshold file $THRESHOLD_FILE" >&2; exit 1; }
  # Strip comments and blank lines so the threshold file can explain itself.
  THRESHOLD="$(grep -v '^[[:space:]]*#' "$THRESHOLD_FILE" | tr -d '[:space:]')"
fi

case "$THRESHOLD" in
  ''|*[!0-9.]*)
    echo "ERROR: threshold must be a number, got '$THRESHOLD'" >&2
    exit 1
    ;;
esac

if [ "$RUN_TESTS" = true ]; then
  PROFILE="$REPO_ROOT/coverage.out"
  # -covermode=atomic so the profile stays valid if -race is ever added here or
  # by a caller; the counts differ from the default mode but the set of covered
  # statements, and therefore the percentage, does not.
  #
  # No `|| true`: a test failure has to fail this script. Reporting coverage for
  # a suite that did not pass would be worse than reporting nothing.
  ( cd "$REPO_ROOT" && go test ./... -covermode=atomic -coverprofile="$PROFILE" )
fi

[ -f "$PROFILE" ] || { echo "ERROR: coverage profile $PROFILE does not exist" >&2; exit 1; }

# Profile lines are `<file>:<start>.<col>,<end>.<col> <numStatements> <count>`.
# Package coverage is covered statements over total statements, grouped by the
# file's directory. Packages with no test files never appear in the profile, so
# they are absent from this table rather than reported as 0%.
report="$(
  awk '
    /^mode:/ { next }
    NF != 3 { next }
    {
      path = substr($1, 1, index($1, ":") - 1)
      pkg = path
      sub(/\/[^\/]*$/, "", pkg)
      if (pkg == path) { pkg = "." }

      statements[pkg] += $2
      total_statements += $2
      if ($3 > 0) {
        covered[pkg] += $2
        total_covered += $2
      }
    }
    END {
      if (total_statements == 0) {
        print "NO_STATEMENTS"
        exit
      }
      for (pkg in statements) {
        printf "%6.1f%%  %s\n", (covered[pkg] * 100.0) / statements[pkg], pkg
      }
      printf "TOTAL %.1f\n", (total_covered * 100.0) / total_statements
    }
  ' "$PROFILE"
)"

if [ "$report" = "NO_STATEMENTS" ]; then
  echo "ERROR: coverage profile $PROFILE contains no statements" >&2
  exit 1
fi

TOTAL="$(printf '%s\n' "$report" | sed -n 's/^TOTAL //p')"
if [ -z "$TOTAL" ]; then
  echo "ERROR: could not compute a total from $PROFILE" >&2
  exit 1
fi

echo "Coverage by package (ascending):"
echo
# Sorted worst-first: the packages that need tests are the reason to read this.
printf '%s\n' "$report" | grep -v '^TOTAL ' | sort -n
echo
printf 'TOTAL: %s%%  (threshold %s%%)\n' "$TOTAL" "$THRESHOLD"

if awk -v total="$TOTAL" -v threshold="$THRESHOLD" 'BEGIN { exit !(total < threshold) }'; then
  echo
  echo "ERROR: total coverage ${TOTAL}% is below the ${THRESHOLD}% threshold in scripts/coverage-threshold" >&2
  exit 1
fi

echo "OK: total coverage meets the committed threshold."
