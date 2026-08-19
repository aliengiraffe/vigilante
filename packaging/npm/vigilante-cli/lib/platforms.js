'use strict';

// Single source of truth for the platform-package mapping, shared by the
// bin/ shim (runtime dispatch) and scripts/postinstall.js (install-time
// check). Mirrors the GoReleaser build matrix in .goreleaser.yml
// (darwin/linux x amd64/arm64, minus linux/arm64) and packaging/npm/*.

const PLATFORM_PACKAGES = {
  'darwin-arm64': 'vigilante-cli-darwin-arm64',
  'darwin-x64': 'vigilante-cli-darwin-x64',
  'linux-x64': 'vigilante-cli-linux-x64',
};

const UNSUPPORTED_PLATFORM_MESSAGE = `
*** No prebuilt vigilante binary is available for this platform. ***

vigilante-cli ships prebuilt binaries for macOS (arm64 and x64) and Linux
(x64) only. On other platforms, install Vigilante with Homebrew:

    brew install --cask aliengiraffe/spaceship/vigilante

or with pip:

    pipx install vigilante-cli

or download a release archive for your platform directly:

    https://github.com/aliengiraffe/vigilante/releases
`;

function platformPackage() {
  return PLATFORM_PACKAGES[`${process.platform}-${process.arch}`] || null;
}

module.exports = { PLATFORM_PACKAGES, UNSUPPORTED_PLATFORM_MESSAGE, platformPackage };
