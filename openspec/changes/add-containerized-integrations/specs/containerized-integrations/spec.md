## ADDED Requirements

### Requirement: Add integration package to gateway image
The system SHALL provide `piccolo integration add <alias-or-ref>` to install a self-describing integration package into a newly built gateway image before the PicoClaw gateway starts.

#### Scenario: Add known alias
- **WHEN** the user runs `piccolo integration add gws`
- **THEN** the system resolves `gws` to the configured Google Workspace integration reference and shows the resolved source before building

#### Scenario: Add explicit reference
- **WHEN** the user runs `piccolo integration add github:kimus/piccolo-gws`
- **THEN** the system uses that reference as the integration package source

#### Scenario: Build is confirmed
- **WHEN** the resolved integration source is shown
- **THEN** the system asks for confirmation before building an image that includes that integration code

#### Scenario: Image contains integration before gateway startup
- **WHEN** the add command succeeds
- **THEN** the selected gateway image contains the integration command before `picoclaw gateway` starts

### Requirement: Discover integration descriptor
The system SHALL discover integration behavior by running the installed integration command's `describe --json` operation inside a container created from the built image.

#### Scenario: Descriptor is valid
- **WHEN** the descriptor command returns JSON with a supported schema version
- **THEN** the system records the descriptor summary as installed integration metadata

#### Scenario: Descriptor is invalid
- **WHEN** the descriptor command fails or returns an unsupported schema version
- **THEN** the add command fails without updating the workspace to use the new image

#### Scenario: Descriptor command convention
- **WHEN** the integration source is an explicit repository reference and no alias supplies a command name
- **THEN** the system derives the descriptor command from the repository basename and runs `<basename> describe --json`

### Requirement: Record Piccolo-owned integration metadata
The system SHALL store installed integration metadata in `piccolo.yaml` without duplicating provider-specific PicoClaw runtime configuration.

#### Scenario: Metadata is recorded
- **WHEN** an integration is added successfully
- **THEN** `piccolo.yaml` records the integration name, source reference, and installed command metadata under Piccolo-owned runtime integration metadata

#### Scenario: Provider configuration remains integration-owned
- **WHEN** an integration needs to modify `config.json`
- **THEN** the add command does not perform that modification directly

### Requirement: Run integration setup in one-shot container
The system SHALL provide `piccolo integration setup <name>` to run the integration-owned setup command in a temporary container created from the configured gateway image.

#### Scenario: Setup runs with mounted workspace
- **WHEN** the user runs `piccolo integration setup gws`
- **THEN** the system runs the descriptor-declared setup command with the workspace bind-mounted at `/root/.picoclaw`

#### Scenario: Setup uses configured image and environment
- **WHEN** setup starts
- **THEN** the system uses the currently configured gateway image and resolves the manifest environment for the setup container

#### Scenario: Setup overrides gateway entrypoint
- **WHEN** the setup container starts
- **THEN** it runs the integration setup command instead of the PicoClaw gateway entrypoint

#### Scenario: Setup may change config
- **WHEN** the integration setup command writes `config.json` or integration state files under the mounted workspace
- **THEN** those changes persist after the setup container exits

#### Scenario: Restart is requested after setup
- **WHEN** setup completes successfully
- **THEN** the system tells the user to run `piccolo restart` to apply the integration image and configuration changes

### Requirement: Integration add does not require live gateway
The system SHALL allow adding and setting up an integration without an already-running gateway container.

#### Scenario: Gateway is stopped during add
- **WHEN** no gateway container is running
- **THEN** `piccolo integration add <alias-or-ref>` can still build and select the integration image

#### Scenario: Gateway is stopped during setup
- **WHEN** no gateway container is running
- **THEN** `piccolo integration setup <name>` can still run the setup command in a temporary container
