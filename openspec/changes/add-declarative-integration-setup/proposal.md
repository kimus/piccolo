## Why

Integration setup is currently an integration-owned TUI running inside a container PTY. That splits the product experience in two: every integration reimplements its own wizard (we added Rust `inquire` to `piccolo-gws` for exactly this), secrets get pasted into a terminal, an integration cannot see the host filesystem, and every integration that needs to register itself re-implements Piccolo's `config.json` merge-patch/backup/diff logic — with a divergent backup convention (`config.json.bak.<epoch>` vs the documented `config.json.bak`).

## What Changes

- **Descriptor becomes declarative, additively inside `piccolo.integration.v1`.** `describe` gains optional `actions[]` (stable id, role, title, inputs). No new command, no schema bump: presence of `actions` selects the new path, absence keeps today's `setup.command` TTY path working for existing integrations.
- **New `state` call** returns current facts (`credentials`, `login`, …) so Piccolo can enable/disable menu entries instead of letting users walk into doomed flows.
- **New `call <action-id>` streaming contract.** Answers go to **stdin as one JSON document** (never argv/env, which are visible in `docker top`/inspect); stdout is NDJSON events (`step`, `open_url`, `wait_redirect`, `result`, `error`); human logs stay on stderr.
- **Piccolo owns the UI.** Actions render with the existing `huh` stack (input, select, multiselect, confirm, file picker). Integrations ship no TUI.
- **Piccolo owns the `config.json` write.** The integration proposes a patch inside `result`; Piccolo validates it against a **fixed in-code allowlist** (MCP server entries only), then applies it through Piccolo's existing diff preview, confirmation, and `config.json.bak` convention.
- **Host↔state file brokering.** Piccolo copies a host-selected file (e.g. `client_secret.json`) into the integration's state directory with mode `0600`, then passes the container-side path to the integration.
- **Loopback callback courier.** For browser OAuth, Piccolo surfaces `open_url`, binds the port derived from the URL's own `redirect_uri`, and hands the raw callback back to the live container — no published ports, no gws change required.
- `status` checks may carry an optional `{label, url}` alongside the existing `remediation` text.

**BREAKING** for integrations that write `config.json` themselves: that behavior is replaced by proposing a patch (see `piccolo-gws` companion change).

## Capabilities

### New Capabilities

- `integration-setup-actions`: Piccolo-rendered integration setup — action discovery, state gating, streaming action invocation, answer transport, host file brokering, callback courier, and Piccolo-owned `config.json` application with a fixed write allowlist.

### Modified Capabilities

## Impact

- `internal/integration`: descriptor `actions[]`, `state` payload, NDJSON event types, optional remediation link; all parsed leniently so unknown/absent fields are ignored.
- `internal/manifest`: cache resolved actions + state keys per installed integration; drop integration-declared write permissions.
- `internal/setup`: reuse the existing merge-patch/backup/diff path for integration patches; enforce the fixed allowlist before writing.
- `cmd/piccolo`: `integration setup` renders from actions via `huh`; new `state`/`call` invocations over the existing attach path; secrets accepted on stdin only; `open_url`/`wait_redirect` handling.
- `internal/docker`: reuse the established one-shot naming/reclaim and signal-cleanup guarantees; exec-based callback delivery.
- Docs: setup UX is Piccolo-owned; integration authors declare actions instead of shipping a TUI.
