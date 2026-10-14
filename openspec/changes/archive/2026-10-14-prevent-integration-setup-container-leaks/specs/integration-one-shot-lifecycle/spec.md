## ADDED Requirements

### Requirement: Name integration one-shot containers deterministically

Piccolo SHALL give integration setup and status one-shot containers a stable, predictable name derived from the integration, and SHALL NOT embed a per-run unique suffix that makes previous containers unfindable.

#### Scenario: Setup container name is stable
- **WHEN** Piccolo starts a setup one-shot for an installed integration
- **THEN** the container name is derived from the integration name with a fixed Piccolo setup prefix
- **AND** a second setup run for the same integration uses the same name

#### Scenario: Status containers do not collide with setup containers
- **WHEN** Piccolo starts a status one-shot
- **THEN** its name carries a distinct status sub-prefix
- **AND** reclaiming abandoned setup containers cannot remove a status container, or vice versa

### Requirement: Reclaim abandoned one-shot containers before starting

Piccolo SHALL detect and remove Piccolo-managed one-shot containers left behind by a previous interrupted run before starting a new one, and SHALL tell the user what it removed.

#### Scenario: Legacy orphan is reclaimed
- **WHEN** a leftover setup container from an older Piccolo version matches the setup prefix for that integration
- **THEN** Piccolo removes it before creating the new container
- **AND** reports the number and names of removed containers

#### Scenario: Non-Piccolo containers are never reclaimed
- **WHEN** Piccolo sweeps for abandoned one-shots
- **THEN** only containers whose names carry the Piccolo setup prefix are removed
- **AND** the gateway container and unrelated user containers are untouched

#### Scenario: Reclaimed port no longer blocks the run
- **WHEN** the abandoned container had published a host port required by the new run
- **THEN** the new setup container starts successfully instead of failing with a port allocation error

### Requirement: Clean up one-shot containers on interrupt

Piccolo SHALL stop and remove setup and status one-shot containers when the user interrupts the command (SIGINT or SIGTERM), rather than leaving them running.

#### Scenario: Ctrl-C during setup
- **WHEN** the user interrupts while a setup one-shot is running
- **THEN** the container is stopped and removed before Piccolo exits
- **AND** the host terminal is restored to its previous mode

### Requirement: Request a container PTY only when the host has a terminal

Piccolo SHALL create the one-shot with a PTY only when the integration declares a TTY requirement and the host standard input is actually a terminal. Otherwise Piccolo SHALL run the integration's non-interactive path so the container terminates.

#### Scenario: Piped stdin with a TTY-requiring integration
- **WHEN** the integration declares it needs a TTY but Piccolo's standard input is not a terminal
- **THEN** Piccolo starts the container without a PTY
- **AND** states that setup is running non-interactively
- **AND** the command completes instead of blocking forever on a terminal read

#### Scenario: Real terminal
- **WHEN** the integration declares a TTY requirement and Piccolo's standard input is a terminal
- **THEN** Piccolo creates the container with a PTY, puts the host terminal in raw mode for the duration, and restores it afterwards

### Requirement: Propagate host terminal size to interactive one-shots

When Piccolo runs an interactive one-shot with a PTY, it SHALL keep the container PTY size synchronized with the host terminal so full-screen prompts render correctly.

#### Scenario: Initial size applied
- **WHEN** an interactive one-shot starts on a real terminal
- **THEN** Piccolo sets the container PTY to the host terminal's dimensions

#### Scenario: Window resized during the session
- **WHEN** the user resizes the host terminal while the interactive one-shot is running
- **THEN** Piccolo propagates the new size to the container PTY

### Requirement: Report a vanished container legibly

Piccolo SHALL report a one-shot container that disappears before finishing as an explicit, human-readable condition rather than surfacing a raw daemon error.

#### Scenario: Container removed externally
- **WHEN** the one-shot container is removed by another Piccolo invocation or manual action before Piccolo observes its exit
- **THEN** Piccolo reports that the container disappeared and names the likely cause
- **AND** does not print an unexplained `No such container` failure

### Requirement: Warn about abandoned one-shot containers in doctor

`piccolo doctor` SHALL warn, without failing, when Piccolo-managed one-shot containers are still present.

#### Scenario: Orphan detected
- **WHEN** a Piccolo setup one-shot container exists while doctor runs
- **THEN** doctor emits a warning naming the containers and stating they are removed on the next setup or status run
- **AND** doctor still exits zero when nothing else fails

#### Scenario: No orphans
- **WHEN** no Piccolo one-shot containers exist
- **THEN** doctor emits no warning for this check
