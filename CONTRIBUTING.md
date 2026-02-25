# Contributing

Contributions are welcome! Please start a GitHub Discussion if you have a
feature you'd like to add. If you've found a bug, file an Issue or send a
PR to fix the bug.

## Build

```bash
make build
```

This writes the binary to `./kpf`.

## Git hooks

Enable repo-managed hooks:

```bash
git config core.hooksPath .githooks
```

The pre-commit hook verifies that `README.md`'s `## All options` section
matches the current kpf-specific options derived from `kpf --help`.

To refresh that section:

```bash
./scripts/update-readme-help.sh --write
```

## Release

Prerequisites:

- Clean working tree (no uncommitted changes)
- `gh` CLI installed and authenticated (`gh auth login`)
- Push access to `origin`

Choose one bump type:

```bash
tools/release.sh --major
tools/release.sh --minor
tools/release.sh --patch
```

```bash
tools/release.sh --patch
```

Use `--major`, `--minor`, or `--patch` to bump the most recent semver tag.
If no existing semver tag exists, the script creates `v0.0.0`.

The script pushes the current branch and new tag, then dispatches a manual
GitHub Actions workflow that builds `{linux,darwin} x {amd64,arm64}` artifacts
and creates a draft GitHub release (it is not published automatically).
