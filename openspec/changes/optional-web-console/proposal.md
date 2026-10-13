## Why

Today `piccolo init` frames the interaction style as a mutually exclusive choice — "Terminal **or** Web console?" — which bakes a whole interaction paradigm into the workspace. Users actually think of it differently: the **gateway is required**, and the **web console is an optional extra** they may or may not want running when they `piccolo serve`. The current either/or wording obscures that the gateway (and `piccolo chat`) always exist, and that the web console is simply an add-on layered on top.

## What Changes

- Reframe the setup choice from an either/or *interaction mode* ("terminal" vs "web") into an **optional web console add-on**: the PicoClaw gateway always runs; the web console is started only when the user opted in.
- Make the **`:18800` port binding the single source of truth** for "web console enabled". No separate boolean that can drift from the published ports.
- Guarantee **no dead port**: when the web console is disabled, `:18800` is not published and the dashboard process does not run; the container serves only the gateway on `:18790`.
- Rename the existing `--web` flag (web *search tools*) to **`--web-search`**. Once a `--dashboard` flag exists, a bare `--web` is ambiguous; the explicit name makes the two axes self-documenting (`--web-search` = agent tool in `config.json`; `--dashboard` = browser UI on `:18800`).
- Replace the `--mode terminal|web` selection with a dedicated **web-console toggle** (a `--dashboard` flag plus a wizard confirmation), framed as "gateway required, web console optional". No backward compatibility is required for the old `--mode`/`--web` flags or the internal mode enum.
- Confirm `serve`, `status`, and `doctor` messaging treats the web console as optional and never leaves a published-but-unbound `:18800`.
- Update README and AGENTS.md to describe the web console as an optional service rather than an interaction mode.

Non-goals: no separate `piccolo web` command, no serve-time image swapping, no two-container/sidecar topology, no re-init automation to flip the choice later (users edit `piccolo.yaml`).

## Capabilities

### New Capabilities
- `web-console`: The optional PicoClaw web console — how a workspace records the choice, how `serve` starts (or omits) it, how the `:18800` binding acts as the single source of truth, and how the gateway remains required regardless.

### Modified Capabilities
<!-- No OpenSpec specs exist yet (openspec/specs is empty); existing behavior is documented in AGENTS.md/README, not as OpenSpec capabilities. Nothing to modify here. -->

## Impact

- **CLI/UX**: `internal/wizard` (replace the terminal/web Select with a web-console Confirm), `cmd/piccolo/init.go` (replace `--mode` with `--dashboard`; rename `--web` → `--web-search`).
- **Generation**: `internal/generate` (replace the `Mode` enum input with a `Dashboard bool`; console-on → launcher image + `:18800` binding, console-off → gateway image).
- **Runtime**: `cmd/piccolo/serve.go` (`webConsoleURL` detection, hint output), `cmd/piccolo/lifecycle.go` (status), `internal/doctor` (no dead-port warning).
- **Manifest**: `internal/manifest` (the `:18800` binding remains implicit source of truth; no schema change required).
- **Docs**: `README.md`, `AGENTS.md`.
- **Manifest compatibility**: existing `piccolo.yaml` files keep working — serve/doctor still read console state from the `:18800` binding, so no manifest migration is needed. The breaking change is confined to the `init` CLI flags and the internal generation input.
