## Why

Both gates between a registered integration and a callable tool failed silently: PicoClaw
skips MCP initialization while `tools.mcp.enabled` is false (pkg/agent/agent_mcp.go:79,
default false at pkg/config/defaults.go:428-431), and the patch allowlist deliberately
keeps an integration from flipping that toggle. A gateway therefore answered `/health`,
passed the serve gate (which executes integration *binaries*), and handed the agent nothing.
Enabling is not free either: with the toggle on and a server that cannot initialize,
PicoClaw fails the whole turn, so every chat message breaks.

## What Changes

- Registration and enablement separated; `tools.mcp.enabled` is written only after the
  recorded stdio command answers `initialize` in a one-shot container from the workspace's
  runtime image. Refused/absent framing, no engine, or nothing spawnable → register and
  withhold, reporting why.
- The manual toggle on setup's Tools screen runs the same verification, probing the running
  container's image as well as the manifest's (an upgrade lands only on restart).
- Doctor: MCP reachability, installed-but-never-registered, and the running container's
  image vs the manifest; a running container is no longer called "stale".

## Impact
- `cmd/piccolo/mcp_probe.go` (new), `integration_setup.go`, `setup.go`, `internal/doctor`,
  `lifecycle.go`, `internal/setup/integrationpatch.go`
- Verified live against the real image: gws 0.3.4 refused for LSP framing; a
  newline-delimited server verified; 0 probe containers left behind.
