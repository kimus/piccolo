## ADDED Requirements

### Requirement: Local custom runtime image build
Piccolo SHALL provide a command that builds a local custom PicoClaw runtime image for use by Piccolo workspaces.

#### Scenario: Build local image
- **WHEN** the user runs the runtime image build command in a Piccolo workspace
- **THEN** Piccolo builds a local Docker image derived from the pinned PicoClaw base image
- **AND** the built image tag is deterministic and pinned, not `latest`
- **AND** the build output identifies the image tag to use in `piccolo.yaml`

#### Scenario: Docker daemon unavailable
- **WHEN** the user runs the runtime image build command and the Docker daemon is not reachable
- **THEN** Piccolo fails with an actionable error
- **AND** no workspace files are modified

### Requirement: Devbox support in custom image
The custom runtime image SHALL include Devbox support suitable for running common file-handling tools.

#### Scenario: Devbox available in image
- **WHEN** the custom image build succeeds
- **THEN** containers created from the image can run `devbox` from the default shell environment

#### Scenario: File-handling Devbox config generated
- **WHEN** Piccolo prepares the custom image build context
- **THEN** it includes a `devbox.json` with common file-handling utilities for QR/barcode decoding, file type detection, structured data parsing, PDF extraction, OCR, metadata inspection, media inspection, SQLite, and archives

### Requirement: Explicit manifest adoption
Piccolo SHALL only update `piccolo.yaml` to use the custom image after explicit user consent or an explicit non-interactive flag.

#### Scenario: User accepts manifest update
- **WHEN** the image build succeeds
- **AND** the user confirms adopting the image
- **THEN** Piccolo updates `piccolo.yaml` `container.image` to the built local image tag
- **AND** preserves unrelated manifest fields

#### Scenario: User declines manifest update
- **WHEN** the image build succeeds
- **AND** the user declines adopting the image
- **THEN** Piccolo leaves `piccolo.yaml` unchanged
- **AND** prints the manual edit needed to use the image

### Requirement: Runtime image build is local-first
Piccolo SHALL NOT require a public Piccolo image registry for the custom runtime image build.

#### Scenario: Offline registry-independent build plan
- **WHEN** Piccolo builds the custom runtime image
- **THEN** the command uses the configured base PicoClaw image and local generated build context
- **AND** it does not require pulling a Piccolo-maintained image

### Requirement: Media path limitation is surfaced
Piccolo SHALL surface that custom image tools solve tool availability but do not automatically bypass PicoClaw exec path restrictions for uploaded media.

#### Scenario: Build command completion message
- **WHEN** the custom image build command completes
- **THEN** Piccolo explains that uploaded media may need to be copied or made available inside the agent workspace before exec can process it
- **AND** it does not claim that the image alone guarantees Discord attachment decoding

#### Scenario: Documentation covers media path restriction
- **WHEN** users read the runtime image build documentation
- **THEN** it explains the distinction between tool availability and exec access to uploaded media paths such as `/tmp/picoclaw_media`
