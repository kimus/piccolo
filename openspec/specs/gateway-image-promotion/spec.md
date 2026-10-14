# gateway-image-promotion Specification

## Purpose
TBD - created by archiving change promote-gateway-image-with-revert. Update Purpose after archive.
## Requirements
### Requirement: Promote the gateway image through a gated candidate

Piccolo SHALL promote a gateway image by stopping the running gateway, keeping it as a revert target under a reserved name, starting the target image under the canonical name, and only adopting it after it passes the readiness gate.

#### Scenario: Healthy promotion adopts the new image

- **WHEN** Piccolo promotes the gateway to a target image and that container passes the readiness gate
- **THEN** the previous container is removed
- **AND** the gateway keeps running on the canonical name with its published ports unchanged
- **AND** no container remains under the reserved previous name

#### Scenario: The canonical name is freed without discarding the previous gateway

- **WHEN** Piccolo stops the current gateway in order to promote a replacement
- **THEN** the stopped container is retained under a reserved name rather than removed
- **AND** the replacement is started under the canonical name

#### Scenario: A retained gateway is moved aside, not recreated

- **WHEN** Piccolo renames the parked container to free or restore the canonical name
- **THEN** the container keeps its image, mounts, environment and published port configuration
- **AND** starting it later restores exactly that known-good configuration

### Requirement: Readiness gate executes the integration binaries

Piccolo SHALL consider a gateway promotion successful only when the health endpoint answers and every installed integration's binary can be executed inside the running container, and SHALL treat a binary that is present but not executable as a failure.

#### Scenario: Health answers but the integration cannot execute

- **WHEN** a promoted gateway answers its health endpoint but an installed integration binary fails to execute inside it
- **THEN** the promotion is treated as failed
- **AND** the report names the binary, the probe command and the observed output

#### Scenario: Presence is not proof

- **WHEN** a check only proves that an integration binary exists on the container's PATH
- **THEN** that alone does not satisfy the readiness gate

#### Scenario: Workspace without integrations

- **WHEN** a workspace has no installed integrations
- **THEN** the readiness gate consists of the health endpoint alone

### Requirement: Failed promotion reverts to the previous gateway

When a promotion fails, Piccolo SHALL capture the diagnostics of the failed attempt, remove the failed container, restore the previously running gateway, and exit with a failure status.

#### Scenario: Automatic revert after a failed gate

- **WHEN** the candidate gateway fails the readiness gate
- **THEN** the candidate is stopped and removed, and the reserved container is renamed back to the canonical name and started
- **AND** Piccolo reports the failure with a non-zero exit status

#### Scenario: Diagnostics outlive the failed container

- **WHEN** Piccolo removes a candidate container that failed its gate
- **THEN** the report already contains that attempt's gate results, health response and recent log output

#### Scenario: Revert itself cannot restore service

- **WHEN** the previously running container cannot be started again, for example because its image no longer exists or its published ports are taken
- **THEN** Piccolo states that neither the new nor the previous gateway is running
- **AND** it names the concrete next step rather than reporting a successful rollback

### Requirement: Interrupted promotion is detected and repaired

Piccolo SHALL detect a gateway left mid-promotion by a previous interrupted run and restore service before doing anything else.

#### Scenario: Stray reserved container found

- **WHEN** Piccolo starts and finds a reserved previous container while nothing runs under the canonical name
- **THEN** it restores that container under the canonical name, starts it, and reports that a prior promotion was interrupted

#### Scenario: Reserved container with a healthy canonical gateway

- **WHEN** the canonical gateway is running and healthy while a reserved previous container also exists
- **THEN** Piccolo reports the leftover and continues serving without disturbing the running gateway

### Requirement: Roll back to the previously running image

Piccolo SHALL offer a rollback that returns a workspace to the image it was running before the last successful promotion, routed through the same gated promotion sequence.

#### Scenario: Explicit rollback

- **WHEN** the user asks to roll back after a promotion that was healthy but wrong
- **THEN** Piccolo resolves the previously running image, verifies it is still available, and promotes it through the same readiness gate
- **AND** it reports the target image together with the base and integration content recorded for it

#### Scenario: Rollback target no longer available

- **WHEN** the previously running image is no longer present locally
- **THEN** Piccolo refuses the rollback and says so before stopping anything

#### Scenario: Nothing to roll back to

- **WHEN** no previous image has been recorded for the workspace
- **THEN** Piccolo reports that there is nothing to roll back to and changes nothing

### Requirement: Promotion never builds an image implicitly

Piccolo SHALL promote only among images that already exist locally, and SHALL keep rebuilding a stale image an explicit, user-initiated action.

#### Scenario: Target image is missing

- **WHEN** a promotion or rollback needs an image that is not available locally
- **THEN** Piccolo stops before touching the running gateway and names the command that would build it

