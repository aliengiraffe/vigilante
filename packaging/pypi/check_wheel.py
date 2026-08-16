#!/usr/bin/env python3
"""Pre-upload gate for the vigilante PyPI artifacts.

Inspects every wheel built by build_wheels.py and fails the release when any
invariant is broken. The regression that matters most is a wheel silently
reverting to py3-none-any: pip would serve it to every platform, including
ones the binary cannot run on. The other checks catch a lost executable bit,
a stray .py file, a console-script shim, and purelib metadata.

Usage: check_wheel.py --dist <dir> --version <pep440-version>
"""

import argparse
import re
import sys
import zipfile
from pathlib import Path

# GoReleaser archive suffix -> wheel platform tag. Mirrors the release build
# matrix in .goreleaser.yml (darwin/linux x amd64/arm64, minus linux/arm64).
# manylinux2014 is honest for the Linux binary because it is CGO_ENABLED=0
# and therefore fully static. The macOS tags are permissive floors.
TARGETS = [
    ("macOS_arm64", "macosx_11_0_arm64"),
    ("macOS_amd64", "macosx_10_13_x86_64"),
    ("Linux_amd64", "manylinux2014_x86_64"),
]


def dist_name():
    """The distribution name, read from pyproject.toml (its single source)."""
    pyproject = Path(__file__).resolve().parent / "pyproject.toml"
    match = re.search(r'^name = "([^"]+)"', pyproject.read_text(encoding="utf-8"), re.M)
    if not match:
        sys.exit("check_wheel: could not read `name` from pyproject.toml")
    return match.group(1)


def check_wheel(wheel, name_norm, version, plat):
    """Return a list of problems with one wheel (empty when it is sound)."""
    problems = []
    data_script = "{}-{}.data/scripts/vigilante".format(name_norm, version)
    dist_info = "{}-{}.dist-info".format(name_norm, version)

    with zipfile.ZipFile(wheel) as zf:
        names = zf.namelist()

        py_files = [n for n in names if n.endswith(".py")]
        if py_files:
            problems.append("contains Python files: {}".format(", ".join(py_files)))

        if any(n.endswith("entry_points.txt") for n in names):
            problems.append("declares entry points; the binary must not be shimmed")

        if data_script not in names:
            problems.append("missing binary at {}".format(data_script))
        else:
            mode = zf.getinfo(data_script).external_attr >> 16
            if mode & 0o111 != 0o111:
                problems.append(
                    "binary mode {:o} lacks the executable bit".format(mode)
                )

        wheel_meta = "{}/WHEEL".format(dist_info)
        if wheel_meta not in names:
            problems.append("missing {}".format(wheel_meta))
        else:
            meta = zf.read(wheel_meta).decode("utf-8")
            if "Root-Is-Purelib: false" not in meta:
                problems.append("WHEEL metadata is not Root-Is-Purelib: false")
            expected_tag = "Tag: py3-none-{}".format(plat)
            if expected_tag not in meta:
                problems.append(
                    "WHEEL metadata lacks '{}' (got: {})".format(
                        expected_tag,
                        ", ".join(l for l in meta.splitlines() if l.startswith("Tag:")),
                    )
                )

    return problems


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--dist", required=True, type=Path)
    parser.add_argument("--version", required=True)
    args = parser.parse_args()

    name_norm = dist_name().replace("-", "_")
    failures = []

    for _, plat in TARGETS:
        wheel = args.dist / "{}-{}-py3-none-{}.whl".format(name_norm, args.version, plat)
        if not wheel.is_file():
            failures.append("{}: wheel not found".format(wheel.name))
            continue
        for problem in check_wheel(wheel, name_norm, args.version, plat):
            failures.append("{}: {}".format(wheel.name, problem))
        print("checked {}".format(wheel.name))

    sdist = args.dist / "{}-{}.tar.gz".format(name_norm, args.version)
    if not sdist.is_file():
        failures.append("{}: sdist not found".format(sdist.name))
    else:
        print("checked {}".format(sdist.name))

    extras = sorted(
        p.name
        for p in args.dist.iterdir()
        if p.suffix in (".whl", ".gz") and "py3-none-any" in p.name
    )
    for extra in extras:
        failures.append("{}: unexpected py3-none-any artifact".format(extra))

    if failures:
        print("check_wheel: FAILED", file=sys.stderr)
        for failure in failures:
            print("  - {}".format(failure), file=sys.stderr)
        sys.exit(1)
    print("check_wheel: all artifacts OK")


if __name__ == "__main__":
    main()
