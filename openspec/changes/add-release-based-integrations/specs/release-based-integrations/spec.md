## ADDED Requirements

### Requirement: Parse package integration references
The system SHALL accept integration package references using the grammar `[provider:]owner/repo[@selector]`, defaulting `provider` to `github` when omitted.

#### Scenario: Parse default GitHub package
- **WHEN** the user runs `piccolo integration add kimus/piccolo-gws`
- **THEN** the system resolves the package identity as `github:kimus/piccolo-gws`
- **AND** the selected release channel is `latest`

#### Scenario: Parse explicit provider package
- **WHEN** the user runs `piccolo integration add github:kimus/piccolo-gws@3.1.0`
- **THEN** the system resolves the package identity as `github:kimus/piccolo-gws`
- **AND** the selected release selector is `3.1.0`

#### Scenario: Preserve provider-owned selector
- **WHEN** the user runs `piccolo integration add johndoe/piccolo-maker@v2.1.10`
- **THEN** the system resolves the selector as the exact string `v2.1.10`
- **AND** the system does not add or remove a `v` prefix

#### Scenario: Reject short aliases
- **WHEN** the user runs `piccolo integration add gws`
- **THEN** the command fails with guidance to use a package reference such as `kimus/piccolo-gws`

### Requirement: Install integration release artifacts
The system SHALL install integrations from verified GitHub Release artifacts when `piccolo integration add <package-ref>` is used.

#### Scenario: Install latest release
- **WHEN** the user runs `piccolo integration add kimus/piccolo-gws`
- **THEN** the system resolves the latest non-draft, non-prerelease GitHub Release for `kimus/piccolo-gws`
- **AND** downloads the release manifest
- **AND** downloads the artifact matching the target Linux runtime platform
- **AND** verifies the artifact digest before building the runtime image

#### Scenario: Install exact release selector
- **WHEN** the user runs `piccolo integration add kimus/piccolo-gws@3.1.0`
- **THEN** the system resolves GitHub Release tag `3.1.0`
- **AND** installs the matching verified artifact

#### Scenario: Missing release manifest
- **WHEN** the selected release does not contain a supported Piccolo integration release manifest
- **THEN** the add command fails without updating `piccolo.yaml`

#### Scenario: Digest mismatch
- **WHEN** the downloaded artifact digest does not match the release manifest
- **THEN** the add command fails without building or selecting a new runtime image

### Requirement: Validate installed release descriptors
The system SHALL validate integration descriptors from the built runtime image before updating the workspace manifest.

#### Scenario: Descriptor validates after release install
- **WHEN** a release artifact has been copied into the runtime image
- **THEN** Piccolo runs the integration descriptor command from that image
- **AND** updates `piccolo.yaml` only after descriptor parsing and validation succeed

#### Scenario: Descriptor validation fails
- **WHEN** the installed release artifact's descriptor command fails or returns invalid JSON
- **THEN** the command fails
- **AND** `piccolo.yaml` remains unchanged

### Requirement: Preserve package-scoped integration state
The system SHALL store integration state under paths derived from package owner and repository, not descriptor names.

#### Scenario: State path for GitHub package
- **WHEN** package `github:kimus/piccolo-gws` is installed
- **THEN** the host state path is `<workspace>/integrations/kimus/piccolo-gws`
- **AND** the container state path is `/root/.piccolo/integrations/kimus/piccolo-gws`

#### Scenario: Version change preserves state
- **WHEN** `kimus/piccolo-gws@3.1.0` is installed
- **AND** the user runs `piccolo integration add kimus/piccolo-gws@3.2.0`
- **THEN** files under `<workspace>/integrations/kimus/piccolo-gws` remain in place
- **AND** setup is not run automatically

### Requirement: List installed integrations
The system SHALL provide `piccolo integration list` to display installed integrations from local manifest metadata without performing live provider connectivity checks.

#### Scenario: List human-readable integrations
- **WHEN** one integration is installed
- **THEN** `piccolo integration list` prints package, descriptor name, display name, source, channel, and resolved version

#### Scenario: List JSON integrations
- **WHEN** the user runs `piccolo integration list --json`
- **THEN** the command emits JSON containing package identity, requested ref, source, channel, selector, resolved version, digest, state path, descriptor name, display name, and commands

#### Scenario: No integrations installed
- **WHEN** no integrations are installed
- **THEN** `piccolo integration list` prints an empty-state message and exits successfully

### Requirement: Upgrade latest-channel integrations
The system SHALL advance installed latest-channel release integrations with `piccolo integration upgrade` while leaving fixed selector installs pinned.

#### Scenario: Upgrade one latest-channel integration
- **WHEN** `kimus/piccolo-gws` is installed with channel `latest`
- **AND** the user runs `piccolo integration upgrade kimus/piccolo-gws`
- **THEN** Piccolo resolves the latest GitHub Release
- **AND** installs and validates it when it differs from the installed resolved version or digest

#### Scenario: Fixed selector remains pinned
- **WHEN** `kimus/piccolo-gws@3.1.0` is installed with channel `fixed`
- **AND** the user runs `piccolo integration upgrade kimus/piccolo-gws`
- **THEN** Piccolo does not move to another release
- **AND** prints guidance to run `piccolo integration add kimus/piccolo-gws@<selector>` to change versions or `piccolo integration add kimus/piccolo-gws` to track latest

#### Scenario: Upgrade all latest-channel integrations
- **WHEN** the user runs `piccolo integration upgrade` without an integration argument
- **THEN** Piccolo attempts to upgrade every installed latest-channel release integration
- **AND** leaves fixed selector integrations unchanged

### Requirement: Build integrations from source explicitly
The system SHALL provide `piccolo integration build <package-ref>` as the explicit source-build path using Devbox/Nix source installation.

#### Scenario: Build source package
- **WHEN** the user runs `piccolo integration build kimus/piccolo-gws@main`
- **THEN** Piccolo builds the integration from source using the existing Devbox/Nix mechanism
- **AND** records the integration source as `build`

#### Scenario: Warn about build dependencies
- **WHEN** the user runs `piccolo integration build <package-ref>`
- **THEN** Piccolo warns that source builds can download compilers and build dependencies
- **AND** recommends `piccolo integration add <package-ref>` for normal release installs
