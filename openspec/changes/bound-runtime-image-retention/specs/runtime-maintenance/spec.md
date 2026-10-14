## MODIFIED Requirements

### Requirement: Image drift is reported, never repaired automatically

Piccolo SHALL compare the runtime image in use with the image the workspace currently describes, and when they differ SHALL report the difference and the command that rebuilds it, without rebuilding or removing anything on the user's behalf.

#### Scenario: Toolchain list changed after the image was built

- **WHEN** the workspace toolchain package list differs from the one baked into the image in use
- **THEN** Piccolo reports that the image is stale and names the toolchain change as the reason
- **AND** Piccolo does not start a build

#### Scenario: Integration version changed after the image was built

- **WHEN** an installed integration's recorded version or digest differs from the image in use
- **THEN** Piccolo reports the image as stale and names the integration and both versions
- **AND** Piccolo does not start a build

#### Scenario: Rebuild command is stated

- **WHEN** Piccolo reports stale runtime image
- **THEN** it names a concrete command the user can run to rebuild and adopt the current image

#### Scenario: Drift check cannot be computed

- **WHEN** the desired image cannot be determined because the daemon is unreachable, the base image is unavailable or release metadata cannot be fetched
- **THEN** Piccolo reports that image currency could not be verified
- **AND** starting or continuing to serve is not blocked by the inability to check

#### Scenario: Accumulated images are surfaced alongside drift

- **WHEN** Piccolo reports image state for a workspace with unreferenced runtime images above the reporting threshold
- **THEN** it also reports how many images are unreferenced and the command that reclaims them
- **AND** it still removes nothing
