# Contribute to Vigilante

Thanks for helping improve `vigilante`.

This guide is intentionally short. For setup, command reference, and deeper operational detail, start with [README.md](README.md) and [DOCS.md](DOCS.md).

## Common Ways To Contribute

- Report bugs or confusing behavior by opening a GitHub issue with clear repro steps, expected behavior, and actual behavior.
- Propose features or workflow changes by opening an issue that explains the problem, the desired outcome, and any constraints.
- Open a pull request for focused code or docs changes. Small, easy-to-review changes are the simplest path.

## Opening A Change

If you want to send a patch:

1. Fork the repository or create a branch from the main repository if you already have access.
2. Make the smallest change that solves the problem.
3. Run the relevant local validation for the area you touched.
4. Open a pull request with a concise summary of what changed and how you verified it.

If you need local setup first, follow the install and development guidance in [README.md](README.md) and [DOCS.md](DOCS.md).

## Using Vigilante To Contribute To Vigilante

If you want to use Vigilante itself while contributing back to this project, you can use Vigilante's forks workflow so the tool works from your fork and opens changes back toward `aliengiraffe/vigilante`.

Example:

1. Fork `aliengiraffe/vigilante` to your own GitHub account and clone your fork locally.
2. Run `vigilante watch /path/to/your/vigilante-fork` so Vigilante operates from that forked checkout.
3. Let Vigilante prepare the issue branch, make the change, and open the pull request from your fork back to `aliengiraffe/vigilante`.

That gives you the same normal contributor flow, but with Vigilante doing the work from your fork instead of from the main repository checkout.

## Keep It Simple

- Prefer narrow pull requests over large refactors.
- Update nearby docs when behavior changes.
- When in doubt, open an issue first and align on scope before building something larger.
