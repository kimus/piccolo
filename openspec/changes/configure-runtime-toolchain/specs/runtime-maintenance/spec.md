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

### Requirement: Runtime apply reconciles the configured toolchain
Piccolo SHALL provide an explicit command to apply the configured runtime toolchain to a running gateway container without requiring a full image rebuild.

#### Scenario: Apply updates running container
- **WHEN** the gateway container is running
- **AND** the user runs the runtime apply command
- **THEN** Piccolo installs/reconciles configured packages
- **AND** updates Piccolo-owned command symlinks in the container

#### Scenario: Apply reports missing container
- **WHEN** the gateway container is not running
- **AND** the user runs the runtime apply command
- **THEN** Piccolo reports that `serve` or `restart` is needed before applying to a container

### Requirement: Runtime cleanup is explicit
Piccolo SHALL distinguish safe toolchain apply from cache cleanup and destructive volume reset.

#### Scenario: Apply does not delete cache by default
- **WHEN** a package is removed from `runtime.toolchain.packages`
- **AND** the toolchain is applied
- **THEN** Piccolo does not delete persisted Nix store data by default

#### Scenario: Prune is explicit
- **WHEN** the user invokes runtime prune
- **THEN** Piccolo attempts supported Devbox/Nix garbage collection or reports that pruning is unsupported
- **AND** it does not remove the runtime volumes wholesale

#### Scenario: Reset cache is explicit and destructive
- **WHEN** the user invokes runtime reset-cache and confirms the operation
- **THEN** Piccolo removes the persisted Devbox/Nix runtime volumes used for toolchain state
- **AND** the next `serve` or `restart` reinstalls the configured toolchain from scratch
