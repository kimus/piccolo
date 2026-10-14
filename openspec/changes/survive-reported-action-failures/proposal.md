## Why

`check_connectivity` reported that Drive is not enabled for the project — a true, useful answer — and Piccolo ended the session with `Error: integration action failed: … (connectivity_failed)`. The protocol defines `error` as one of five **events**, so the integration had reported an outcome; the transport turned it into an exception. A user who loses the menu also loses any config change an earlier action earned.

## What Changes

- An action's ending is classified by whether it **spoke**: a terminal event plus a non-zero exit is a report, silence plus a non-zero exit is a crash, and a context Piccolo cancelled is neither.
- The menu survives all three. State is re-read each pass, so a partial action becomes visible instead of being buried by an ended session.
- A config proposal from a completed action outlives a later failure, crash, or abandonment.
- A closing summary lists unfinished actions and the command exits 0; only Piccolo-side faults (daemon unreachable, container will not start) still abort.
- Severity is shown in colour — green ok, amber for an integration's report, red for Piccolo's own fault, faint for a cancel — with text markers retained so nothing depends on colour.
- `integration status` finally prints the remediation link it already parsed.

## Capabilities

### New Capabilities

### Modified Capabilities

- `integration-setup-actions`: outcome classification, session survival, patch preservation, severity display

## Impact

- `cmd/piccolo/integration_setup.go`, `cmd/piccolo/integration_courier.go` (cancel wording), `cmd/piccolo/style.go` (new), `cmd/piccolo/doctor.go`, `cmd/piccolo/integration.go`
- Tests: outcome classification table, summary filtering, styler behaviour with colour on/off, remediation link rendering
- Verified live against a real failing action: the menu returned, no `Error:` was printed
