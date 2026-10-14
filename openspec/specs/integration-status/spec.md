# integration-status Specification

## Purpose
TBD - created by archiving change add-integration-status. Update Purpose after archive.
## Requirements
### Requirement: Run integration status command
The system SHALL provide `piccolo integration status <name>` to run an installed integration's descriptor-declared status command and present the result.

#### Scenario: Status command succeeds
- **WHEN** the user runs `piccolo integration status gws`
- **AND** `gws` is recorded in `runtime.integrations` with a status command
- **THEN** the system runs that status command in a temporary container created from the configured runtime image
- **AND** the system renders the returned checks in human-readable form

#### Scenario: Integration is not installed
- **WHEN** the user runs `piccolo integration status ghost`
- **AND** `ghost` is not recorded in `runtime.integrations`
- **THEN** the command fails with an actionable error naming the missing integration

#### Scenario: Integration has no status command
- **WHEN** the user runs `piccolo integration status gws`
- **AND** `gws` is installed but has no recorded status command
- **THEN** the command reports that status is unsupported for the installed integration
- **AND** the command suggests upgrading or reinstalling the integration

### Requirement: Run status for all integrations
The system SHALL provide `piccolo integration status --all` to check every installed integration that supports status.

#### Scenario: Status all integrations
- **WHEN** the user runs `piccolo integration status --all`
- **THEN** the system runs status checks for every integration recorded in `runtime.integrations`
- **AND** the system renders one status section per integration

#### Scenario: No integrations installed
- **WHEN** the user runs `piccolo integration status --all` and no integrations are recorded
- **THEN** the command fails with an actionable message explaining that no integrations are installed

### Requirement: Parse integration status JSON
The system SHALL parse and validate integration status output using a generic provider-neutral schema.

#### Scenario: Valid status JSON
- **WHEN** a status command emits JSON with schema `piccolo.integration.status.v1`
- **AND** the JSON includes an integration name, overall status, summary, and checks
- **THEN** Piccolo validates the status values and displays the checks

#### Scenario: Invalid status JSON
- **WHEN** a status command exits successfully but emits invalid status JSON
- **THEN** Piccolo reports a status parsing error for that integration
- **AND** the command exits non-zero

#### Scenario: JSON output requested
- **WHEN** the user runs `piccolo integration status gws --json`
- **THEN** Piccolo emits the parsed status JSON without human formatting

#### Scenario: All JSON output requested
- **WHEN** the user runs `piccolo integration status --all --json`
- **THEN** Piccolo emits an aggregate JSON document containing every integration result

### Requirement: Descriptor records status command
The system SHALL support integration descriptors that declare a `commands.status` command and SHALL record that command in Piccolo-owned integration metadata.

#### Scenario: Descriptor includes status command
- **WHEN** an integration descriptor contains `commands.status`
- **THEN** Piccolo records the status command argv in `piccolo.yaml` for that integration

#### Scenario: Descriptor lacks status command
- **WHEN** an integration descriptor does not contain `commands.status`
- **THEN** Piccolo still accepts the descriptor if all existing required fields are valid
- **AND** status is reported as unsupported until the integration is upgraded to provide a status command

### Requirement: Use structured one-shot container mounts
The system SHALL run status commands in a temporary container with the structured integration mounts.

#### Scenario: Status container mounts state
- **WHEN** Piccolo runs an integration status command
- **THEN** the status container has `<workspace>/picoclaw` mounted to `/root/.picoclaw`
- **AND** the status container has `<workspace>/integrations` mounted to `/root/.piccolo/integrations`

#### Scenario: Status container preserves image Nix store
- **WHEN** Piccolo runs an integration status command
- **THEN** the status container mounts no host directory over `/nix`

### Requirement: Exit code reflects status
The system SHALL return a non-zero exit code when any checked integration reports an overall `fail` status or when status execution/parsing fails.

#### Scenario: Overall status ok
- **WHEN** all checked integrations report overall status `ok` or `warn`
- **THEN** the command exits successfully

#### Scenario: Overall status fail
- **WHEN** any checked integration reports overall status `fail`
- **THEN** the command exits non-zero

