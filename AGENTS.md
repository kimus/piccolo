# AGENTS.md — Implementation Guide

Instructions for coding agents implementing Piccolo. Read [README.md](README.md) first for the user-facing model. This file contains the binding constraints, verified upstream facts, and acceptance criteria.

## Hard Constraints

- The binary produced by this project SHALL be named `piccolo`.
- A reachable Docker daemon SHALL be required. Piccolo talks to it through the Docker Go SDK (`github.com/docker/go-sdk/client`, pinned exactly: `v0.1.0-alpha013`, plus `github.com/moby/moby/api` and `github.com/moby/moby/client`). The `docker` CLI SHALL NOT be a hard dependency.
- There SHALL be no `picoclaw` binary requirement on `$PATH`, no binary-path flag, and no binary downloader.
- `piccolo.yaml` SHALL contain only Piccolo launch configuration. There SHALL be NO duplicated configuration between `piccolo.yaml` and `config.json` (no assistant name, model, tools, or agent lists in `piccolo.yaml`).
- `config.json` and agent workspace files are generated **once** by `piccolo init` and are **user-owned** afterwards. Piccolo SHALL never silently regenerate or overwrite them.
- The implementation SHALL run a single gateway container hosting multiple PicoClaw agents — not one container per agent.
- Do NOT implement custom Piccolo memory, native PicoClaw tools, MCP servers, or custom runtime logic.
- Do not assume PicoClaw features — verify against the pinned version's source before relying on any flag, field, or behavior.

## Verified PicoClaw Facts (v0.3.1)

Verified against `github.com/sipeed/picoclaw@v0.3.1` source (present in the Go module cache at `~/go/pkg/mod/github.com/sipeed/picoclaw@v0.3.1/`). Treat as ground truth:

- `PICOCLAW_HOME` overrides the base dir for all PicoClaw data (default `~/.picoclaw`). Config file: `<home>/config.json` — **JSON, schema `version: 3`**. No YAML main-config support. Piccolo does NOT set `PICOCLAW_HOME`: we mount onto the in-container default (`/root/.picoclaw`, since `HOME=/root`).
- Multi-agent in one gateway: `agents: { defaults, list: [ { id, default, name, workspace, model, skills, subagents: { allow_agents: [] } } ], dispatch }` (`pkg/config/config.go`: `AgentsConfig`, `AgentConfig`).
- Tool toggles are real config fields: `tools.web.enabled`, `tools.exec.enabled`, `tools.spawn.enabled`, `tools.subagent.enabled`, plus per-tool entries (`web_fetch`, `message`, `read_file`, `write_file`, `edit_file`, `list_dir`, `cron`, `mcp`, `skills`, ...).
- Models: top-level `model_list`: `{ model_name, provider, model, api_base, api_keys, ... }`. Agents reference models by `model_name`. `api_keys` (`SecureStrings`) has **no verified env-expansion** — assume plaintext on disk.
- Agent workspaces: `AGENT.md` (YAML frontmatter: `name, description, tools, model, maxTurns, skills, mcpServers` — `pkg/agent/definition.go`), `SOUL.md`, `USER.md`, `memory/MEMORY.md`. Legacy `AGENTS.md` is a fallback; use `AGENT.md`.
- Official images: `docker.io/sipeed/picoclaw:<version>` (gateway/agent) and `docker.io/sipeed/picoclaw:launcher` (web console + gateway, ports `18800`/`18790`, needs `PICOCLAW_GATEWAY_HOST=0.0.0.0`).
- Image `/entrypoint.sh`: skips onboarding when `config.json` exists in home, removes stale `.picoclaw.pid`, then `exec picoclaw gateway "$@"`. Rely on this default entrypoint.
- PicoClaw home also holds `auth.json`, default `workspace/`, global `skills/`, channel state — runtime writes back, so mount the WHOLE workspace dir, not just `config.json`.
- Health API: `/health` and `/ready` are **unauthenticated**; only `POST /reload` requires a bearer token (`pkg/health/server.go`). Served on `gateway.port` (default **18790**, config key `gateway.port`, env `PICOCLAW_GATEWAY_PORT`).
- The gateway binds `localhost` by default. A process on the container's loopback is **unreachable through published ports** — when publishing, piccolo injects `PICOCLAW_GATEWAY_HOST=0.0.0.0` into the container env (unless the manifest already sets it, or network mode is `host`). Verified by smoke test.
- Clients: the ONLY terminal client is `picoclaw agent` (readline REPL; `-m` for one-shot). There is no `picoclaw-launcher-tui` — the launcher is web-only (`picoclaw-launcher`, React dashboard :18800, proxies chat to the gateway via `/pico/ws`, spawns `picoclaw gateway -E` as its own child in the launcher image). First run: `:18800` redirects to `/launcher-setup` (create dashboard password), afterwards `/launcher-login`. Verified live: launcher image works with a pre-mounted `config.json`, the gateway health port (:18790) is served by the launcher's gateway child.

