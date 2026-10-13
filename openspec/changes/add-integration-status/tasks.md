## 1. Status data model

- [x] 1.1 Add descriptor parsing support for structured `commands.status` metadata.
- [x] 1.2 Add `status_command` to manifest integration metadata with strict YAML parsing/writing.
- [x] 1.3 Add provider-neutral status result structs for `piccolo.integration.status.v1`.
- [x] 1.4 Validate allowed check statuses: `ok`, `warn`, `fail`, `unknown`, and `skip`.

## 2. CLI command

- [x] 2.1 Add `piccolo integration status [name]` subcommand.
- [x] 2.2 Add `--all` argument validation matching upgrade command style.
- [x] 2.3 Add `--json` output mode for single and all status checks.
- [x] 2.4 Return actionable errors for unknown integrations and empty installed integration lists.
- [x] 2.5 Report unsupported status when an installed integration lacks a status command.

## 3. Status execution

- [x] 3.1 Run status commands in one-shot containers from the configured runtime image.
- [x] 3.2 Use structured mounts: `picoclaw/` and `integrations/` only.
- [x] 3.3 Ensure status execution does not mount `runtime/nix` over `/nix`.
- [x] 3.4 Parse status command stdout as status JSON and surface stderr on command failure.
- [x] 3.5 Implement exit-code behavior: fail on any `fail` result or execution/parsing error.

## 4. Human and JSON rendering

- [x] 4.1 Render single integration status with overall status, summary, and check lines.
- [x] 4.2 Render `--all` status with one section per integration.
- [x] 4.3 Emit raw parsed JSON for single `--json` status.
- [x] 4.4 Emit aggregate JSON for `--all --json`.
- [x] 4.5 Include remediation lines when checks provide them.

## 5. Tests and docs

- [x] 5.1 Add descriptor parsing tests for `commands.status`.
- [x] 5.2 Add manifest round-trip tests for `status_command`.
- [x] 5.3 Add integration status command tests for success, fail, unsupported, unknown, and `--all`.
- [x] 5.4 Add mount regression tests proving status one-shots do not bind `/nix`.
- [x] 5.5 Document `piccolo integration status` in README.
- [x] 5.6 Run `go test ./...`.
- [x] 5.7 Validate `add-integration-status` OpenSpec artifacts.
