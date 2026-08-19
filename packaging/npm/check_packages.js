#!/usr/bin/env node
'use strict';

// Pre-publish gate for the vigilante npm packages.
//
// Inspects every package built by build_packages.js and fails the release
// when any invariant is broken. The regression that matters most is a
// platform package missing its os/cpu fields: npm would then install it on
// every platform, including ones the binary cannot run on. The other checks
// catch a lost executable bit, stray code shipped in a platform package, and
// version skew between the main package and its optionalDependencies.
//
// Usage: check_packages.js --version <version, no leading v>

const fs = require('fs');
const path = require('path');

const { TARGETS, mainPackageDir, platformPackageDir } = require('./targets');

const NETWORK_TOKENS = [
  "require('http')",
  'require("http")',
  "require('https')",
  'require("https")',
  'fetch(',
  'XMLHttpRequest',
];

function parseArgs(argv) {
  const args = {};
  for (let i = 0; i < argv.length; i += 1) {
    const arg = argv[i];
    if (arg === '--version') {
      args.version = argv[(i += 1)];
    } else {
      throw new Error(`check_packages: unknown argument ${arg}`);
    }
  }
  if (!args.version) {
    throw new Error('check_packages: --version is required');
  }
  return args;
}

function readPackageJson(dir) {
  return JSON.parse(fs.readFileSync(path.join(dir, 'package.json'), 'utf8'));
}

function checkPlatformPackage(target, version, failures) {
  const dir = platformPackageDir(target);
  const label = target.npmPackage;

  const pkg = readPackageJson(dir);
  if (pkg.version !== version) {
    failures.push(`${label}: version is ${pkg.version}, expected ${version}`);
  }
  if (JSON.stringify(pkg.os) !== JSON.stringify([target.os])) {
    failures.push(`${label}: "os" is ${JSON.stringify(pkg.os)}, expected ["${target.os}"]`);
  }
  if (JSON.stringify(pkg.cpu) !== JSON.stringify([target.cpu])) {
    failures.push(`${label}: "cpu" is ${JSON.stringify(pkg.cpu)}, expected ["${target.cpu}"]`);
  }
  if (pkg.bin) {
    failures.push(`${label}: must not declare "bin" — only the main package registers a command`);
  }
  if (pkg.scripts) {
    failures.push(`${label}: must not declare "scripts" — platform packages run no install-time code`);
  }

  const binaryPath = path.join(dir, 'bin', 'vigilante');
  if (!fs.existsSync(binaryPath)) {
    failures.push(`${label}: missing staged binary at bin/vigilante`);
  } else {
    const mode = fs.statSync(binaryPath).mode;
    if ((mode & 0o111) !== 0o111) {
      failures.push(`${label}: bin/vigilante is not executable (mode ${(mode & 0o777).toString(8)})`);
    }
  }

  const strayFiles = fs
    .readdirSync(path.join(dir, 'bin'))
    .filter((name) => name !== 'vigilante');
  if (strayFiles.length > 0) {
    failures.push(`${label}: unexpected files in bin/: ${strayFiles.join(', ')}`);
  }
}

function checkMainPackage(version, failures) {
  const dir = mainPackageDir();
  const pkg = readPackageJson(dir);

  if (pkg.version !== version) {
    failures.push(`vigilante-cli: version is ${pkg.version}, expected ${version}`);
  }

  const expectedOptional = TARGETS.map((t) => t.npmPackage).sort();
  const actualOptional = Object.keys(pkg.optionalDependencies || {}).sort();
  if (JSON.stringify(actualOptional) !== JSON.stringify(expectedOptional)) {
    failures.push(
      `vigilante-cli: optionalDependencies keys are ${JSON.stringify(actualOptional)}, expected ${JSON.stringify(expectedOptional)}`
    );
  }
  for (const name of expectedOptional) {
    const pinned = (pkg.optionalDependencies || {})[name];
    if (pinned !== version) {
      failures.push(`vigilante-cli: optionalDependencies["${name}"] is ${pinned}, expected ${version}`);
    }
  }

  if (!pkg.scripts || pkg.scripts.postinstall !== 'node scripts/postinstall.js') {
    failures.push('vigilante-cli: missing the non-network postinstall platform check');
  }

  for (const relFile of ['bin/vigilante.js', 'lib/platforms.js', 'scripts/postinstall.js']) {
    const filePath = path.join(dir, relFile);
    if (!fs.existsSync(filePath)) {
      failures.push(`vigilante-cli: missing ${relFile}`);
      continue;
    }
    const contents = fs.readFileSync(filePath, 'utf8');
    for (const token of NETWORK_TOKENS) {
      if (contents.includes(token)) {
        failures.push(`vigilante-cli: ${relFile} contains a networking call (${token}) — must never fetch over the network`);
      }
    }
  }
}

function main() {
  const args = parseArgs(process.argv.slice(2));
  const failures = [];

  for (const target of TARGETS) {
    checkPlatformPackage(target, args.version, failures);
  }
  checkMainPackage(args.version, failures);

  if (failures.length > 0) {
    console.error('check_packages: FAILED');
    for (const failure of failures) {
      console.error(`  - ${failure}`);
    }
    process.exit(1);
  }

  console.log('check_packages: all packages OK');
}

main();
