'use strict';

const path = require('path');

const ROOT = __dirname;

// GoReleaser archive suffix -> npm platform package. Mirrors the release
// build matrix in .goreleaser.yml (darwin/linux x amd64/arm64, minus
// linux/arm64) and the mapping in vigilante-cli/lib/platforms.js.
const TARGETS = [
  { archiveSuffix: 'macOS_arm64', npmPackage: 'vigilante-cli-darwin-arm64', os: 'darwin', cpu: 'arm64' },
  { archiveSuffix: 'macOS_amd64', npmPackage: 'vigilante-cli-darwin-x64', os: 'darwin', cpu: 'x64' },
  { archiveSuffix: 'Linux_amd64', npmPackage: 'vigilante-cli-linux-x64', os: 'linux', cpu: 'x64' },
];

function mainPackageDir() {
  return path.join(ROOT, 'vigilante-cli');
}

function platformPackageDir(target) {
  return path.join(ROOT, target.npmPackage);
}

module.exports = { TARGETS, mainPackageDir, platformPackageDir };
