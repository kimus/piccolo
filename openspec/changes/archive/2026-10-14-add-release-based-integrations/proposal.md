## Why

Installing integrations by building source packages inside the user runtime image is slow, noisy, and pulls large transient build toolchains. Piccolo should treat integrations as release-distributed packages by default while keeping source builds as an explicit developer path.

## What Changes

- **BREAKING**: `piccolo integration add <ref>` installs GitHub release artifacts by package reference instead of source-building `github:` refs through Devbox/Nix.
- **BREAKING**: short aliases such as `gws` and `gws@3.1.0` are not supported; users specify package refs such as `kimus/piccolo-gws` or `github:kimus/piccolo-gws@3.1.0`.
- Add package-ref parsing for `[provider:]owner/repo[@selector]`, defaulting provider to GitHub.
- Add release artifact resolution/download/verification for GitHub Releases.
- Add scoped integration state paths based on package owner/repo: `integrations/<owner>/<repo>`.
- Add `piccolo integration list` to show installed package, descriptor name, display name, source, channel, and resolved version.
- Change `upgrade` to advance latest-channel installed integrations without `--to`; version selection is done by re-running `add <package[@selector]>`.
- Add `piccolo integration build <package[@ref]>` as the explicit source-build/developer path using the existing Devbox/Nix mechanism.

## Capabilities

### New Capabilities
- `release-based-integrations`: Installing, listing, upgrading, and source-building package-scoped integrations from release artifacts or explicit source builds.

### Modified Capabilities

## Impact

- Affects `cmd/piccolo/integration.go`, integration reference parsing, manifest schema, runtime image generation, Docker build context creation, README docs, and integration tests.
- Existing workspaces using `runtime.integrations[].ref: github:...` need migration or clear compatibility handling because `github:` changes from source-build semantics to release-package semantics.
- Requires network access to GitHub Releases for default integration installs and upgrades.
