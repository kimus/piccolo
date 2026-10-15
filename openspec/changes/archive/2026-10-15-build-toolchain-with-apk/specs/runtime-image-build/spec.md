## ADDED Requirements

### Requirement: Toolchain installs with the base image package manager

The runtime image SHALL install its toolchain using the base image's own package manager, and SHALL NOT install or depend on a separate package system inside the image.

#### Scenario: Packages installed by the base distribution

- **WHEN** the runtime image is built
- **THEN** the configured toolchain packages are installed by the base image's package manager
- **AND** their programs are on the default PATH without any additional environment setup

#### Scenario: No separate package system in the image

- **WHEN** the runtime image build inputs are generated
- **THEN** no installer, configuration file, profile path, or garbage-collection step for a secondary package system appears in the Dockerfile
- **AND** no build context file is generated for one

#### Scenario: Bootstrap tools limited to what the build needs

- **WHEN** the Dockerfile installs its bootstrap layer
- **THEN** it includes only tools required to build and run the runtime
- **AND** it does not include packages needed solely by a removed packaging system

#### Scenario: A version can be pinned by naming it

- **WHEN** a toolchain package entry includes an explicit version
- **THEN** that entry is passed to the package manager unchanged
- **AND** the build resolves exactly that version or fails

## REMOVED Requirements

### Requirement: Devbox support in custom image

**Reason**: Devbox and Nix are no longer used to install the runtime toolchain; the image's own package manager does it, at roughly one sixth of the size for the same advertised commands.

**Migration**: nothing to do in the workspace. `runtime.toolchain.packages` now holds apk package names — rename entries that used nixpkgs attribute names (`tesseract` → `tesseract-ocr` plus `tesseract-ocr-data-eng`, `yq-go` → `yq`, `libxml2` → `libxml2-utils`), then rebuild with `piccolo image build --set` and `piccolo restart`.
