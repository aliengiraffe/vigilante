# vigilante-cli

Prebuilt-binary distribution of [Vigilante](https://github.com/aliengiraffe/vigilante),
the autonomous GitHub issue runner for headless coding agents.

Each wheel contains no Python code — it ships the already-signed `vigilante`
Go binary for its platform, which pip places on `PATH`:

```sh
pipx install vigilante-cli   # or: pip install / uv tool install
vigilante --help
```

Prebuilt wheels exist for macOS arm64, macOS x86_64, and Linux x86_64. On
other platforms (Windows, Linux arm64) installation fails with instructions;
use Homebrew or a [GitHub release archive](https://github.com/aliengiraffe/vigilante/releases)
instead:

```sh
brew install --cask aliengiraffe/spaceship/vigilante
```

Installing the package delivers the binary only. Vigilante still requires
`git`, an authenticated `gh`, and a locally installed coding-agent CLI
(`claude` by default, or `codex`, `gemini`, `opencode`) — see the
[README](https://github.com/aliengiraffe/vigilante#install) for setup.
