# Releasing Vigilante

Vigilante's macOS binaries are built, **signed**, and **notarized** by GitHub
Actions using [GoReleaser](https://goreleaser.com). This document covers the
one-time Apple setup, wiring the secrets from 1Password into GitHub, the
day-to-day release flow, and how to verify a published artifact.

- Config: [`.goreleaser.yml`](../.goreleaser.yml)
- Signing script: [`scripts/sign-macos-binary.sh`](../scripts/sign-macos-binary.sh)
- Keychain import: [`scripts/import-signing-certificate.sh`](../scripts/import-signing-certificate.sh)
- Workflows: [`release.yml`](../.github/workflows/release.yml),
  [`nightly.yml`](../.github/workflows/nightly.yml),
  [`release-smoke.yml`](../.github/workflows/release-smoke.yml)

## What the pipeline produces

| Artifact | Notes |
|---|---|
| `vigilante_<version>_macOS_arm64.tar.gz` | signed + notarized |
| `vigilante_<version>_macOS_amd64.tar.gz` | signed + notarized |
| `vigilante_<version>_Linux_amd64.tar.gz` | unsigned, unchanged |
| `gh-sandbox_<version>_Linux_{amd64,arm64}.tar.gz` | unsigned, unchanged |
| `checksums.txt` | covers all of the above |
| `vigilante_cli-<version>-py3-none-{macosx_11_0_arm64,macosx_10_13_x86_64,manylinux2014_x86_64}.whl` | PyPI wheels repackaging the three `vigilante` archives above |
| `vigilante_cli-<version>.tar.gz` (sdist) | PyPI fallback that fails loudly on unsupported platforms |

macOS binaries are signed with our **Apple Developer ID Application**
certificate under the hardened runtime and notarized by Apple's notary service,
so `brew install --cask vigilante` works without a Gatekeeper prompt.

Both macOS architectures ship as **separate per-arch binaries**, not a universal
binary, because the Homebrew casks select on `on_intel` / `on_arm`.

> **A bare CLI binary cannot have a notarization ticket stapled** — stapling
> targets `.app`, `.dmg`, and `.pkg`. The ticket is registered with Apple against
> the binary's cdhash and Gatekeeper checks it online. A machine that has never
> been online will not see the notarization. We ship no `.pkg`, so there is no
> offline-verifiable artifact today.

## Triggers

| Trigger | Environment | Result |
|---|---|---|
| Push a `vX.Y.Z` tag | `main` | Signed + notarized GitHub release, Homebrew cask updated |
| Push to `main` | `main` | Signed + notarized rolling `main-nightly` prerelease, `vigilante-nightly` cask updated |
| PR touching the release machinery | — | `macos-latest` snapshot build, **ad-hoc** signed, execution gate runs, no secrets |
| Any other PR | — | `goreleaser check` only, on Linux (`ci.yml`) |

### Why signing runs in a GoReleaser build hook

`scripts/sign-macos-binary.sh` is a `builds` **post hook**
(`.goreleaser.yml`), so it runs once per built target, after the binary is
linked but **before the archive is built and before anything is published**. A
nonzero exit aborts the release rather than shipping a broken artifact.

The hook fires for every target in the matrix. The script skips itself for
non-darwin targets, on non-macOS runners, and for non-Mach-O inputs, so the
Linux artifacts and the `gh-sandbox` build pass through untouched.

### The release-blocking execution gate

Signature inspection is not enough. A binary can carry a signature that every
`codesign -d` field reports as healthy and still be refused by the kernel, so the
script **runs what it built** and fails on a nonzero exit:

| Check | Covers |
|---|---|
| `codesign --verify --strict` | signature integrity **and** the designated requirement |
| `<binary> --help` (under `DO_NOT_TRACK=1`) | that AMFI actually loads and runs the artifact |
| re-run of the above after notarization | that the final shipped form still runs |

`--help` is the gate command because Vigilante has no `version` subcommand.
`DO_NOT_TRACK=1` keeps CI gate runs out of telemetry
(`internal/telemetry/telemetry.go`).

**Coverage limit:** `macos-latest` is arm64, so the `amd64` artifact is executed
only when Rosetta is present on the runner. When it is not, the script signs and
verifies that artifact and reports plainly that it was **NOT COVERED** rather
than implying the gate ran.

### Why native codesign, not GoReleaser's `notarize.macos`

**Do not switch this to GoReleaser's OSS `notarize.macos` pipe (anchore/quill).**

Our sibling project `lander` (in `aliengiraffe/env-manager`) shipped **two
releases whose binaries could not run at all** — SIGKILLed by the kernel at exec
before printing a byte. The cause was quill embedding an unsatisfiable
designated requirement:

```text
# what quill wrote — cannot ever be satisfied
identifier X and anchor apple generic and
  certificate root[field.1.2.840.113635.100.6.2.6] and certificate leaf[subject.OU] = "TEAMID"

# what Apple's codesign writes
identifier X and anchor apple generic and
  certificate 1[field.1.2.840.113635.100.6.2.6] and
  certificate leaf[field.1.2.840.113635.100.6.1.13] and certificate leaf[subject.OU] = "TEAMID"
```

The `1.2.840.113635.100.6.2.6` marker is carried by the **"Developer ID
Certification Authority" intermediate**, never by Apple Root CA, so
`certificate root[...]` is false for every Developer ID chain that exists. A
non-ad-hoc signature whose designated requirement fails validation is refused by
AMFI at exec.

Two things let it survive two releases, and both are guarded against here:

- **Ad-hoc signing hides it.** An ad-hoc designated requirement is a `cdhash`
  literal, which is trivially satisfiable — which is why `codesign -s -` "fixes"
  a broken binary without fixing anything.
- **CI never asked Apple's tooling anything.** Nothing ran `codesign --verify`,
  and nothing ran the binary. Note that `codesign -dv` prints a completely
  healthy-looking summary for the broken binary, because it never evaluates the
  designated requirement.

## Secrets and variables

All live in the **`main`** GitHub environment of `aliengiraffe/vigilante`.

| Name | Kind | Purpose |
|---|---|---|
| `APPLE_DEVELOPER_ID_CERT_P12_BASE64` | secret | base64 of the Developer ID **Application** `.p12` |
| `APPLE_DEVELOPER_ID_CERT_PASSWORD` | secret | the `.p12` export password |
| `APPLE_NOTARY_API_KEY_P8_BASE64` | secret | base64 of the App Store Connect `.p8` notary key |
| `APPLE_NOTARY_API_KEY_ID` | secret | notary Key ID |
| `APPLE_NOTARY_API_ISSUER_ID` | secret | notary Issuer ID |
| `APPLE_DEVELOPER_ID_IDENTITY` | variable | `Developer ID Application: <Name> (TEAMID)` |
| `VIGILANTE_SKIP_NOTARIZE` | variable | optional; set to `1` to skip notary submission while keeping signing and the gate |

`APPLE_DEVELOPER_ID_IDENTITY` is passed to `codesign --sign`. When it is unset,
the signing script discovers the identity from the throwaway keychain instead, so
both paths work.

**Degradation is deliberate.** When the certificate secret is absent the workflows
do not fail: `import-signing-certificate.sh` warns and exits 0, and the signing
script falls back to ad-hoc signing with notarization skipped. Releases stay
unsigned instead of the pipeline breaking.

No **Developer ID Installer** certificate is needed — Vigilante ships no `.pkg`.

### Pulling the secrets from 1Password

The Developer ID certificate and notary key are **team-wide**, not per-product —
these are the same items `lander` uses. They live in the **`Engineering`** vault:

| 1Password item | Category |
|---|---|
| `Apple Developer ID — Lander (.p12)` | Document — Developer ID **Application** certificate |
| `Apple Notary API Key — Lander (.p8)` | Document — App Store Connect notary key |
| `Apple Developer ID — Lander Code Signing` | Secure Note — `p12 password`, `notary key id`, `notary issuer id`, `team id`, `identity`, `developer portal cert expiry` |

> The `— Lander` naming predates Vigilante using the same certificate. Renaming
> the items to something product-neutral would mean updating this document and
> `env-manager/lander/docs/releasing.md` together.

Run signed in to the `aliengiraffe` 1Password account with `gh` authenticated
against this repository.

Two `op` quirks make the obvious one-liners fail, so the script below works around
both:

- **`op://` secret references cannot contain an em dash**, and every item title
  here has one. Address the Secure Note by its **item UUID** instead of its title
  (`op item list --vault Engineering` prints the IDs).
- **`op document get` refuses to write a binary document to a pipe** — it errors
  with *"the queried document contains unprintable characters"*. The `.p12` is
  binary, so it needs `--out-file`. (The `.p8` is PEM text and would pipe fine;
  it uses a file here for symmetry.)

```bash
set -euo pipefail
REPO=aliengiraffe/vigilante
VAULT=Engineering
# UUID of "Apple Developer ID — Lander Code Signing"; confirm with `op item list`.
NOTE_ID=stfwdaukd5mtodowuejck2obsu

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
umask 077

op document get "Apple Developer ID — Lander (.p12)" --vault "$VAULT" --out-file "$WORK/cert.p12"
op document get "Apple Notary API Key — Lander (.p8)" --vault "$VAULT" --out-file "$WORK/notary.p8"

# --- secrets ---
# `tr -d '\n'` gives the single-line encoding the workflows decode.
base64 < "$WORK/cert.p12" | tr -d '\n' \
  | gh secret set APPLE_DEVELOPER_ID_CERT_P12_BASE64 --env main --repo "$REPO"

base64 < "$WORK/notary.p8" | tr -d '\n' \
  | gh secret set APPLE_NOTARY_API_KEY_P8_BASE64 --env main --repo "$REPO"

# `printf '%s' "$(...)"` guarantees no trailing newline reaches the secret value,
# independently of how gh handles stdin. A stray newline in the .p12 password
# fails the keychain import in CI with a misleading error.
printf '%s' "$(op read "op://$VAULT/$NOTE_ID/p12 password")" \
  | gh secret set APPLE_DEVELOPER_ID_CERT_PASSWORD --env main --repo "$REPO"

printf '%s' "$(op read "op://$VAULT/$NOTE_ID/notary key id")" \
  | gh secret set APPLE_NOTARY_API_KEY_ID --env main --repo "$REPO"

printf '%s' "$(op read "op://$VAULT/$NOTE_ID/notary issuer id")" \
  | gh secret set APPLE_NOTARY_API_ISSUER_ID --env main --repo "$REPO"

# --- non-secret variable ---
printf '%s' "$(op read "op://$VAULT/$NOTE_ID/identity")" \
  | gh variable set APPLE_DEVELOPER_ID_IDENTITY --env main --repo "$REPO"
```

Verify:

```bash
gh secret   list --env main --repo aliengiraffe/vigilante
gh variable list --env main --repo aliengiraffe/vigilante
```

### Checking the certificate before you push it

Worth doing after a rotation — a bad `.p12`/password pair fails in CI with an
unhelpful error. Note that macOS ships **LibreSSL**, whose `openssl pkcs12` has no
`-legacy` flag and whose `openssl x509` cannot read the `Bag Attributes` preamble
directly, hence the `sed`:

```bash
op document get "Apple Developer ID — Lander (.p12)" --vault Engineering --out-file /tmp/c.p12
export P12PW="$(op read "op://Engineering/$NOTE_ID/p12 password")"

openssl pkcs12 -in /tmp/c.p12 -passin env:P12PW -nokeys -clcerts 2>/dev/null \
  | sed -n '/BEGIN CERTIFICATE/,/END CERTIFICATE/p' > /tmp/c.crt

openssl x509 -in /tmp/c.crt -noout -subject -enddate -issuer
openssl x509 -in /tmp/c.crt -noout -checkend 7776000   # nonzero => expires within 90 days

# Must report at least one private key; codesign cannot sign without it.
openssl pkcs12 -in /tmp/c.p12 -passin env:P12PW -nocerts -nodes 2>/dev/null | grep -c "PRIVATE KEY"

unset P12PW; rm -f /tmp/c.p12 /tmp/c.crt
```

Expect the issuer to be **`Developer ID Certification Authority`** — that
intermediate is what carries the `1.2.840.113635.100.6.2.6` marker the designated
requirement checks for.

> **Do not validate by importing into your login keychain.** If you want to
> rehearse the CI path locally, import into a throwaway keychain and remember that
> `security list-keychains -d user -s <kc>` **replaces** your search list. Save it
> first (`security list-keychains -d user`) and restore it afterwards — calling
> `-s` with no arguments empties the list and breaks login-keychain lookups until
> it is put back.

### Creating the Apple assets from scratch

Only needed if the team-wide certificate or notary key does not already exist.
Both steps require the account holder's Apple ID and MFA and cannot be automated.

1. **Developer ID Application certificate.** Keychain Access → *Certificate
   Assistant* → *Request a Certificate from a Certificate Authority* → **Saved to
   disk**. Upload the CSR at
   <https://developer.apple.com/account/resources/certificates/list> → **+** →
   **Developer ID Application** → download the `.cer` → double-click to import →
   select **both** the certificate and its private key → **Export 2 items…** as a
   `.p12` with a strong password.
2. **App Store Connect API key.**
   <https://appstoreconnect.apple.com/access/integrations/api> → **Keys** →
   generate a key with a role allowed to notarize → download the `.p8`
   (**one-time download**) → record the Key ID and the Issuer ID.

Back both up in 1Password as documents, with the passwords and IDs on the Secure
Note, then delete the local copies.

## Cutting a release

```bash
git switch main && git pull
git tag v1.2.0            # must be vX.Y.Z and already merged into main
git push origin v1.2.0
```

The `Release` workflow runs on `macos-latest`, signs and notarizes, publishes the
GitHub release, and updates the `vigilante` Homebrew cask. A second job then
publishes the PyPI artifacts (next section).

Nightlies need no action: every push to `main` republishes the rolling
`main-nightly` prerelease and the `vigilante-nightly` cask. Nightlies do not
publish to PyPI — only tagged releases do.

## PyPI wheels (`vigilante-cli`)

Every tagged release also publishes **prebuilt-binary wheels** to PyPI so
`pip install vigilante-cli`, `pipx install vigilante-cli`, and
`uv tool install vigilante-cli` place the `vigilante` binary on `PATH` with no
Go toolchain and no Python shim. The machinery lives in
[`packaging/pypi/`](../packaging/pypi/) and the `pypi` job of
[`release.yml`](../.github/workflows/release.yml).

The distribution is named **`vigilante-cli`** because the `vigilante` name on
PyPI belongs to an unrelated project (see issue #516, where the decision is
recorded). The command users run is still `vigilante` — the wheel stages the
binary in `vigilante_cli-<version>.data/scripts/`, the standard wheel location
pip copies onto `PATH` with the executable bit set.

How the job works, and the invariants it protects:

- It runs **after** the `release` job, on `ubuntu-latest`, and downloads the
  **already-published archives** with `gh release download`. The Go binary is
  **never rebuilt** during packaging: a rebuild would be unsigned, not
  notarized, and would lack the ldflags version stamp and telemetry
  configuration, and the packaging job deliberately has no access to those
  secrets.
- Ordering is intentional: by the time PyPI is attempted, the GitHub release
  and the Homebrew cask are live. A PyPI outage therefore degrades to "this
  version is missing from PyPI" — it can never abort a signed release. The
  failed job still fails the workflow run, so it is visible, and it can be
  re-run from the Actions UI once PyPI recovers.
- `packaging/pypi/build_wheels.py` builds one sdist plus one wheel per
  GoReleaser target (`macosx_11_0_arm64`, `macosx_10_13_x86_64`,
  `manylinux2014_x86_64` — honest because the binary is `CGO_ENABLED=0` and
  fully static). The PyPI version is the tag with the leading `v` stripped and
  must match the version embedded in the downloaded archive names, so a
  tag/artifact mismatch fails the job instead of publishing a mislabeled wheel.
- `packaging/pypi/check_wheel.py` gates the upload: each wheel must carry
  exactly its `py3-none-<platform>` tag, contain the binary under
  `.data/scripts/` with the executable bit recorded, contain **zero `.py`
  files** and no entry points, and say `Root-Is-Purelib: false`. The worst
  regression this guards against is a wheel silently reverting to
  `py3-none-any`, which pip would serve to every platform.
- The **sdist is a loud-failure fallback**, not a build path. On platforms
  with no matching wheel (Windows, Linux arm64) pip falls back to the sdist,
  whose build aborts with a message naming the Homebrew cask command and the
  GitHub releases page. It never installs a no-op package and never tries to
  compile Go.

### Credential: PyPI Trusted Publishing

The job authenticates with **Trusted Publishing** (OIDC) — no long-lived PyPI
token is stored anywhere. One-time maintainer setup on
<https://pypi.org/manage/account/publishing/> (or the project's *Publishing*
settings once it exists): add a trusted publisher for

