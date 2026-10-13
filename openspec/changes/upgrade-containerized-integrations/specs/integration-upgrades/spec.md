## ADDED Requirements

### Requirement: Upgrade installed integration
The system SHALL provide `piccolo integration upgrade <name>` to rebuild an already installed integration from its recorded source reference and select the resulting runtime image only after validation succeeds.

#### Scenario: Upgrade known integration
- **WHEN** the user runs `piccolo integration upgrade gws` and `gws` is recorded in `runtime.integrations`
- **THEN** the system rebuilds a runtime image containing `gws` from its stored ref
- **AND** the system validates the integration descriptor from the newly built image before updating `piccolo.yaml`

#### Scenario: Unknown integration
- **WHEN** the user runs `piccolo integration upgrade ghost` and `ghost` is not recorded in `runtime.integrations`
- **THEN** the command fails with an actionable error naming the missing integration
- **AND** the system does not build a new image
- **AND** the system does not modify `piccolo.yaml`

### Requirement: Preserve installed integration set
The system SHALL rebuild upgrade images from the complete installed integration set so upgrading one integration does not remove other integrations from the selected runtime image.

#### Scenario: Upgrade one of multiple integrations
- **WHEN** the manifest records integrations `gws` and `other`
- **AND** the user runs `piccolo integration upgrade gws`
- **THEN** the generated image build includes both `gws` and `other`
- **AND** the updated manifest still records both integrations

### Requirement: Upgrade all integrations
The system SHALL provide `piccolo integration upgrade --all` to rebuild the current integration image and refresh descriptor metadata for every installed integration.

#### Scenario: Upgrade all installed integrations
- **WHEN** the user runs `piccolo integration upgrade --all`
- **THEN** the system rebuilds a runtime image containing every integration recorded in `runtime.integrations`
- **AND** the system runs descriptor validation for every recorded integration from the newly built image
- **AND** the system updates `piccolo.yaml` only after all descriptors validate successfully

#### Scenario: No integrations installed
- **WHEN** the user runs `piccolo integration upgrade --all` and no integrations are recorded
- **THEN** the command fails with an actionable message explaining that no integrations are installed
- **AND** the system does not modify `piccolo.yaml`

### Requirement: Change integration source during upgrade
The system SHALL allow the selected integration source to be changed during upgrade with `--to <alias-or-ref>`.

#### Scenario: Upgrade to explicit ref
- **WHEN** the user runs `piccolo integration upgrade gws --to github:kimus/piccolo-gws?ref=v0.2.0`
- **THEN** the system resolves the provided ref using the same rules as `integration add`
- **AND** the system builds the selected integration from the resolved ref
- **AND** the system records the resolved ref in `piccolo.yaml` only after descriptor validation succeeds

#### Scenario: Upgrade to alias
- **WHEN** the user runs `piccolo integration upgrade gws --to gws`
- **THEN** the system resolves `gws` through the integration alias table
- **AND** the upgrade uses the resolved alias ref and descriptor command

### Requirement: Warn about unpinned refs
The system SHALL warn the user when an upgrade uses an unpinned integration ref whose resolved code may change between builds.

#### Scenario: Unpinned stored ref
- **WHEN** the selected integration has an unpinned stored ref
- **AND** the user runs `piccolo integration upgrade <name>`
- **THEN** the command prints a warning that the rebuild may resolve different integration code

#### Scenario: Unpinned target ref
- **WHEN** the user runs `piccolo integration upgrade <name> --to <alias-or-ref>` and the resolved target ref is unpinned
- **THEN** the command prints a warning that future rebuilds may resolve different integration code

### Requirement: Safe manifest update
The system SHALL update the workspace manifest only after the new image is built and all required descriptor validations succeed.

#### Scenario: Build fails
- **WHEN** the integration upgrade image build fails
- **THEN** the command returns a non-zero error
- **AND** the manifest's `container.image` remains unchanged
- **AND** the manifest's `runtime.integrations` metadata remains unchanged

#### Scenario: Descriptor validation fails
- **WHEN** the new image builds successfully but a required descriptor command fails or returns invalid JSON
- **THEN** the command returns a non-zero error
- **AND** the manifest's `container.image` remains unchanged
- **AND** the manifest's `runtime.integrations` metadata remains unchanged

### Requirement: Preserve integration state
The system SHALL preserve existing integration state and SHALL NOT run integration setup automatically during upgrade.

#### Scenario: Upgrade preserves state directory
- **WHEN** the user upgrades `gws`
- **THEN** files under `<workspace>/integrations/gws/` remain in place
- **AND** the upgrade command does not delete or recreate the state directory

#### Scenario: Setup is not automatic
- **WHEN** an integration upgrade succeeds
- **THEN** the system does not run the descriptor-declared setup command automatically
- **AND** the success output tells the user to run `piccolo integration setup <name>` only when reconfiguration is needed
- **AND** the success output tells the user to run `piccolo restart` to use the new image

### Requirement: Use structured mounts for descriptor validation
The system SHALL run upgrade descriptor validation in a one-shot container with the structured workspace mounts required by integration commands.

#### Scenario: Descriptor container has integration state mount
- **WHEN** the system validates an upgraded integration descriptor
- **THEN** the descriptor container has `<workspace>/picoclaw` mounted to `/root/.picoclaw`
- **AND** the descriptor container has `<workspace>/integrations` mounted to `/root/.piccolo/integrations`

#### Scenario: Descriptor container preserves image Nix store
- **WHEN** the system validates an upgraded integration descriptor
- **THEN** the descriptor container does not mount `<workspace>/runtime/nix` over `/nix`
