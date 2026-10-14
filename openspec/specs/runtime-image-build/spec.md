# runtime-image-build Specification

## Purpose
TBD - created by archiving change add-custom-runtime-image-build. Update Purpose after archive.
## Requirements
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

### Requirement: Toolchain installed into the runtime image

Piccolo SHALL install the configured runtime toolchain into the runtime image during the image build, in a layer that precedes the integration layers, so a container started from the image already contains the toolchain without any start-time installation.

#### Scenario: Gateway starts with the toolchain present

- **WHEN** Piccolo starts the gateway container from a locally built runtime image
- **THEN** the toolchain programs declared for the workspace are executable inside that container
- **AND** no start-time tool installation step is required for them to be usable

#### Scenario: Integration change reuses the toolchain layer

- **WHEN** an integration is added, upgraded or removed and the runtime image is rebuilt without changing the toolchain package list
- **THEN** the toolchain layer is not re-executed
- **AND** only the layers after it are rebuilt

#### Scenario: Toolchain change invalidates its own layer

- **WHEN** the workspace toolchain package list changes and the runtime image is rebuilt
- **THEN** the toolchain layer is rebuilt together with the layers that follow it

#### Scenario: A configured command the packages do not provide

- **WHEN** the workspace lists a toolchain command that no configured package provides
- **THEN** the image build fails rather than producing an image that advertises a tool it cannot run

### Requirement: Image store must not be shadowed at runtime

Piccolo SHALL NOT bind-mount any host directory over the container's Nix store, so the store baked into the image remains visible to every container started from it.

#### Scenario: Binaries baked into the image stay executable

- **WHEN** Piccolo runs a container from a locally built runtime image
- **THEN** the integration executables baked into that image are executable
- **AND** no host cache mount hides the store paths they depend on

#### Scenario: One-shot and gateway see the same store

- **WHEN** Piccolo compares an integration one-shot container and the gateway container built from the same image
- **THEN** both can execute the same integration binaries

### Requirement: A failed image build fails the command

Piccolo SHALL report an image build as failed when the build does not produce the requested tag, and SHALL NOT record or adopt an image that was not created.

#### Scenario: A build step fails but the daemon reports success

- **WHEN** a layer fails during an image build and the daemon's build stream otherwise completes without an explicit error
- **THEN** the build command exits non-zero
- **AND** no workspace configuration is updated to reference an image that does not exist

### Requirement: Runtime image tag includes installed integration identity

Piccolo SHALL include the identity of every installed release integration — package, resolved version and artifact digest — in the content-derived runtime image tag, and SHALL derive that identity from release metadata without downloading integration artifacts.

#### Scenario: Different versions produce different tags

- **WHEN** the same workspace installs two different versions of one integration
- **THEN** the computed desired image tag differs between them
- **AND** the recorded resolved version matches the content of the image the tag names

#### Scenario: Identity does not depend on install order

- **WHEN** two workspaces install the same set of integrations in different orders
- **THEN** they compute the same desired image tag

#### Scenario: Planning never downloads artifacts

- **WHEN** Piccolo computes the desired image tag while serving or checking a workspace
- **THEN** it uses release metadata only
- **AND** no integration artifact is downloaded as part of that computation

### Requirement: Integration bundle paths named by package

Piccolo SHALL place each release integration's bundle in the build context under a path derived from its package reference rather than a positional index.

#### Scenario: Adding an integration leaves others stable

- **WHEN** an additional release integration is installed alongside an existing one
- **THEN** the existing integration's bundle path in the build context is unchanged
- **AND** layers belonging to unrelated integrations are not invalidated merely because another integration was added

### Requirement: Image content recorded as labels

Piccolo SHALL label each locally built runtime image with its base image, its toolchain, and one entry per installed release integration at resolved version and artifact digest.

#### Scenario: The recorded identity is what the build verified

- **WHEN** an integration release is installed, or an image is rebuilt and adopted
- **THEN** the workspace record for that integration carries the version, artifact digest and command list the image was actually built from
- **AND** recomputing the desired image from that record yields the tag that was just built

#### Scenario: Image identifies what it contains

- **WHEN** a runtime image is built
- **THEN** inspecting the image reports its base image, a toolchain identifier, and one labelled entry per installed integration including version and digest

#### Scenario: Installed integration versions are queryable

- **WHEN** a user lists locally built runtime images
- **THEN** they can determine which images contain a given integration at a given version without inspecting the image's filesystem

#### Scenario: Labels travel with the container

- **WHEN** a container is started from a labelled runtime image and later renamed
- **THEN** the container still reports the same content labels as its image

### Requirement: Rebuild command composes the whole workspace image

Piccolo's image build SHALL produce the image the workspace describes — toolchain plus every installed integration — so that the command a staleness warning names is the command that actually resolves it.

#### Scenario: Rebuilding an integration workspace

- **WHEN** a user runs the image build in a workspace with installed integrations
- **THEN** the built image contains the configured toolchain and every installed integration
- **AND** the build reports the base, tag, toolchain summary and each integration identity it is baking

#### Scenario: Rebuild leaves the workspace configuration alone unless adopted

- **WHEN** an image build succeeds and the user did not ask to adopt it
- **THEN** `piccolo.yaml` still references the previous image
- **AND** Piccolo prints the value to adopt