## Workspace & Mounting

- Workspace = the target directory itself (default: cwd; `-D/--dir` flag overrides). NO `.piccolo/` subfolder — `piccolo.yaml`, `config.json`, `.gitignore`, `agents/` live directly inside it.
- Workspace resolution rule is shared by ALL commands: `-D/--dir` flag, else cwd. `serve`/`doctor` fail with `no piccolo workspace found — run 'piccolo init' first` when `piccolo.yaml` is absent.
- ONE bind mount: `<abs workspace path>` → `/root/.picoclaw`.
- All `workspace` paths in `config.json` SHALL be container-side paths, e.g. `~/.picoclaw/agents/main` (image runs with `HOME=/root`; upstream defaults use `~`, tilde expansion supported).

### `piccolo.yaml` schema

```yaml
version: 1
container:
  image: docker.io/sipeed/picoclaw:v0.3.1  # pinned tag, never "latest" in generated manifests
  name: piccolo-gateway
  network: bridge                           # none | bridge | host
  ports: []                                 # launcher: ["127.0.0.1:18800:18800"]
  env: {}                                   # literal or "env:HOST_VAR" (resolved at serve time)
```

- Strict decoding: unknown keys SHALL cause a parse error.
- `env:HOST_VAR` refs resolve from host env at `serve`; missing refs → doctor warning + serve-time error.

### `config.json` generation

Generated from wizard answers, PicoClaw schema v3. Minimum shape:

```json
{
  "version": 3,
  "agents": {
    "defaults": {
      "workspace": "~/.picoclaw/agents/main",
      "restrict_to_workspace": true,
      "model_name": "<wizard choice>"
    },
    "list": [
      { "id": "main", "default": true, "workspace": "~/.picoclaw/agents/main",
        "subagents": { "allow_agents": ["research", "planner"] } },
      { "id": "research", "workspace": "~/.picoclaw/agents/research" },
      { "id": "planner", "workspace": "~/.picoclaw/agents/planner" }
    ]
  },
  "model_list": [
    { "model_name": "<alias>", "model": "<provider>/<model>", "api_keys": ["<key>"], "api_base": "<optional>" }
  ],
  "tools": {
    "web":  { "enabled": true },
    "exec": { "enabled": false },
    "spawn": { "enabled": true },
    "subagent": { "enabled": true }
  }
}
```

- Re-verify the exact field set against `config/config.example.json` of the pinned version during implementation.
- Deterministic output: stable key order, 2-space indent. No Piccolo branding inside the file.

## Command Behavior

### `piccolo init`

1. Check Docker daemon reachability; warn (not fail) if unreachable.
2. Ask wizard questions: assistant name; owner name; purpose template (personal/research/coding/operations/blank); specialist agents (research, planner, coder, ops); interaction mode (**terminal** = gateway image + stdio, **web console** = launcher image + `127.0.0.1:18800` + `PICOCLAW_GATEWAY_HOST=0.0.0.0` env); model provider + model + API key (placeholder allowed with warning); web search (default yes); shell exec (default no); workspace path (default cwd).
3. Preview the generation plan (files, image, ports), then final confirmation.
4. Write files. **Create-only**: if files exist, list them and require explicit per-file confirmation before any overwrite.
5. Ensure `config.json` is git-ignored: create `.gitignore` or append if the entry is missing.

- Preferred prompt library: `github.com/charmbracelet/huh`. Keep wizard logic separated from file generation (upgrade path to full Bubble Tea).
- Support non-interactive flags (`--name`, `--owner`, `--purpose`, `--yes`, ...) for scripting.

### `piccolo doctor`

Checks: Docker daemon reachable (SDK ping); image present locally (warn + pull hint if missing, not a failure); `piccolo.yaml` exists/parses strictly/pinned tag; `config.json` exists/parses/`version == 3`; `agents.list` non-empty with exactly one `default: true`; every referenced agent workspace exists **on the host** (translate `~/.picoclaw/...` → `<workspace>/...`) with parseable `AGENT.md` frontmatter; `model_list` non-empty (warn on placeholder/empty keys); every `env:VAR` ref resolves (warn); stale container with configured name (warn — `serve` will replace it).

Output: `[OK]`/`[WARN]`/`[FAIL]` status lines (see README for the exact format). Exit `0` unless any `[FAIL]`; warnings never fail.

### `piccolo serve`

Starts the gateway container **detached** and health-gates the command:

