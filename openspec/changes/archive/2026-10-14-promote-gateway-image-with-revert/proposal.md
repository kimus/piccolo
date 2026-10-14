## Why

`piccolo restart` stops and removes the running gateway **before** it knows the replacement works, then health-gates the new one. If the new image fails its gate, there is no gateway left running and nothing recorded to go back to — the user is down, and the recovery path is manual editing of `piccolo.yaml`. Measured on this workspace: the health gate is the only check, and a gateway whose binaries cannot execute passes it, because `/health` says nothing about the Nix store.

The steps this change needs are already verified against the daemon:

```text
stop old                      → published port released (listeners 0)
start <other-name> on same    → OK (1 listener)
rename the RUNNING winner     → OK: published ports intact, exec by new name works, no restart
stopped old, never renamed    → still restartable under its original name
```

## What Changes

- Promote through a candidate container named `<container.name>.next`: stop the current gateway, start `.next` from the target image, gate it, then either finish or revert.
- **Gate = execute, not find.** `/health` plus a `docker exec` of each installed integration's binary. Presence checks are known to lie: `command -v piccolo-gws` succeeds where `piccolo-gws state` reports `not found`.
- **On success:** remove the stopped old container, then the candidate already holds the canonical name. There is no second outage and no rename of a running container needed.
- **On failure:** stop `.next`… specifically: remove the failed candidate, rename the parked container back to the canonical name, start it again, and report non-zero — after its gate diagnostics and last log lines have been printed.
- Record the previously running image so `piccolo integration rollback` can put a workspace back on it through the same promote path.
- Identify the parked revert target by **name** (`<container.name>.prev`), not by label: this Docker SDK can only set labels at create time and `ContainerUpdateOptions` cannot change them, so a "candidate" label could never be cleared once the candidate won. (The original sketch said "label the one-shot candidate"; the name carries the role instead, and the canonical name always belongs to what should be running.)

## Capabilities

### New Capabilities

- `gateway-image-promotion`: staged gateway image promotion with an executed readiness gate, automatic revert, retained diagnostics, and rollback to the previously running image

### Modified Capabilities

## Impact

- `cmd/piccolo/serve.go`: health gate extended to an executed readiness check; promote/revert sequencing
- `cmd/piccolo/restart.go`: stops instead of stop-and-discard, keeps the revert target
- `internal/docker`: start-without-remove and start-by-name primitives for the revert path; label the candidate
- `internal/manifest`: record the previous image, single slot
- `cmd/piccolo/integration.go`: `piccolo integration rollback` driving the same promote path
- `internal/doctor`: report an interrupted promote (stray candidate) and a stale previous image
- Depends on the labels and identity from `bake-runtime-toolchain-into-image`; drift remains a warning, never an implicit build
