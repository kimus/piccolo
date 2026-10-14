## MODIFIED Requirements

### Requirement: Publish Piccolo CLI release binaries
The project SHALL publish Piccolo CLI binaries as GitHub Release assets for supported host platforms when a bare version tag is pushed, and SHALL stamp each published binary with the release it belongs to.

#### Scenario: Release tag uses bare version
- **WHEN** tag `0.2.0` is pushed
- **THEN** the release workflow builds Piccolo release artifacts
- **AND** the GitHub Release name/tag is `0.2.0`
- **AND** no `v` prefix is added

#### Scenario: Build supported platforms
- **WHEN** the release workflow runs
- **THEN** it builds archives for `darwin/arm64`, `darwin/amd64`, `linux/arm64`, and `linux/amd64`

#### Scenario: Publish checksums
- **WHEN** release assets are uploaded
- **THEN** a checksum file containing SHA-256 digests for every archive is uploaded with the release

#### Scenario: Published binary identifies its release
- **WHEN** `piccolo --version` is run from an archive published by the release workflow
- **THEN** the output names the release tag and the build timestamp
- **AND** it does not report a development default

### Requirement: Provide static install script
The project SHALL provide a static install script suitable for `curl | sh` installation from GitHub Pages, and SHALL verify that the documented URL actually serves it.

#### Scenario: Install latest version
- **WHEN** the user runs `curl -fsSL https://kimus.github.io/piccolo/install.sh | sh`
- **THEN** the script resolves the latest Piccolo GitHub Release
- **AND** downloads the matching archive for the host OS and architecture
- **AND** verifies its checksum before installing the `piccolo` binary

#### Scenario: Install requested version
- **WHEN** the user runs `curl -fsSL https://kimus.github.io/piccolo/install.sh | sh -s -- --version 0.1.1`
- **THEN** the script downloads release `0.1.1`
- **AND** installs that exact version after checksum verification

#### Scenario: Unsupported platform
- **WHEN** the host OS or architecture does not have a matching release asset
- **THEN** the install script exits non-zero with an actionable unsupported-platform message

#### Scenario: Documented endpoint is live
- **WHEN** a user requests the documented install URL or the landing page
- **THEN** both respond successfully rather than not-found
- **AND** the landing page shows the same command the README documents