| Field | Value |
|---|---|
| Owner / repository | `aliengiraffe/vigilante` |
| Workflow filename | `release.yml` |
| Environment | `main` |

That is why the `pypi` job carries `id-token: write` in a job-level
`permissions` block (the workflow default stays `contents: write` untouched)
and runs in the `main` environment. If Trusted Publishing ever has to be
replaced with an API token, store it as a `PYPI_API_TOKEN` secret in the
`main` environment and pass it to the publish action — do not widen workflow
permissions for it.

### Rehearsing and verifying

Rehearse against **Test PyPI** before the first production tag (and after any
change to `packaging/pypi/`): add a second trusted publisher on
<https://test.pypi.org>, point the publish step at it temporarily
(`repository-url: https://test.pypi.org/legacy/`), and run the install matrix
against the uploaded artifacts. Remember that a published PyPI version can
never be reused — a botched upload costs a new patch version, not a
re-upload.

The packaging machinery itself can be rehearsed locally without touching any
index:

```bash
python3 -m venv /tmp/venv && /tmp/venv/bin/pip install build
gh release download v1.2.3 --pattern 'vigilante_*.tar.gz' --dir /tmp/archives
/tmp/venv/bin/python packaging/pypi/build_wheels.py \
  --version 1.2.3 --archives /tmp/archives --out /tmp/pypi-dist
/tmp/venv/bin/python packaging/pypi/check_wheel.py \
  --dist /tmp/pypi-dist --version 1.2.3
/tmp/venv/bin/pip install /tmp/pypi-dist/vigilante_cli-1.2.3-py3-none-macosx_11_0_arm64.whl
file /tmp/venv/bin/vigilante   # Mach-O/ELF executable, not a Python script
/tmp/venv/bin/vigilante --help
```

