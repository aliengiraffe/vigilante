# vigilante-cli

Prebuilt-binary distribution of [Vigilante](https://github.com/aliengiraffe/vigilante),
the autonomous GitHub issue runner for headless coding agents.

This package contains no JavaScript reimplementation of Vigilante — it places
the already-signed `vigilante` Go binary for your platform on `PATH`:

```sh
npm install -g vigilante-cli
vigilante --help
```

or run it once with `npx`:

```sh
npx vigilante-cli --help
```

Prebuilt binaries exist for macOS arm64, macOS x64, and Linux x64. On other
platforms (Windows, Linux arm64) installation fails with instructions; use
Homebrew, pip, or a [GitHub release archive](https://github.com/aliengiraffe/vigilante/releases)
instead:

```sh
brew install --cask aliengiraffe/spaceship/vigilante
pipx install vigilante-cli
```

Installing this package delivers the binary only. Vigilante still requires
`git`, an authenticated `gh`, and a locally installed coding-agent CLI
(`claude` by default, or `codex`, `gemini`, `opencode`) — see the
[README](https://github.com/aliengiraffe/vigilante#install) for setup.
