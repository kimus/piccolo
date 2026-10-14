## ADDED Requirements

### Requirement: Integration identity resolvable without artifact download

Piccolo SHALL be able to determine an installed release integration's resolved version, artifact name and digest from release metadata alone, without transferring the artifact.

#### Scenario: Resolve then decide

- **WHEN** Piccolo needs to know which release of an integration a workspace refers to
- **THEN** it resolves the version, artifact name and digest from the release manifest
- **AND** it downloads the artifact only after a build is actually required

#### Scenario: Installed integration unchanged

- **WHEN** the resolved version and digest of an installed integration match what the workspace already records
- **THEN** Piccolo reports the integration as already installed at that version
- **AND** it downloads no artifact, starts no container and rebuilds no image

#### Scenario: Planning a rebuild needs no bulk transfer

- **WHEN** Piccolo assembles the set of integrations that must be present in a rebuilt image
- **THEN** it obtains each integration's identity from release metadata
- **AND** it downloads an artifact only for an integration whose content must actually be placed in the build context

### Requirement: Integration state path derived from package reference

Piccolo SHALL keep an installed integration's recorded identity consistent with the image content it contributed to, so a version change always produces a distinguishable record.

#### Scenario: Version change is recorded

- **WHEN** an installed integration is moved to a different version
- **THEN** the workspace records the new resolved version and artifact digest
- **AND** the rebuilt image tag differs from the previous one

#### Scenario: Stale record detected

- **WHEN** the recorded integration identity does not match the image in use
- **THEN** Piccolo can report the mismatch without starting any container
