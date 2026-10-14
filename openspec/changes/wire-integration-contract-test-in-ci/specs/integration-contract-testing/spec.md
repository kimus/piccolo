## ADDED Requirements

### Requirement: Contract test runs in continuous integration

Piccolo's CI SHALL build a runtime image and run the cross-repo integration contract test against it, so that a change which breaks the argv contract between Piccolo and an installed integration fails CI rather than a later live session.

#### Scenario: Derived argv regression fails CI

- **WHEN** a change makes Piccolo derive integration command arguments the shipped binary rejects
- **THEN** the contract test fails in CI
- **AND** the failure names the command and the rejection it produced

#### Scenario: Image is built by the same generator workspaces use

- **WHEN** the contract job prepares its image
- **THEN** the image comes from Piccolo's own build path
- **AND** the test is not run against a hand-written Dockerfile

### Requirement: The CI image is sized for the question it answers

The contract job SHALL build a runtime image with a reduced toolchain, since the test verifies command acceptance and bundle layout rather than the full tool set, and the complete default toolchain is orders of magnitude larger than the test requires.

#### Scenario: Minimal toolchain build

- **WHEN** the contract job builds its image
- **THEN** it requests a package list and command list of one entry each
- **AND** the resulting image excludes the office and media tooling the default list installs

#### Scenario: Reduced command list is expressible

- **WHEN** a user builds a runtime image and passes one or more `--command` values
- **THEN** those commands replace the configured or default command list
- **AND** the build verifies exactly that set, and fails if a package does not provide one

#### Scenario: A prebuilt image still works

- **WHEN** the contract test is given an already-built image through its environment variable
- **THEN** it runs against that image without building anything

### Requirement: Contract coverage is scoped and pinned

The contract job SHALL run when inputs it protects change and on a schedule, and SHALL install a pinned integration version so an unrelated release in another repository cannot break Piccolo's CI.

#### Scenario: Unrelated change does not pay for a build

- **WHEN** a push changes only files the contract does not exercise
- **THEN** the contract job does not run

#### Scenario: Upstream drift is caught without a Piccolo change

- **WHEN** the scheduled run executes
- **THEN** the contract test runs against the pinned integration release
- **AND** a change in the integration's accepted interface is reported even though Piccolo did not change

#### Scenario: Pinned version keeps CI attributable

- **WHEN** the contract job installs the integration under test
- **THEN** it names a specific version rather than the latest release
