#!/usr/bin/env node
'use strict';

// Assemble the vigilante npm packages from already-released archives.
//
// Stages the binary extracted from each published GitHub release archive
// into its platform package and patches the version fields in place. Never
// builds Go: the released binaries carry the Developer ID signature,
// notarization, and the ldflags version stamp, and a rebuild here would
// silently lose all three.
//
// The expected archive filenames embed the release version, so looking them
// up by name doubles as a tag/artifact consistency check: a mismatch fails
// the release instead of publishing a mislabeled package.
//
// Usage: build_packages.js --version <version, no leading v> --archives <dir>

const fs = require('fs');
const os = require('os');
const path = require('path');
const { execFileSync } = require('child_process');

const { TARGETS, mainPackageDir, platformPackageDir } = require('./targets');

function parseArgs(argv) {
  const args = {};
  for (let i = 0; i < argv.length; i += 1) {
    const arg = argv[i];
    if (arg === '--version' || arg === '--archives') {
      args[arg.slice(2)] = argv[(i += 1)];
    } else {
      throw new Error(`build_packages: unknown argument ${arg}`);
    }
  }
  if (!args.version || !args.archives) {
    throw new Error('build_packages: --version and --archives are required');
  }
  return args;
}

function extractBinary(archivePath, destPath) {
  const tmpDir = fs.mkdtempSync(path.join(os.tmpdir(), 'vigilante-npm-'));
  try {
    execFileSync('tar', ['xzf', archivePath, '-C', tmpDir, 'vigilante']);
    fs.mkdirSync(path.dirname(destPath), { recursive: true });
    fs.copyFileSync(path.join(tmpDir, 'vigilante'), destPath);
    fs.chmodSync(destPath, 0o755);
  } finally {
    fs.rmSync(tmpDir, { recursive: true, force: true });
  }
}

function writeVersion(packageJsonPath, version, optionalDependencyVersion) {
  const pkg = JSON.parse(fs.readFileSync(packageJsonPath, 'utf8'));
  pkg.version = version;
  if (optionalDependencyVersion && pkg.optionalDependencies) {
    for (const name of Object.keys(pkg.optionalDependencies)) {
      pkg.optionalDependencies[name] = optionalDependencyVersion;
    }
  }
  fs.writeFileSync(packageJsonPath, `${JSON.stringify(pkg, null, 2)}\n`);
}

function main() {
  const args = parseArgs(process.argv.slice(2));
  if (args.version.startsWith('v')) {
    throw new Error('build_packages: --version must not carry the leading v');
  }

  const archivesDir = path.resolve(args.archives);

  for (const target of TARGETS) {
    const archiveName = `vigilante_${args.version}_${target.archiveSuffix}.tar.gz`;
    const archivePath = path.join(archivesDir, archiveName);
    if (!fs.existsSync(archivePath)) {
      const present = fs.existsSync(archivesDir)
        ? fs.readdirSync(archivesDir).sort().join(', ') || 'none'
        : 'none (directory missing)';
      throw new Error(
        `build_packages: expected release archive ${archiveName} not found ` +
          `(downloaded: ${present}) — tag/artifact version mismatch?`
      );
    }

    const pkgDir = platformPackageDir(target);
    const binaryPath = path.join(pkgDir, 'bin', 'vigilante');
    extractBinary(archivePath, binaryPath);
    writeVersion(path.join(pkgDir, 'package.json'), args.version, null);
    console.log(`staged ${target.npmPackage}@${args.version} from ${archiveName}`);
  }

  writeVersion(path.join(mainPackageDir(), 'package.json'), args.version, args.version);
  console.log(`staged vigilante-cli@${args.version} with pinned optionalDependencies`);
}

main();