On a macOS machine that has never seen the binary, also confirm the
pip-installed `vigilante` runs without a Gatekeeper prompt: the Developer ID
signature travels inside the wheel byte-for-byte, but a bare CLI binary's
notarization is validated online (see the stapling note above), so execution
is the only conclusive check — the same rule as for the release archives.

### Platform coverage

Wheels exist only for the current GoReleaser matrix (macOS arm64/x86_64,
Linux x86_64). Adding `linux/arm64` or Windows wheels requires adding those
targets to `.goreleaser.yml` first and is deliberately out of scope here;
`packaging/pypi/check_wheel.py` carries the target table that would need to
grow with it.

## Local validation

Signing degrades gracefully without secrets: with no Developer ID identity
available the script signs **ad-hoc** so the execution gate can still run. (An
unsigned arm64 Mach-O cannot exec on Apple Silicon at all, so "unsigned" is not a
testable state — the gate would fail for the wrong reason.)

```bash
goreleaser check

# Full snapshot: ad-hoc signs each macOS artifact and runs the execution gate.
# The OTEL_* keys must exist because the ldflags template reads them.
NIGHTLY_VERSION=0.0.0-local OTEL_ENDPOINT= OTEL_TOKEN= OTEL_URL_PATH= \
  goreleaser release --snapshot --clean

# Or run the gate by hand against one binary:
bash scripts/sign-macos-binary.sh dist/vigilante_darwin_arm64_v8.0/vigilante darwin
```

