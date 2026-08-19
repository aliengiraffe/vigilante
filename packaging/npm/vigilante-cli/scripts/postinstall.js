#!/usr/bin/env node
'use strict';

// Install-time gate: npm silently skips any optionalDependency whose os/cpu
// doesn't match, so an unsupported platform would otherwise install this
// package with no binary and no error. This script makes that failure loud
// instead, without ever touching the network — it only checks whether the
// matching platform package resolved.

const { platformPackage, UNSUPPORTED_PLATFORM_MESSAGE } = require('../lib/platforms');

function main() {
  const pkgName = platformPackage();
  if (pkgName) {
    try {
      require.resolve(`${pkgName}/package.json`);
      return;
    } catch (err) {
      // fall through to the failure message below
    }
  }

  process.stderr.write(UNSUPPORTED_PLATFORM_MESSAGE);
  process.exit(1);
}

main();
