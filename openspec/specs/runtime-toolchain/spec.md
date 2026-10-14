# runtime-toolchain Specification

## Purpose
TBD - created by archiving change configure-runtime-toolchain. Update Purpose after archive.
## Requirements
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

