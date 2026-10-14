# piccolo-cli-releases Specification

## Purpose
TBD - created by archiving change release-piccolo-cli. Update Purpose after archive.
## Requirements
### Requirement: Publish Piccolo CLI release binaries
The project SHALL publish Piccolo CLI binaries as GitHub Release assets for supported host platforms when a bare version tag is pushed.

#### Scenario: Release tag uses bare version
- **WHEN** tag `3.1.0` is pushed
- **THEN** the release workflow builds Piccolo release artifacts
- **AND** the GitHub Release name/tag is `3.1.0`
- **AND** no `v` prefix is added

#### Scenario: Build supported platforms
- **WHEN** the release workflow runs
- **THEN** it builds archives for `darwin/arm64`, `darwin/amd64`, `linux/arm64`, and `linux/amd64`

#### Scenario: Publish checksums
- **WHEN** release assets are uploaded
- **THEN** a checksum file containing SHA-256 digests for every archive is uploaded with the release

### Requirement: Provide static install script
The project SHALL provide a static install script suitable for `curl | sh` installation from GitHub Pages.

#### Scenario: Install latest version
- **WHEN** the user runs `curl -fsSL https://kimus.github.io/piccolo/install.sh | sh`
- **THEN** the script resolves the latest Piccolo GitHub Release
- **AND** downloads the matching archive for the host OS and architecture
- **AND** verifies its checksum before installing the `piccolo` binary

#### Scenario: Install requested version
- **WHEN** the user runs `curl -fsSL https://kimus.github.io/piccolo/install.sh | sh -s -- --version 3.1.0`
- **THEN** the script downloads release `3.1.0`
- **AND** installs that exact version after checksum verification

#### Scenario: Unsupported platform
- **WHEN** the host OS or architecture does not have a matching release asset
- **THEN** the install script exits non-zero with an actionable unsupported-platform message

### Requirement: Install only host Piccolo CLI
The installer SHALL install only the host `piccolo` CLI binary and SHALL NOT install container runtime dependencies or integration packages.

#### Scenario: Installer completes
- **WHEN** the install script succeeds
- **THEN** the selected install directory contains the `piccolo` binary
- **AND** Docker, PicoClaw, Devbox, Nix, and integrations are not installed by the script

#### Scenario: Install directory is not on PATH
- **WHEN** the chosen install directory is not on the user's PATH
- **THEN** the script prints instructions for adding it to PATH

### Requirement: Document binary installation
The README SHALL document the release installer, pinned version installation, and manual download/checksum verification path.

#### Scenario: User reads installation docs
- **WHEN** a user opens the README installation section
- **THEN** it includes the curl installer command
- **AND** the pinned version example
- **AND** a note that the installer only installs the host Piccolo CLI

