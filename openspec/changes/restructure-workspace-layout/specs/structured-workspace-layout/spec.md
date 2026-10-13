## ADDED Requirements

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
The system SHALL mount structured workspace directories into the gateway and setup containers at stable in-container paths.

#### Scenario: PicoClaw home mount
- **WHEN** Piccolo starts the gateway container
- **THEN** it bind-mounts `<workspace>/picoclaw` to `/root/.picoclaw`

#### Scenario: Integration state mount
- **WHEN** Piccolo starts a gateway or integration setup container
- **THEN** it bind-mounts `<workspace>/integrations` to `/root/.piccolo/integrations`

#### Scenario: Runtime cache mounts
- **WHEN** Piccolo starts a local runtime image
- **THEN** it bind-mounts `<workspace>/runtime/devbox` to `/root/.local/share/devbox/global/default`, `<workspace>/runtime/nix` to `/nix`, and `<workspace>/runtime/media` to `/tmp/picoclaw_media`

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

### Requirement: Keep runtime cache visible and gitignored
The system SHALL store runtime cache/state under top-level `runtime/` and ensure it is not committed to source control by default.

#### Scenario: Runtime cache directories
- **WHEN** a workspace is initialized or migrated
- **THEN** Piccolo creates or expects `runtime/devbox`, `runtime/nix`, and `runtime/media`

#### Scenario: Gitignore runtime cache
- **WHEN** Piccolo creates or updates `.gitignore`
- **THEN** it ignores `runtime/` cache contents and integration auth state

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