1. Critical doctor subset first (daemon reachable, both config files present + parseable). Fail fast, actionable messages.
2. Derive the gateway port from `config.json` `gateway.port` (default 18790).
3. Compose the launch spec: image/name/network/ports/env from `piccolo.yaml`; auto-publish `127.0.0.1:<gwPort>:<gwPort>` when the manifest doesn't already map that container port (skip for `network: host`); inject `PICOCLAW_GATEWAY_HOST=0.0.0.0` unless the manifest env already sets it; labels `piccolo.managed=true`, `piccolo.workspace=<abs path>`.
4. Remove stale container by name, create (pull once on `No such image`), start detached (`Init: true`, default entrypoint, no stdio attach).
5. Poll `http://127.0.0.1:<gwPort>/health` (1s interval, 90s budget). Fail early if the container exits. On timeout/failure: print error + last ~30 log lines, leave the container for debugging, exit non-zero.
6. On success: print the health URL, web console URL when a `:18800` binding exists, and next-step hints (`chat`, `logs -f`, `status`, `stop`). Exit 0.

`--foreground/-f` keeps the attached behavior: pump stdio, forward SIGINT/SIGTERM to a graceful stop, remove the container, propagate its exit code.

The Docker SDK SHALL be isolated behind an internal `Runner`/`Process` interface so SDK types never leak and unit tests run daemon-free.

### `piccolo stop` / `piccolo status` / `piccolo logs`

- `stop`: graceful `ContainerStop` (grace 10s) + remove by name; message when not running.
- `status`: container state (found/running/status/exit code) + live `/health` probe with body.
- `logs [-f] [--tail N]`: stream via `ContainerLogs`, demux the docker framing, stop following on Ctrl-C.

### `piccolo chat`

Terminal chat by wrapping PicoClaw's own client through `ExecCreate`/`ExecAttach` (no custom TUI), rebranded at the stream layer (`brandFilter`):

- `chat` (interactive): TTY exec of `picoclaw agent`; local terminal goes raw (`golang.org/x/term`), bytes flow both ways, restore on exit, propagate the remote exit code. Requires a TTY and a running gateway.
- `chat -m <msg>` (one-shot): non-TTY exec of `picoclaw agent -m <msg>`; demux output; wait for the exec exit code via `ExecInspect`.

Rebranding rules (verified against v0.3.1 client output):

- The assistant name comes from the default agent's `AGENT.md` frontmatter (fallback: "Piccolo").
- Interactive: swallow everything up to the `Interactive mode (Ctrl+C to exit)` sentinel; print our own header; the 🦞 emoji is replaced by the assistant name everywhere (handles emoji split across writes).
- One-shot: swallow the leading banner up to its terminating space-only line (both color and plain banners end with `" \x1b[0m\r\n"` / `" \r\n"`).
- PicoClaw client log lines (`HH:MM:SS ERR|WRN|INF|DBG|FTL|TRC ...`) are suppressed in chat — full detail stays available via `piccolo logs`.
- Safety: if a sentinel is not found within 8 KiB, the filter degrades to plain pass-through + emoji replacement, so an upstream banner change can never break or eat the stream.

For the web console, the wizard's **web** mode selects the launcher image; the browser chat lives at `http://127.0.0.1:18800`.

## Agent File Generation

Generated once by `init`; user-owned afterwards.

### Main agent (`agents/main`)

`AGENT.md` frontmatter:

```md
---
name: <AssistantName>
description: Coordinator assistant for <Owner>.
tools: [web_search, web_fetch, message, spawn, subagent]
---
```

Body: knows assistant + owner names, explains it runs on PicoClaw, delegates to configured subagents, MUST NOT claim unavailable features (custom memory DB, reminders, native Piccolo runtime).

Tool names in frontmatter MUST match real PicoClaw runtime tool names — verify against the pinned version's tool registry (config-side names verified: `web`, `web_fetch`, `exec`, `spawn`, `subagent`, `message`, `read_file`, `write_file`, `edit_file`, `list_dir`; frontmatter allowlist names to double-check).

- `SOUL.md`: personality/tone from purpose template (default: concise, helpful, proactive).
- `USER.md`: owner name + setup-provided context.
- `memory/MEMORY.md`: seeded empty with a short usage note.

### Specialists (`research`, `planner`, `coder`, `ops`)

`AGENT.md` + `SOUL.md` each:

- **research** — investigation, web search, reading, synthesis.
- **planner** — goal decomposition, plans, work organization.
- **coder** — coding/project help; conservative; shell guidance only when exec enabled.
- **ops** — operational checklists, diagnostics, troubleshooting.

Reduced tool allowlists (research: `web_search`, `web_fetch`, `read_file`; coder adds file-write tools; exec only if user enabled shell execution).

## Non-Goals (PoC)

