# runtime-maintenance Specification

## Purpose
TBD - created by archiving change configure-runtime-toolchain. Update Purpose after archive.
## Requirements
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

### Requirement: Image drift is reported, never repaired automatically

Piccolo SHALL compare the runtime image in use with the image the workspace currently describes, and when they differ SHALL report the difference and the command that rebuilds it, without rebuilding or restarting anything on the user's behalf.

#### Scenario: Toolchain list changed after the image was built

- **WHEN** the workspace toolchain package list differs from the one baked into the image in use
- **THEN** Piccolo reports that the image is stale and names the toolchain change as the reason
- **AND** Piccolo does not start a build

#### Scenario: Integration version changed after the image was built

- **WHEN** an installed integration's recorded version or digest differs from the image in use
- **THEN** Piccolo reports the image as stale and names the integration and both versions
- **AND** Piccolo does not start a build

#### Scenario: Only the build recipe changed

- **WHEN** the image in use records the same content identity as the workspace describes, but under a different tag
- **THEN** Piccolo does not report the image as stale
- **AND** it says only that the tag differs because Piccolo's build recipe changed

#### Scenario: The same problem is not reported twice

- **WHEN** the configured image is simply not present locally
- **THEN** Piccolo reports the missing image once, as a missing image
- **AND** it does not also report content drift for that image

#### Scenario: Rebuild command is stated

- **WHEN** Piccolo reports stale runtime image
- **THEN** it names a concrete command the user can run to rebuild and adopt the current image

#### Scenario: Drift check cannot be computed

- **WHEN** the desired image cannot be determined because the daemon is unreachable, the base image is unavailable or release metadata cannot be fetched
- **THEN** Piccolo reports that image currency could not be verified
- **AND** starting or continuing to serve is not blocked by the inability to check

### Requirement: Start-time tool installation is not part of serving

Piccolo SHALL NOT install runtime tools while starting the gateway, and SHALL NOT depend on a mounted host cache to make baked-in tools available.

#### Scenario: Gateway startup performs no package installation

- **WHEN** Piccolo starts the gateway container
- **THEN** it does not run a package or tool installation step
- **AND** the tools available in the container are exactly those the image was built with

#### Scenario: First start is not privileged

- **WHEN** the gateway is started for the first time from a freshly built image and again after a restart
- **THEN** the set of available toolchain programs is identical in both cases

### Requirement: No runtime cache maintenance commands

Piccolo SHALL NOT offer commands that maintain a runtime toolchain cache, because the toolchain is image content: garbage collection inside a running container reclaims nothing durable, and the writable layer is discarded when the container is recreated.

#### Scenario: Cache garbage collection is not offered

- **WHEN** a user looks for a way to reclaim toolchain space
- **THEN** Piccolo offers no container-side Nix garbage-collection command
- **AND** reclamation is expressed at the image level, where the space actually lives

#### Scenario: Cache directory removal is not offered

- **WHEN** a workspace still holds cache directories from an earlier layout
- **THEN** Piccolo provides no command that deletes them
- **AND** documentation states that they are unused leftovers

### Requirement: Generated agents describe the toolchain as image content

Piccolo's generated file-handling guidance SHALL state that the toolchain comes from the runtime image, and SHALL NOT instruct an agent to run a start-time installer that no longer exists.

#### Scenario: Newly generated workspace

- **WHEN** Piccolo generates a workspace
- **THEN** the file-handling skill contains no reference to a runtime tool-setup script
- **AND** it says a missing tool means the image was built without it

#### Scenario: Guard test covers the prose, not only the Dockerfile

- **WHEN** the runtime image or toolchain lifecycle changes again
- **THEN** a test asserts the generated guidance matches the new lifecycle

