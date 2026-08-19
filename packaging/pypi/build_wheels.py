#!/usr/bin/env python3
"""Assemble the vigilante PyPI artifacts from already-released archives.

Builds one sdist plus one platform wheel per GoReleaser target, staging the
binary extracted from each published release archive. It never compiles Go:
the released binaries carry the Developer ID signature, notarization, and the
ldflags version stamp, and a rebuild here would silently lose all three.

The expected archive filenames embed the release version, so looking them up
by name doubles as the tag/artifact consistency check: a mismatch fails the
release instead of publishing a mislabeled wheel.

Usage: build_wheels.py --version <pep440-version> --archives <dir> --out <dir>
"""

import argparse
import os
import shutil
import subprocess
import sys
import tarfile
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from check_wheel import TARGETS, dist_name  # noqa: E402

HERE = Path(__file__).resolve().parent
STAGED_BINARY = HERE / "scripts" / "vigilante"


def run_build(kind, out_dir, env_extra):
    env = dict(os.environ)
    env.update(env_extra)
    subprocess.run(
        [sys.executable, "-m", "build", "--{}".format(kind), "--outdir", str(out_dir)],
        cwd=str(HERE),
        env=env,
        check=True,
    )


def extract_binary(archive):
    """Extract the vigilante binary from a release archive to the staging path."""
    STAGED_BINARY.parent.mkdir(parents=True, exist_ok=True)
    with tarfile.open(archive) as tf:
        member = tf.getmember("vigilante")
        with tf.extractfile(member) as src, open(STAGED_BINARY, "wb") as dst:
            shutil.copyfileobj(src, dst)
    STAGED_BINARY.chmod(0o755)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--version", required=True, help="release version, no leading v")
    parser.add_argument("--archives", required=True, type=Path)
    parser.add_argument("--out", required=True, type=Path)
    args = parser.parse_args()

    # Resolved to absolute: run_build() launches `python -m build` with
    # cwd=HERE, so a relative --out would resolve against the wrong
    # directory there while the existence check below still resolves it
    # against this process's cwd, silently doubling the path.
    args.out = args.out.resolve()

    if args.version.startswith("v"):
        sys.exit("build_wheels: --version must not carry the leading v (PEP 440)")

    # Version-consistency gate: every expected archive must exist under the
    # exact tag-derived name before anything is built.
    archives = {}
    for suffix, plat in TARGETS:
        archive = args.archives / "vigilante_{}_{}.tar.gz".format(args.version, suffix)
        if not archive.is_file():
            present = ", ".join(sorted(p.name for p in args.archives.iterdir())) or "none"
            sys.exit(
                "build_wheels: expected release archive {} not found "
                "(downloaded: {}) — tag/artifact version mismatch?".format(
                    archive.name, present
                )
            )
        archives[plat] = archive

    args.out.mkdir(parents=True, exist_ok=True)
    version_env = {"VIGILANTE_VERSION": args.version}

    # The sdist is built first, with no binary staged, so it can never
    # accidentally contain one (MANIFEST.in excludes it as well).
    if STAGED_BINARY.exists():
        STAGED_BINARY.unlink()
    run_build("sdist", args.out, version_env)

    name_norm = dist_name().replace("-", "_")
    for suffix, plat in TARGETS:
        extract_binary(archives[plat])
        try:
            run_build("wheel", args.out, dict(version_env, VIGILANTE_PLAT_NAME=plat))
        finally:
            STAGED_BINARY.unlink()
        wheel = args.out / "{}-{}-py3-none-{}.whl".format(name_norm, args.version, plat)
        if not wheel.is_file():
            sys.exit("build_wheels: expected wheel {} was not produced".format(wheel.name))
        print("built {} from {}".format(wheel.name, archives[plat].name))

    print("built sdist and {} wheels into {}".format(len(TARGETS), args.out))


if __name__ == "__main__":
    main()