- No custom memory MCP server, SQLite store, tasks/reminders/scheduler.
- No native PicoClaw tool registration or hooks.
- No PicoClaw binary bundling, downloading, or `$PATH` usage.
- No detached/foreground ambiguity: serve is detached + health-gated; `--foreground` attaches.
- No custom terminal chat UI — `piccolo chat` wraps `picoclaw agent` via exec.
- No `piccolo add-agent` (manual edit of `config.json` + workspace; doctor catches mistakes).
- No channel credential setup wizard.
- No regeneration/migration of an existing `config.json`.
- No migration from piccolo-full beyond code reuse.

## Implementation Notes

- Go. CLI: Cobra. Prompts: `charmbracelet/huh`. YAML: `gopkg.in/yaml.v3`.
- Docker deps pinned exactly (go-sdk is pre-1.0, never float): `github.com/docker/go-sdk/client v0.1.0-alpha013`, `github.com/moby/moby/api v1.52.0`, `github.com/moby/moby/client v0.1.0`, `github.com/containerd/errdefs v1.0.0`.
- Reuse from `../piccolo-full`: `internal/isolation/dockerapi` pattern (prepare/start split, attach-before-start, stale removal by name, pull-once, ownership labels, demuxed stdio) and the `Runner`/`Process` interfaces.
- Deterministic generation everywhere: sorted keys, stable templates — workspace diffs must be readable.
- Package layout: `cmd/piccolo`, `internal/manifest`, `internal/generate`, `internal/doctor`, `internal/docker`, `internal/wizard`.

## Acceptance Criteria

1. `go build ./...` and `go test ./...` succeed.
2. `piccolo init` creates a complete workspace from interactive answers (and via non-interactive flags).
3. `piccolo init` on an existing workspace creates only missing files and asks before any overwrite.
4. `piccolo doctor` reports all `[OK]` for a fresh workspace with a reachable daemon; `[FAIL] docker daemon not reachable` when Docker is down; non-zero exit only on failures.
5. `piccolo serve` starts the container detached, auto-publishes the health port, waits for `/health`, prints status + hints, and exits 0; on unhealthy start it prints logs and exits non-zero.
6. `piccolo stop` removes the container; `piccolo status` reports state + health; `piccolo logs` streams (with `-f`).
7. `piccolo chat -m <msg>` runs a one-shot agent turn through exec and propagates its output/exit code; `piccolo chat` provides an interactive TTY session.
8. `piccolo serve --foreground` attaches stdio, forwards SIGINT/SIGTERM to a graceful stop, removes the container, exits with the container's exit code.
9. Recovery from a crashed previous run: stale container replaced by name on next serve.
10. Generated `config.json`: ONE gateway, multiple agents in `agents.list`, container-side workspace paths, ≥1 `model_list` entry.
11. `piccolo.yaml` contains no PicoClaw-domain configuration.
12. Generated `AGENT.md` files have valid frontmatter and useful role instructions.
13. Unit tests: manifest strict parsing, config generation determinism, doctor checks (daemon-free via interface), init non-destructive behavior, container spec composition (mounts/env/ports/labels), health-port/gateway-host injection helpers.

## Decisions Log

- **Runtime delivery**: Docker container via Go SDK — no `picoclaw` on `$PATH`.
- **Config split**: `piccolo.yaml` = launch-only; `config.json` = PicoClaw behavior, generated once, user-owned forever. No duplication.
- **Workspace = picoclaw home via default mount**: init writes everything directly into the workspace dir (default cwd, user-chosen); serve mounts it onto `/root/.picoclaw`. `PICOCLAW_HOME` never set. No `.piccolo/` subfolder, no `home:` key.
- **Config format**: PicoClaw reads JSON (schema v3); YAML only for `piccolo.yaml` and `AGENT.md` frontmatter.
- **API keys**: plaintext in `config.json`; mitigated by generated `.gitignore` + doctor placeholder warnings.
- **Serve lifecycle**: detached + health-gated (`/health` on published 127.0.0.1 port) is the default; `--foreground` for attached stdio. Gateway gets `PICOCLAW_GATEWAY_HOST=0.0.0.0` injected when ports are published (container-loopback unreachable otherwise — verified by smoke test).
- **Chat clients**: terminal = `piccolo chat` wrapping PicoClaw's own `picoclaw agent` REPL via docker exec; web = launcher image at `:18800`. No custom TUI is built.
- **Interaction modes**: terminal (gateway image) or web console (launcher image, port 18800), chosen in the wizard.

## Open Questions (verify during implementation)

- Container stop grace period (currently 10s, then kill).

Resolved during implementation: runtime tool names verified (`web_search`, `web_fetch`, `message`, `spawn`, `subagent`, `exec`, `read_file`, ...); `~/.picoclaw/...` tilde expansion verified by a live agent turn; gateway-image entrypoint behavior verified; `/health` requires no auth; launcher image (`--mode web`) smoke-tested end-to-end (health via gateway child, `:18800` → `/launcher-setup` password flow).
