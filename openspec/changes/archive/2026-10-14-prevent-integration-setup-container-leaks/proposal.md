## Why

An interrupted `piccolo integration setup` left its one-shot container running forever. Because setup published a fixed host port from the descriptor, the next setup failed with `Bind for 127.0.0.1:8080 failed: port is already allocated` — a leaked container became a permanent block, and Piccolo had no way to detect or reclaim it.

## What Changes

- Give setup/status one-shot containers **deterministic names** (`piccolo-setup-<integration>`, `piccolo-setup-status-<integration>`) instead of per-run nanosecond suffixes, so leftovers are findable.
- **Reclaim abandoned one-shots** before starting a new run, reporting what was removed.
- **Handle SIGINT/SIGTERM** during setup and status so the container is stopped and removed instead of orphaned.
- **Only request a container PTY when the host has a terminal.** With a PTY the wizard reads `/dev/tty`, so piped stdin never delivers EOF and the container blocks forever — the actual source of the leaks.
- `piccolo doctor` **warns** about abandoned Piccolo one-shot containers.
- Report a vanished container as a legible message rather than a raw daemon 404.

## Capabilities

### New Capabilities

- `integration-one-shot-lifecycle`: Piccolo names, reclaims, signal-cleans, and reports on short-lived integration setup/status containers so an interrupted run cannot permanently block the next one.

### Modified Capabilities

## Impact

- `internal/docker`: `SetupContainerPrefix`, `ListByPrefix`, `RemoveByPrefix`, not-found handling in `inspectState`.
- `cmd/piccolo/integration.go`: deterministic naming, reclaim sweep, TTY gating on host terminal, signal-aware waits.
- `cmd/piccolo/terminal.go`: raw-mode + PTY size sync helpers.
- `internal/doctor`: new `Engine.ListByPrefix` method + orphan warning.
- Tests: reclaim/deterministic-name/orphan-warning coverage.
