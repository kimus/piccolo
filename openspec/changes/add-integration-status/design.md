## Context

Piccolo integrations are self-describing packages installed into the gateway runtime image. Piccolo currently records setup and descriptor metadata and can run setup in a one-shot container. It deliberately avoids provider-specific behavior: Google Workspace auth, Gmail connectivity, Calendar scopes, and similar semantics belong to `piccolo-gws` or the relevant integration package.

Users need a single Piccolo command that answers whether an installed integration is usable. The core should orchestrate execution and presentation, while integrations provide the actual checks and remediations.

## Goals / Non-Goals

**Goals:**

- Provide `piccolo integration status <name>` and `piccolo integration status --all`.
- Add descriptor support for an integration-owned status command.
- Define a stable generic JSON status schema with overall and per-check status.
- Render useful human output and support JSON output for automation.
- Keep status execution independent of a running gateway container.
- Avoid mounting workspace `runtime/nix` over `/nix` for integration one-shots.

**Non-Goals:**

- Do not hard-code Google Workspace, Gmail, Calendar, Drive, or any provider-specific checks in Piccolo core.
- Do not require the gateway container to be running.
- Do not mutate integration state during status checks except for any benign provider cache/refresh behavior owned by the integration command.
- Do not replace `piccolo doctor`; doctor validates Piccolo workspace/container readiness, while status validates integration-specific connectivity.

## Decisions

### Descriptor-owned status command

Integrations will advertise a status command through descriptor metadata. For structured descriptors this is `commands.status`, for example:

```json
{
  "commands": {
    "status": {
      "command": "piccolo-gws",
      "args": ["status", "--config-dir", "/root/.piccolo/integrations/gws", "--json"]
    }
  }
}
```

Piccolo records the resolved argv in `piccolo.yaml` as `status_command`. Older integrations without a status command remain installable, but `piccolo integration status <name>` reports that status is unsupported and suggests upgrading the integration.

### Generic status schema

Status commands emit JSON using `schema: "piccolo.integration.status.v1"`. The shape is intentionally provider-neutral:

```json
{
  "schema": "piccolo.integration.status.v1",
  "name": "gws",
  "status": "fail",
  "summary": "Calendar cannot connect",
  "checks": [
    {"id":"auth","label":"Google auth","status":"ok","message":"authenticated"},
    {"id":"calendar","label":"Calendar","status":"fail","message":"insufficient scopes","remediation":"Run piccolo integration setup gws"}
  ]
}
```

Allowed statuses are `ok`, `warn`, `fail`, `unknown`, and `skip`. The overall status determines the Piccolo command exit code unless `--json` aggregation is used for scripting.

### One-shot status containers

Piccolo runs status commands in a temporary container from the configured runtime image. The mounts match integration setup/descriptor execution: `picoclaw/` to `/root/.picoclaw` and `integrations/` to `/root/.piccolo/integrations`. Piccolo does not mount `runtime/nix` to `/nix` because integration binaries are installed at image build time and may rely on the image's Nix store.

### `--all` aggregation

`piccolo integration status --all` runs each installed integration's status command sequentially and renders sections per integration. JSON output returns an aggregate with an array of integration status results. If an integration lacks a status command, the result is `unknown` or `warn` with remediation to upgrade/reinstall.

## Risks / Trade-offs

- **Provider checks may be slow** → Status command is integration-owned; Piccolo should surface progress and timeouts if needed.
- **Status may refresh tokens or touch external APIs** → This is the intended behavior for connectivity status, but integrations must keep checks minimal and read-only.
- **Old integrations lack status commands** → Report unsupported status clearly instead of failing with a low-level error.
- **JSON contract drift** → Validate schema and statuses in Piccolo tests and integration tests.
- **Exit code ambiguity** → Use non-zero for overall `fail`; keep `warn` as zero unless a future `--strict` flag is added.

## Open Questions

- Should Piccolo expose a timeout flag for status commands, or rely on integration defaults first?
- Should `piccolo doctor` include a lightweight hint such as "run integration status" without executing provider checks?
