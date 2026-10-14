## ADDED Requirements
### Requirement: Runtime status reports toolchain state
Piccolo SHALL provide a runtime status view that summarizes configured packages, configured commands, exposed-command availability, and persisted runtime volume/cache information when available.

#### Scenario: Status shows available commands
- **WHEN** a workspace has a configured runtime toolchain
- **AND** the gateway container is running
- **THEN** runtime status reports which configured commands are available on PATH inside the container

#### Scenario: Status reports missing commands
- **WHEN** a configured command is not available after applying the runtime toolchain
- **THEN** runtime status identifies the missing command
- **AND** suggests applying/restarting or checking the package/command configuration

#### Scenario: Status works without running container
- **WHEN** the gateway container is not running
- **THEN** runtime status still reports the configured packages and commands from `piccolo.yaml`
- **AND** reports that live command availability could not be checked

### Requirement: Runtime apply reports whether the image provides the toolchain

Piccolo SHALL compare the toolchain the workspace configures with the toolchain the running image records, report a mismatch with the reason, and name the rebuild command. Piccolo SHALL NOT install packages into a running container: the toolchain is image content, and installing at runtime would recreate the two-sources-of-truth problem the image-owned toolchain removes.

#### Scenario: Toolchain does not match the running image

- **WHEN** the configured toolchain differs from what the running image recorded
- **THEN** Piccolo reports the mismatch and names the rebuild command
- **AND** it starts no package installation
- **AND** it performs no build on the user's behalf

#### Scenario: Toolchain matches

- **WHEN** the configured toolchain equals what the running image records
- **THEN** Piccolo reports that the running image matches the configuration

#### Scenario: Image is not available locally

- **WHEN** the configured runtime image is not present locally
- **THEN** Piccolo refuses and names the command that builds it
