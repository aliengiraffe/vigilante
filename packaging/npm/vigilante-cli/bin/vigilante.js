#!/usr/bin/env node
'use strict';

// Forwards argv/stdio/exit code to the prebuilt vigilante binary shipped by
// the platform-specific optionalDependency package (see lib/platforms.js).
// This file never fetches anything over the network: the binary is already
// on disk by the time this shim runs, staged by npm's own optionalDependency
// resolution at install time.

const { spawnSync } = require('child_process');
const { platformPackage, UNSUPPORTED_PLATFORM_MESSAGE } = require('../lib/platforms');

function resolveBinary() {
  const pkgName = platformPackage();
  if (!pkgName) {
    return null;
  }
  try {
    return require.resolve(`${pkgName}/bin/vigilante`);
  } catch (err) {
    return null;
  }
}

function main() {
  const binaryPath = resolveBinary();
  if (!binaryPath) {
    process.stderr.write(UNSUPPORTED_PLATFORM_MESSAGE);
    process.exit(1);
  }

  const result = spawnSync(binaryPath, process.argv.slice(2), { stdio: 'inherit' });
  if (result.error) {
    process.stderr.write(`vigilante-cli: failed to launch ${binaryPath}: ${result.error.message}\n`);
    process.exit(1);
  }
  process.exit(result.status === null ? 1 : result.status);
}

main();
