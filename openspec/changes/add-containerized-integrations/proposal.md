## Why

Piccolo is beginning to need productized integrations such as Google Workspace without baking provider-specific behavior into the core launcher. A generic containerized integration mechanism lets Piccolo install trusted integration packages into the gateway image and delegate provider-specific setup to those integrations while preserving Piccolo's thin setup/launcher boundary.

## What Changes

- Add a `piccolo integration` command group with:
  - `piccolo integration add <alias-or-ref>` to resolve an integration, build a new gateway image containing it, and record the installed integration metadata.
  - `piccolo integration setup <name>` to run the integration-owned setup command in a one-shot container created from the configured gateway image.
- Add an integration descriptor contract discovered by running the installed integration binary's `describe --json` command inside the built image.
- Support built-in aliases, initially `gws` / `google-workspace` resolving to `github:kimus/piccolo-gws`.
- Install integrations at Docker image build time so their commands exist before `picoclaw gateway` starts.
- Record Piccolo-owned integration build metadata in `piccolo.yaml` under `runtime.integrations`.
- Leave integration-specific configuration changes, including `config.json` MCP server patching, to the integration setup command.

## Capabilities

### New Capabilities
- `containerized-integrations`: Install, describe, record, and run setup for self-describing integration packages baked into the gateway image.

### Modified Capabilities

## Impact

- CLI: new `piccolo integration add` and `piccolo integration setup` commands.
- Runtime image builder: support build-time Nix/flake integration installation and descriptor inspection.
- Manifest: extend `piccolo.yaml` with Piccolo-owned installed integration metadata.
- Docker runner: support one-shot setup containers using the configured image, workspace mount, resolved environment, and integration-declared command/ports.
- Documentation: integration author contract and user workflow.
