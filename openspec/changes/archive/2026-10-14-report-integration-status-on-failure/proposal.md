## Why

`piccolo integration status <package>` surfaces the integration's report only when the status command exits 0. An integration legitimately exits non-zero to report failed checks, so today the useful provider-specific report is thrown away behind `Error: status integration gws: command exited 1:` with no output.

## What Changes

- Parse and render the status payload whenever stdout is a valid `piccolo.integration.status.v1` document, regardless of exit code.
- Report the exit code as a degraded/unhealthy signal instead of treating it as a transport failure.
- Keep failing the command when output is absent or unparseable (that is a real invocation failure), preserving the current message shape.
- JSON output mode keeps emitting the parsed payload rather than an error.

## Capabilities

### New Capabilities

- `integration-status-reporting`: Piccolo renders integration-owned status reports even when the integration exits non-zero to signal failing checks.

### Modified Capabilities

## Impact

- `cmd/piccolo/integration.go`: status path decodes stdout before judging the exit code.
- Tests: cover non-zero exit with valid payload, zero exit with garbage payload, and `--json` behavior.
- README: document that a failed check set is a report, not a Piccolo error.
