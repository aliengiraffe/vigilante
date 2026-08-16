"""Wheel builder for the prebuilt vigilante binary.

One setup.py builds every platform wheel: the release pipeline stages the
already-released, already-signed Go binary at scripts/vigilante, sets
VIGILANTE_VERSION and VIGILANTE_PLAT_NAME, and runs the build. The binary is
declared through the classic `scripts` argument so it lands in
<dist>-<version>.data/scripts/vigilante inside the wheel — the standard
mechanism pip uses to copy a file onto PATH with the executable bit set. No
console-script entry point and no Python launcher shim: the installed
`vigilante` is the Go binary itself.

The sdist is a deliberate loud-failure fallback. It contains no binary, so
building a wheel from it (what pip does on platforms with no matching wheel,
e.g. Windows or Linux arm64) aborts with instructions instead of installing a
broken or no-op package. It intentionally does not build from Go source: a
local rebuild would lack the release signing and the ldflags version stamp.
"""

import os
from pathlib import Path

# setuptools must be imported first: it installs the distutils shim that the
# next import resolves to on Python >= 3.12, where stdlib distutils is gone.
from setuptools import setup
from setuptools.command.bdist_wheel import bdist_wheel

from distutils.command.build_scripts import build_scripts

HERE = Path(__file__).resolve().parent
BINARY = HERE / "scripts" / "vigilante"

UNSUPPORTED_PLATFORM_MESSAGE = """
*** No prebuilt vigilante binary is available for this platform. ***

vigilante-cli ships prebuilt wheels for macOS (arm64 and x86_64) and
Linux (x86_64) only. On other platforms, install Vigilante with Homebrew:

    brew install --cask aliengiraffe/spaceship/vigilante

or download a release archive for your platform directly:

    https://github.com/aliengiraffe/vigilante/releases
"""


def dist_version():
    """Return the release version: from the pipeline env, or from the
    sdist's recorded metadata so metadata preparation works during the
    (intentionally failing) install-from-sdist path."""
    version = os.environ.get("VIGILANTE_VERSION")
    if version:
        return version
    pkg_info = HERE / "PKG-INFO"
    if pkg_info.is_file():
        for line in pkg_info.read_text(encoding="utf-8").splitlines():
            if line.startswith("Version:"):
                return line.split(":", 1)[1].strip()
    return "0.0.0"


def target_platform(cmd):
    """The wheel platform tag: VIGILANTE_PLAT_NAME, or --plat-name."""
    env_plat = os.environ.get("VIGILANTE_PLAT_NAME")
    if env_plat:
        return env_plat
    if cmd.plat_name_supplied:
        return cmd.plat_name
    return None


class BinaryScripts(build_scripts):
    """build_scripts that copies the binary verbatim.

    The stock implementation tokenizes each script to rewrite a Python
    shebang and raises SyntaxError on a Mach-O/ELF binary. There is no
    shebang to rewrite here — copy the bytes untouched (rewriting them
    would also invalidate the macOS code signature).
    """

    def copy_scripts(self):
        self.mkpath(self.build_dir)
        outfiles = []
        for script in self.scripts:
            outfile = os.path.join(self.build_dir, os.path.basename(script))
            self.copy_file(script, outfile)
            os.chmod(outfile, 0o755)
            outfiles.append(outfile)
        return outfiles, outfiles


class BinaryWheel(bdist_wheel):
    """bdist_wheel that emits a py3-none-<platform> wheel around the binary."""

    def finalize_options(self):
        super().finalize_options()
        # The payload is a compiled binary, not pure Python; without this the
        # wheel would be tagged py3-none-any and served to every platform.
        self.root_is_pure = False

    def run(self):
        if not BINARY.is_file() or target_platform(self) is None:
            raise SystemExit(UNSUPPORTED_PLATFORM_MESSAGE)
        # bdist_wheel records the mode it finds on disk; make sure the
        # executable bit survives the wheel round trip.
        BINARY.chmod(0o755)
        super().run()

    def get_tag(self):
        plat = target_platform(self)
        if plat is None:
            raise SystemExit(UNSUPPORTED_PLATFORM_MESSAGE)
        return "py3", "none", plat.replace("-", "_").replace(".", "_")


setup(
    version=dist_version(),
    cmdclass={"bdist_wheel": BinaryWheel, "build_scripts": BinaryScripts},
    # Present only while the pipeline builds a wheel; absent in the sdist.
    scripts=["scripts/vigilante"] if BINARY.is_file() else [],
)
