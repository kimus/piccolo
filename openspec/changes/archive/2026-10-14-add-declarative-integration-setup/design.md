## Context

Setup today is an integration-owned terminal wizard executed inside a container PTY. That shape forced a lot of accidental complexity:

- `piccolo-gws` had to add a Rust prompt library (`inquire`) to reproduce a wizard Piccolo already builds in `huh`.
- An integration cannot see the host filesystem, so importing `client_secret.json` degenerated into "paste this and press Ctrl-D".
- The PTY path produced a real leak class: with a PTY, closing the attach stream does not deliver EOF, so piped stdin hung the one-shot forever and squatted its published port.
- Every integration that needs to register itself re-implements `config.json` merge-patch/backup. `piccolo-gws/src/config.rs` carries `merge_patch`, `backup_path`, `write_config_with_backup`, `summarize_patch` — Go equivalents already exist in `internal/setup`, and the two backup conventions disagree (`config.json.bak.<epoch>` vs the documented `config.json.bak`).
- Verified against `gws` 0.22.5: browser login prints its authorize URL on **stderr**, binds an **ephemeral** loopback port inside the **container**, and blocks until the redirect arrives. So a published host port can never carry the callback.

Piccolo already parses named command specs from the descriptor (`CommandSpecs["setup"]`, `["status"]`), which is the seam this design extends.

## Goals / Non-Goals

**Goals:**
- Piccolo renders all integration setup UI; integrations ship no TUI.
- Integrations declare a stable action menu plus dynamic state; Piccolo dispatches by opaque id.
- One config.json writer: Piccolo, behind a fixed in-code allowlist, with Piccolo's existing diff/confirm/backup path.
- Secrets and answers travel over stdin, never argv or env.
- Browser OAuth completes with zero published ports, including on a headless/SSH host.

**Non-Goals:**
- No breaking descriptor version: additive optional fields inside `piccolo.integration.v1`, presence-driven dispatch.
- No provider concepts in Piccolo core (`credentials`, `login`, `client_secret` stay integration-side).
- No arbitrary-port or arbitrary-path binding requested by integrations.
- No generic user-facing action runner (`piccolo integration run …`) — see Decisions.

## Decisions

### `describe` carries the stable menu, `state` carries the facts

Verified: `piccolo integration add` **already runs `describe` in a container**, so the menu arrives at install time for free — no extra cold start. `state` is a separate command because it is fetched on every run while `describe` is essentially install-time.

```json
{ "schema": "piccolo.integration.v1",
  "actions": [
    { "id": "set_up_access", "role": "setup", "title": "Set up Google Workspace access",
      "requires": ["credentials"],
      "inputs": [{ "key": "client_secret", "type": "path", "title": "client_secret.json",
                   "destination": "auth/client_secret.json", "sensitive": true }] },
    { "id": "login", "role": "setup", "title": "Login / authorize Google account" },
    { "id": "connectivity", "role": "diagnostic", "title": "Check connectivity" } ] }
```

`state --json` (JSON-only, **no flag**: the flag was mandatory noise on `status`):

```json
{ "schema": "piccolo.integration.state.v1", "name": "gws",
  "facts": { "credentials": false, "login": false, "apis_enabled": false } }
```

Piccolo enables an action only when its `requires` facts are true, and explains what's missing instead of running a doomed command.

**Alternative rejected:** one combined `describe --state`. It couples install-time data to run-time data and forces a re-describe on every invocation.

### Field vocabulary is closed, ids are opaque

`inputs[].type` ∈ `text`, `secret`, `path`, `select`, `multiselect`, `confirm` — a 1:1 mapping onto huh's `NewInput`/`NewSelect`/`NewMultiSelect`/`NewConfirm`/`NewFilePicker` (all already used dynamically in `internal/wizard/wizard.go`). `requires` is validated against keys the descriptor declares, so a typo cannot silently make an action permanently unavailable. Piccolo never interprets an action id.

### `call <action-id>` with three separate channels

```sh
piccolo-gws call login            # id passed as an argument, not a subcommand
```

| Channel | Purpose |
|---|---|
| stdin | one JSON answers document |
| stdout | NDJSON events (machine) |
| stderr | human logs, streamed through as progress |

A dynamic id as a clap *subcommand* would need static dispatch per integration; `call <id>` keeps one dispatch point and avoids collisions with `describe`/`state`. Answers go on **stdin** because argv is world-readable: `docker top` printed our own setup command line during this work, and `docker inspect` exposes env too.

### Streaming events, five types, one envelope

```json
{ "schema": "piccolo.integration.event.v1", "type": "step",           "id": "credentials", "status": "running", "message": "…" }
{ "schema": "piccolo.integration.event.v1", "type": "open_url",       "url": "…", "label": "Create a Desktop OAuth client in project my-proj" }
{ "schema": "piccolo.integration.event.v1", "type": "wait_redirect",  "url": "https://accounts.google.com/o/oauth2/auth?…redirect_uri=http://localhost:35935…" }
{ "schema": "piccolo.integration.event.v1", "type": "result",         "status": "ok", "configPatch": { "tools": { "mcp": { "servers": { "google_workspace": { … } } } } } }
{ "schema": "piccolo.integration.event.v1", "type": "error",          "code": "login_required", "message": "…", "remediation": "…", "url": "…" }
```

`open_url` is presentation only ("go here"); `label` is the one-line text, and step-by-step guidance rides in `step.message`. Choosing structured progress means `call` is a long-lived streaming process — which dissolves the earlier one-shot-vs-session debate: one streaming `call` per action, while `describe`/`state` stay simple one-shot JSON.

### The callback courier, not a published port

