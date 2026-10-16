# structured-workspace-layout Specification

## Purpose
TBD - created by archiving change restructure-workspace-layout. Update Purpose after archive.
## Requirements
### Requirement: Use structured workspace layout
The system SHALL organize each Piccolo workspace into explicit top-level areas for Piccolo launch config, PicoClaw home data, integration state, and runtime cache/state.

#### Scenario: Fresh workspace layout
- **WHEN** `piccolo init` creates a new workspace
- **THEN** the workspace contains `piccolo.yaml`, `picoclaw/config.json`, `picoclaw/agents/`, `integrations/`, and `runtime/` directories

#### Scenario: Piccolo launch file remains top-level
- **WHEN** a command resolves a Piccolo workspace
- **THEN** it finds `piccolo.yaml` at the workspace root

#### Scenario: PicoClaw config location
- **WHEN** Piccolo reads or writes PicoClaw runtime configuration
- **THEN** it uses `<workspace>/picoclaw/config.json`

### Requirement: Document Piccolo workspace directory
The system SHALL document the Piccolo workspace directory layout in `README.md`, including ownership and host-to-container mount paths.

#### Scenario: README shows workspace tree
- **WHEN** a user reads the workspace layout section in `README.md`
- **THEN** it shows `piccolo.yaml`, `picoclaw/`, `integrations/`, and `runtime/` at the workspace root

#### Scenario: README explains ownership
- **WHEN** a user reads the workspace layout section in `README.md`
- **THEN** it explains that `piccolo.yaml` is Piccolo-owned, `picoclaw/` is PicoClaw home, `integrations/` is integration-owned state, and `runtime/` is local runtime cache/state

#### Scenario: README explains mounts
- **WHEN** a user reads the workspace layout section in `README.md`
- **THEN** it documents that `picoclaw/` mounts to `/root/.picoclaw`, `integrations/` mounts to `/root/.piccolo/integrations`, and runtime cache directories mount to their container paths

### Requirement: Mount structured directories into container

Piccolo SHALL mount the workspace's PicoClaw home and integration state into the gateway container at their container-side locations, and SHALL mount only the media cache. No host directory SHALL be mounted over the container's Nix store, because doing so hides the store baked into the runtime image.

#### Scenario: PicoClaw home mount

- **WHEN** Piccolo starts the gateway container
- **THEN** it bind-mounts `<workspace>/picoclaw` to `/root/.picoclaw`

#### Scenario: Integration state mount

- **WHEN** Piccolo starts the gateway container
- **THEN** it bind-mounts `<workspace>/integrations` to `/root/.piccolo/integrations`

#### Scenario: Media mirror mount

- **WHEN** Piccolo starts a gateway container from a locally built runtime image
- **THEN** it bind-mounts `<workspace>/runtime/media` to `/tmp/picoclaw_media` and to each agent workspace's `media/`
- **AND** it mounts nothing over `/nix`

### Requirement: Keep integration state outside PicoClaw home on host
The system SHALL store integration durable state under the top-level `integrations/` directory on the host, using a child directory whose name matches the integration descriptor name.

#### Scenario: Integration state directory matches integration name
- **WHEN** an integration descriptor has name `gws`
- **THEN** its host state directory is `<workspace>/integrations/gws/`

#### Scenario: Google Workspace integration state
- **WHEN** the `gws` integration setup writes auth or backend state
- **THEN** the files persist under `<workspace>/integrations/gws/` on the host

#### Scenario: In-container Piccolo integration path
- **WHEN** the `gws` integration uses `/root/.piccolo/integrations/gws` inside the container
- **THEN** it reads and writes the host path `<workspace>/integrations/gws`

### Requirement: Keep the media cache visible and gitignored
Piccolo SHALL create only the runtime cache it actually mounts, and SHALL keep it git-ignored, along with credentials, PicoClaw runtime files, operating-system directory metadata, and the workspace's encryption key. Because the ignore file is written during init, Piccolo SHALL also make missing required entries detectable without modifying anything.

#### Scenario: Runtime cache directories
- **WHEN** Piccolo creates or migrates a workspace
- **THEN** it creates `runtime/media` under the workspace
- **AND** it does not create `runtime/devbox` or `runtime/nix`

#### Scenario: Gitignore runtime cache
- **WHEN** the generated `.gitignore` is written or updated
- **THEN** `runtime/` content is ignored so caches and secrets never enter version control

#### Scenario: Gitignore OS directory metadata
- **WHEN** the generated `.gitignore` is written or updated
- **THEN** Finder/Explorer metadata such as `.DS_Store` is ignored at any depth
- **AND** one rule covers the workspace root and its nested directories

#### Scenario: Gitignore the encryption key
- **WHEN** a workspace has an encryption key under `picoclaw/keys/`
- **THEN** that directory is covered by the generated `.gitignore`
- **AND** the entry is appended without rewriting lines the user added themselves
- **AND** `piccolo doctor` reports the entry as missing when it is absent

#### Scenario: An existing workspace can be healed without losing user content
- **WHEN** a required ignore entry is missing from a workspace's `.gitignore`
- **THEN** appending it leaves every existing line, including the user's own rules, intact
- **AND** repeating the append changes nothing

#### Scenario: Missing entries are reported, not silently absent
- **WHEN** a workspace's `.gitignore` lacks one or more required entries
- **THEN** `piccolo doctor` reports the missing entries and how to fix them
- **AND** reports them as a warning rather than a failure
- **AND** the report stays silent once the entries are present

#### Scenario: Older workspaces keep their leftovers
- **WHEN** a workspace still contains `runtime/devbox` or `runtime/nix` from an earlier layout
- **THEN** Piccolo neither mounts nor deletes them

### Requirement: Migrate flat workspaces
The system SHALL provide an explicit migration path from the previous flat workspace layout to the structured layout.

#### Scenario: Detect flat workspace
- **WHEN** `piccolo doctor` runs on a workspace with top-level `config.json` and `agents/`
- **THEN** it reports that the workspace uses the legacy flat layout and requires migration

#### Scenario: Migrate PicoClaw files
- **WHEN** the migration command runs and is confirmed
- **THEN** it moves known PicoClaw files and directories such as `config.json`, `agents/`, `skills/`, `auth.json`, `workspace/`, and `.picoclaw.pid` under `picoclaw/`

#### Scenario: Preserve top-level integrations
- **WHEN** the legacy workspace already has top-level `integrations/`
- **THEN** migration preserves it as the structured integration directory

#### Scenario: Avoid overwriting user data
- **WHEN** a migration destination path already exists
- **THEN** the migration command does not overwrite it without explicit confirmation

### Requirement: Centralize workspace path resolution
The system SHALL use a shared layout helper for all workspace paths that differ between host and container.

#### Scenario: Command reads config through layout helper
- **WHEN** any command reads PicoClaw `config.json`
- **THEN** it uses the layout helper's config path instead of joining the workspace root with `config.json`

#### Scenario: Docker spec uses layout helper
- **WHEN** Piccolo composes a gateway or setup container spec
- **THEN** it uses the layout helper to determine bind mount sources and container destinations