Ad-hoc-signed local artifacts are **not shippable**; the script says so in its
output. The shell scripts are covered by Go tests:

```bash
go test ./scripts/...
```

## Verifying a published binary

**Run it first.** Only execution is conclusive — every other check below passed on
`lander`'s broken releases except `codesign --verify`.

```bash
tar -xf vigilante_<version>_macOS_arm64.tar.gz

./vigilante --help                                     # (1) MUST exit 0
echo "exit=$?"                                         #     exit 137 = SIGKILL = broken

codesign --verify --deep --strict --verbose=2 vigilante # (2) MUST exit 0
codesign -d --requirements - vigilante                  # (3) expect `certificate 1[...6.2.6]`,
                                                        #     NOT `certificate root[...]`
spctl -a -vvv -t exec vigilante                         # (4) accepted, source=Notarized Developer ID
```

Do **not** treat `codesign -dv` output as verification — it prints a healthy
summary for a binary the kernel will kill, because it never evaluates the
designated requirement.

If `./vigilante --help` ever exits 137, capture the kernel's reason (needs `sudo`;
unprivileged `log show` returns nothing for AMFI):

```bash
sudo log show --last 5m --predicate \
  'sender == "AppleMobileFileIntegrity" OR eventMessage CONTAINS "code signature"' \
  --info --debug
```

