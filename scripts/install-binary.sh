#!/usr/bin/env bash
#
# install-binary.sh — atomically install a built binary to a destination path.
#
# Why this is not just `cp`: `cp` truncates the destination and writes into the
# EXISTING inode. Overwriting a Mach-O that a live process has mapped poisons the
# kernel's page cache for that inode — it holds pages from the old binary,
# validated against the old cdhash, mixed with the new file's contents. Every
# subsequent exec then fails the code-signature page hashes and AMFI SIGKILLs the
# process (exit 137), even though the bytes on disk are correct and
# `codesign --verify` passes. Since the vigilante daemon normally runs from the
# install path, `task install` hit this on every reinstall.
#
# Writing a temp file next to the destination and renaming over it gives the
# destination a NEW inode. Any process still executing the old binary keeps its
# own (now unlinked) inode and is unaffected. This is the same write-and-rename
# that `codesign` itself performs, which is why self-signing never had the bug.
#
# The temp file MUST live in the destination directory: `mv` is only an atomic
# rename(2) within a single filesystem. Across filesystems it degrades to a
# copy, which reintroduces the in-place write this script exists to avoid.
#
# Usage: install-binary.sh <source-binary> <destination-path>
set -euo pipefail

SRC="${1:?usage: install-binary.sh <source-binary> <destination-path>}"
DEST="${2:?usage: install-binary.sh <source-binary> <destination-path>}"

[ -f "$SRC" ] || { echo "ERROR: source binary $SRC does not exist" >&2; exit 1; }

DEST_DIR="$(dirname "$DEST")"
mkdir -p "$DEST_DIR"

TMP="$(mktemp "$DEST.XXXXXX")"
trap 'rm -f "$TMP"' EXIT

# `cat` rather than `cp` so the temp file keeps the mode mktemp created it with
# (0600) instead of inheriting the source's, closing the window where a
# world-readable partial binary is visible in the destination directory.
cat "$SRC" > "$TMP"
chmod 0755 "$TMP"
mv -f "$TMP" "$DEST"
trap - EXIT

echo "installed $SRC -> $DEST"
