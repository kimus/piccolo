## MODIFIED Requirements

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
