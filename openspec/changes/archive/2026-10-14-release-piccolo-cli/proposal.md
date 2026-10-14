## Why

Users should be able to install Piccolo without cloning the repository or building from source. GitHub Release binaries plus a small static install page provide a product-ready distribution path for the Piccolo host CLI.

## What Changes

- Add GitHub Actions release workflow for Piccolo CLI binaries.
- Publish GitHub Release assets for supported host OS/architecture combinations.
- Use bare version tags such as `3.1.0`; no `v` prefix in tags or release names.
- Publish checksums for all release assets.
- Add a simple GitHub Pages static site with a `curl | sh` installer that installs the latest or requested Piccolo version from GitHub Releases.
- Document install, version pinning, and checksum verification behavior.

## Capabilities

### New Capabilities
- `piccolo-cli-releases`: Building, publishing, and installing Piccolo CLI release binaries.

### Modified Capabilities

## Impact

- Adds GitHub Actions workflows and release packaging configuration.
- Adds static site/install script files and README install documentation.
- Does not change runtime container behavior, PicoClaw packaging, or integration state.