The browser lives on the host; `gws` listens on the **container's** loopback, on a port that is re-randomized per run (observed `33773`, then `35935`). Pre-binding is impossible. So:

```text
integration → wait_redirect{url}            Piccolo reads the port from redirect_uri
Piccolo     → binds 127.0.0.1:<that port>   prints URL, attempts host browser open
browser     → Google → localhost:<port>     Piccolo captures the raw request line
Piccolo     → docker exec <one-shot> curl "http://localhost:<port><captured-path>"
                                             runs inside the container's netns: loopback works
```

Rules: loopback only; the port is **derived from `redirect_uri`, never supplied separately** (otherwise it becomes an arbitrary "bind any host port and pipe me bodies" primitive); exactly one request then close; the callback is treated as opaque and never logged. Because there is no `state` param in the observed URL, CSRF protection rests on the PKCE verifier living in that one `gws` process — which is why the code must go back to the *same* live container.

**Alternative rejected:** ngrok / Tailscale Funnel / Serve. All require Google to redirect to a public URI, but a Desktop OAuth client's registered redirect is `http://localhost:<port>` and `gws` hardcodes it. `gws` exposing device flow (it links `yup_oauth2::device`, `verification_uri`, `ask_auth_code_interactively`, `auth_code_from_url` but exposes no flag) would delete the courier entirely — that's the upstream ask, tracked separately.

### Piccolo is the only config.json writer

The integration proposes, Piccolo applies:

```text
result.configPatch  → validated against a FIXED in-code allowlist (tools.mcp.servers.<name>)
                    → diff preview → confirmation → config.json.bak → atomic write
```

The allowlist is not data the integration can widen: an unscoped "merge this JSON" would let any integration rewrite `agents.list`, model keys, or tool toggles — privilege escalation on a file AGENTS.md declares user-owned and create-only. Centralizing also ends the two-writer backup divergence and reuses Piccolo's existing preview/confirm/backup path, so integration authors write zero config-patching code.

### Integration keeps its own files; Piccolo brokers transfers

Auth tokens, credentials, and provider state stay under `integrations/<owner>/<repo>/`, which Piccolo never reads or parses. For host→state transfers, `inputs[].type = "path"` carries a `destination` relative to the state dir; Piccolo copies with mode `0600`, rewrites the value to the container-side path, and hands the integration a path it can actually open. Secrets therefore travel stdin → file, never argv.

### Roles are dispatch, not a plugin surface

`role` ∈ `setup`, `diagnostic`; unknown roles are **ignored, never errors**, so future roles cannot break an old Piccolo. Piccolo dispatches internally by role only.

A generic `piccolo integration run <pkg> <action-id>` was **rejected for now**: one integration exists, so every generalization has one data point; `gws`' own MCP tools are read-only, so a CLI action runner buys little; and actions run with the integration's privileges inside the mount holding its auth tokens, making destructive actions one menu click away. Stable ids + opaque roles keep that door additive for later.

### Preserve `v1`, no negotiation field

`SchemaV1` is compared with `!=`, so the string is a gate: bumping it hard-fails every existing Piccolo build, and unknown JSON keys are silently dropped — meaning additive optional fields are safe today. Dispatch is therefore presence-driven: `actions[]` present → declarative path; absent → the legacy TTY `setup.command` path, unchanged. The earlier `config: "self" | "patch/v1"` idea is dropped for the same reason. Consequence to hold: **never add a field whose absence breaks a path an older Piccolo can reach.**

## Risks / Trade-offs

- [No way to require a Piccolo new enough for the menu] → presence-driven dispatch; unknown roles and fields ignored; a soft `requires`/min-version hint is the escape hatch if ever needed.
- [Long-running `call` blocks on a human] → needs a distinct abort/timeout story (the 20 s tool timeout we fought is the wrong bound); keep the container alive under the existing one-shot naming + reclaim + signal rules.
- [Courier binds a host port that may be taken] → re-run the action to get a fresh ephemeral port rather than failing.
- [Answers on stdin still pass through Piccolo memory] → acceptable; Piccolo must not log them and must not persist them anywhere.
- [Two setup mechanisms live at once (legacy TTY + declarative)] → intentional for compatibility; retire the legacy path once `piccolo-gws` ships actions.
- [SSH session: browser is on the laptop, Piccolo listens on the remote host] → print `ssh -L <port>:localhost:<port> <host>` from the known port; device flow is the real fix.
- [Menu cached in `piccolo.yaml` going stale] → do not persist `actions[]`; re-describe on demand. Existing cached `DisplayName`/`SetupCommand` staleness is out of scope here.

## Migration Plan

1. Extend Piccolo parsing (actions/state/events, lenient), then the huh renderer and `state`/`call` invocation over the existing attach path.
2. Add the fixed config-allowlist write path in `internal/setup`; keep the legacy TTY setup working.
3. `piccolo-gws` companion: declare actions, emit events, stop writing `config.json`, drop `inquire`; release a new integration version.
4. Verify `piccolo integration add` → `setup` end to end; verify the legacy path still runs for integrations without actions.
5. Rollback: because dispatch is presence-driven, removing `actions[]` from a descriptor reverts an integration to the TTY path with no Piccolo change.

## Open Questions

- Should `open_url` attempt a host browser launch automatically, or always require confirmation first? Leaning: launch when stdout is a TTY and not an SSH session, otherwise print.
- Do we want `result` to carry a machine-readable list of files the action wrote (for doctor/reporting), or keep Piccolo ignorant? Ignorance keeps neutrality; a bare `artifacts[]` list of paths is tempting but is exactly where provider leakage starts.
- Should the courier be opt-out per action (`"callback": "none"`)? Probably unnecessary: Piccolo only binds when a `wait_redirect` event arrives.
