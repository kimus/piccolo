## ADDED Requirements

### Requirement: Manifest declares runtime toolchain packages and commands
Piccolo SHALL support a Piccolo-owned `runtime.toolchain` section in `piccolo.yaml` containing Devbox/Nix package names and command names to expose on the container PATH.

#### Scenario: New workspace receives default toolchain
- **WHEN** `piccolo init` creates a new workspace
- **THEN** `piccolo.yaml` contains `runtime.toolchain.packages` and `runtime.toolchain.commands`
- **AND** the defaults include Poppler helpers, LibreOffice, and ImageMagick packages/commands

#### Scenario: Manifest parses strictly
- **WHEN** `piccolo.yaml` contains `runtime.toolchain`
- **THEN** Piccolo strictly decodes the packages and commands lists
- **AND** unknown fields still produce a parse error

#### Scenario: Missing toolchain remains compatible
- **WHEN** an existing workspace lacks `runtime.toolchain`
- **THEN** Piccolo treats the toolchain as the current generated default unless the user explicitly writes a custom list

### Requirement: Toolchain application exposes configured commands
Piccolo SHALL apply the configured runtime toolchain by installing configured packages with Devbox and symlinking configured commands' resolved real binaries into `/usr/local/bin`.

#### Scenario: Serve applies configured toolchain
- **WHEN** `piccolo serve` or `piccolo restart` starts a gateway container successfully
- **THEN** Piccolo applies the configured runtime toolchain after the health check
- **AND** configured commands are available on PATH inside the container

#### Scenario: Command provided by existing package is exposed
- **WHEN** `runtime.toolchain.packages` includes `poppler-utils`
- **AND** `runtime.toolchain.commands` includes `pdfinfo`, `pdftoppm`, and `pdftocairo`
- **THEN** applying the toolchain symlinks those commands into `/usr/local/bin` when Devbox exposes them

#### Scenario: Removed command stops being exposed
- **WHEN** a command previously exposed by Piccolo is removed from `runtime.toolchain.commands`
- **AND** the toolchain is applied again
- **THEN** Piccolo removes the Piccolo-owned `/usr/local/bin/<command>` symlink
- **AND** it does not delete unrelated container binaries

#### Scenario: Missing configured command is reported
- **WHEN** a configured command cannot be resolved after package installation
- **THEN** Piccolo reports the missing command as a warning or error in apply/serve output
- **AND** doctor reports the command as unavailable

### Requirement: Strong media and office defaults
The default runtime toolchain SHALL include deterministic media and office utilities sufficient for common file upload workflows.

#### Scenario: Default PDF helpers are configured
- **WHEN** Piccolo generates or defaults the runtime toolchain
- **THEN** `poppler-utils` is included in packages
- **AND** `pdftotext`, `pdfinfo`, `pdftoppm`, and `pdftocairo` are included in commands

#### Scenario: Default office conversion is configured
- **WHEN** Piccolo generates or defaults the runtime toolchain
- **THEN** `libreoffice` is included in packages
- **AND** `libreoffice` and `soffice` are included in commands

#### Scenario: Default image conversion is configured
- **WHEN** Piccolo generates or defaults the runtime toolchain
- **THEN** `imagemagick` is included in packages
- **AND** `magick`, `convert`, and `identify` are included in commands

#### Scenario: OpenCV is not a default
- **WHEN** Piccolo generates or defaults the runtime toolchain
- **THEN** Python OpenCV/`cv2` is not required as a default package
- **AND** users may add it later through custom packages if needed
