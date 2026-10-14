## ADDED Requirements

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

### Requirement: Image store must not be shadowed at runtime

Piccolo SHALL NOT bind-mount any host directory over the container's Nix store, so the store baked into the image remains visible to every container started from it.

#### Scenario: Binaries baked into the image stay executable

- **WHEN** Piccolo runs a container from a locally built runtime image
- **THEN** the integration executables baked into that image are executable
- **AND** no host cache mount hides the store paths they depend on

#### Scenario: One-shot and gateway see the same store

- **WHEN** Piccolo compares an integration one-shot container and the gateway container built from the same image
- **THEN** both can execute the same integration binaries

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

Piccolo SHALL label each locally built runtime image with its base image, its toolchain, and one entry per installed integration at resolved version and artifact digest.

#### Scenario: Image identifies what it contains

- **WHEN** a runtime image is built
- **THEN** inspecting the image reports its base image, a toolchain identifier, and one labelled entry per installed integration including version and digest

#### Scenario: Installed integration versions are queryable

- **WHEN** a user lists locally built runtime images
- **THEN** they can determine which images contain a given integration at a given version without inspecting the image's filesystem

#### Scenario: Labels travel with the container

- **WHEN** a container is started from a labelled runtime image and later renamed
- **THEN** the container still reports the same content labels as its image
