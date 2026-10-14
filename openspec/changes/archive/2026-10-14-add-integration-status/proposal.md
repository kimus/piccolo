## Why

Piccolo can install, set up, and upgrade integrations, but users still lack a productized way to verify whether an installed integration can actually connect to its backing services. Provider-specific health checks should remain integration-owned while Piccolo provides a generic command to run and present those checks.

## What Changes

- Add `piccolo integration status <name>` to run an installed integration's status command in a one-shot container.
- Add `piccolo integration status --all` to check every installed integration.
- Extend integration descriptors and manifest metadata with a status command.
- Define a generic JSON status contract (`piccolo.integration.status.v1`) with overall status and per-check results.
- Render human-readable status output and support `--json` for automation.
- Run status checks with structured mounts (`picoclaw/` and `integrations/`) without mounting runtime `/nix` over the image's Nix store.
- Preserve Piccolo's generic boundary: Piccolo does not know Gmail, Calendar, Drive, or any provider-specific semantics.

## Capabilities

### New Capabilities
- `integration-status`: Run integration-owned connectivity checks and present standardized status results.

### Modified Capabilities
- None.

## Impact

- CLI: new `piccolo integration status` subcommand and flags.
- Descriptor parsing: support status command metadata from `commands.status`.
- Manifest: record status command metadata for installed integrations.
- Docker: reuse one-shot integration container execution.
- Integrations: packages such as `piccolo-gws` can implement provider-specific live checks behind the generic status contract.