Useful counter-check when triaging: re-signing ad-hoc makes *any* of these
binaries run, because an ad-hoc designated requirement is a `cdhash` literal. So
"it works after `codesign -s -`" tells you the Mach-O is fine and the **signature
policy** was what got rejected — it is not a fix.

## Known gap: `vigilante setup` re-signs the installed binary

`prepareMacOSDaemonBinary` (`internal/service/service.go`) currently strips
Gatekeeper xattrs and runs `codesign --force --sign -` on the installed binary
before loading the LaunchAgent. On a signed release that **replaces the Developer
ID signature with an ad-hoc one and voids notarization**.

The same applies to the `postflight` block that
[`scripts/update-nightly-cask.sh`](../scripts/update-nightly-cask.sh) writes into
the nightly cask.

Both were correct when every release was unsigned. Now that releases are signed,
they should become conditional — skip the repair entirely when
`codesign --verify --strict` and `spctl --assess` both pass. That work is tracked
separately from the signing pipeline itself, deliberately gated on one verified
signed release. A clean `brew install --cask` is already improved without it; what
is lost is the signature persisting after `vigilante setup` runs.

## Rotating the certificate or notary key

1. Create the replacement on developer.apple.com.
2. Update the 1Password `Engineering` items.
3. Re-run the `gh secret set` / `gh variable set` commands above.
4. Revoke the old certificate or API key once a release has succeeded with the new
   one.

Developer ID certificates are valid for several years — the Secure Note carries a
`developer portal cert expiry` field, so set a calendar reminder ahead of it.
Notary API keys do not expire but can be revoked at any time.
