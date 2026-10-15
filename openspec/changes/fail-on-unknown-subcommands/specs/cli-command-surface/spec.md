## ADDED Requirements

### Requirement: Command groups report unknown subcommands as errors

A Piccolo command group SHALL fail, with a non-zero exit status, when its argument selects no subcommand, and SHALL succeed with usage listing when invoked bare.

#### Scenario: Typo in a subcommand name

- **WHEN** a user runs a group command with an argument that matches no subcommand
- **THEN** Piccolo reports an unknown-command error naming the group and the argument
- **AND** the command exits non-zero

#### Scenario: Bare group invocation

- **WHEN** a user runs a group command with no argument
- **THEN** Piccolo lists the available subcommands
- **AND** the command exits zero

#### Scenario: Near miss suggests the intended verb

- **WHEN** the unknown argument is close to exactly one subcommand name
- **THEN** the error names that subcommand as a suggestion

### Requirement: Retired commands refuse rather than disappear

When a subcommand is removed, Piccolo SHALL keep the verb reachable so that it fails with a stated reason and a replacement action, and SHALL NOT advertise it as available.

#### Scenario: Removed verb invoked

- **WHEN** a user runs a retired subcommand
- **THEN** Piccolo explains that the command has been removed and why
- **AND** it names the command to use instead
- **AND** the exit status is non-zero

#### Scenario: Removed verb not advertised

- **WHEN** a user lists the subcommands of the group that owns a retired verb
- **THEN** the retired verb is absent from the listing

#### Scenario: Typo of a removed verb

- **WHEN** an unknown argument is closest to a retired verb
- **THEN** the suggestion names that verb, so following it reaches the removal explanation
