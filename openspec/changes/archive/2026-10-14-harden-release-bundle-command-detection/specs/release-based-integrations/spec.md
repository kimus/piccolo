## ADDED Requirements

### Requirement: Integration commands exclude packaging residue

Piccolo SHALL derive an installed release integration's command list from the programs the artifact provides, excluding filesystem metadata that packaging tools add alongside them.

#### Scenario: macOS resource fork in the bundle

- **WHEN** an integration archive contains `bin/piccolo-x` and `bin/._piccolo-x`
- **THEN** the derived command list is `piccolo-x` only
- **AND** the sidecar is not recorded as a command the image must provide

#### Scenario: Sidecar is not installed onto PATH

- **WHEN** the runtime image installs a bundle whose `bin/` contains packaging residue
- **THEN** the generated install step skips those entries
- **AND** only real programs are placed on PATH

#### Scenario: Build no longer fails on metadata

- **WHEN** a bundle carrying an AppleDouble sidecar is installed
- **THEN** the image build succeeds
- **AND** it does not verify the existence of a resource fork
