# integration-status-reporting Specification

## Purpose
TBD - created by archiving change report-integration-status-on-failure. Update Purpose after archive.
## Requirements
### Requirement: Render integration status reports regardless of exit code

Piccolo SHALL parse and render an integration-owned status report whenever the status command emits a valid `piccolo.integration.status.v1` payload on stdout, including when the command exits non-zero to signal failing checks.

#### Scenario: Failing checks with a valid report
- **WHEN** an integration status command exits non-zero and stdout contains a valid status payload
- **THEN** Piccolo prints the report's overall status and each check with its message and remediation
- **AND** Piccolo does not print an opaque "command exited" error in place of the report

#### Scenario: Unhealthy exit is still visible
- **WHEN** Piccolo renders a status report from a non-zero exit
- **THEN** the output makes clear the run was unhealthy, using the report status or the exit code
- **AND** the Piccolo command itself exits non-zero

#### Scenario: Exit code without a report
- **WHEN** an integration status command exits non-zero and stdout is empty or not a valid status payload
- **THEN** Piccolo reports an invocation failure including the exit code and stderr

#### Scenario: Healthy run renders normally
- **WHEN** an integration status command exits zero with a valid status payload
- **THEN** Piccolo renders the report and exits zero

### Requirement: Machine-readable status survives failing checks

Piccolo SHALL emit the parsed status payload as JSON when `integration status --json` is requested, regardless of the integration's exit code, as long as the payload is valid.

#### Scenario: JSON output with failing checks
- **WHEN** the user runs `piccolo integration status <package> --json` and the integration exits non-zero with a valid payload
- **THEN** Piccolo prints the status payload as JSON on stdout
- **AND** Piccolo still exits non-zero to signal the failed checks

#### Scenario: JSON output without a parseable payload
- **WHEN** `--json` is requested but stdout is not a valid status payload
- **THEN** Piccolo reports an invocation failure rather than emitting malformed JSON

