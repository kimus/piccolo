## ADDED Requirements

### Requirement: Toolchain size is selectable at build time

Piccolo SHALL let a runtime image be built with an explicit command list as well as an explicit package list, so a build can be composed with the smallest toolchain that still exercises the real code path.

#### Scenario: Reduced command list

- **WHEN** an image build is given one or more explicit commands
- **THEN** those commands replace the workspace or default command list
- **AND** the build verifies exactly that set and fails if a package does not provide one

#### Scenario: Reduced package list with unchanged commands

- **WHEN** an image build replaces the package list without replacing the command list
- **THEN** the build fails naming the commands no configured package provides
- **AND** it does not silently produce an image missing tools the workspace advertises

#### Scenario: No override preserves current behaviour

- **WHEN** neither list is overridden
- **THEN** the build composes the toolchain from `piccolo.yaml` and the built-in defaults
