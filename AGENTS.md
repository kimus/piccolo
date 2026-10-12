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

1. Critical doctor subset first (daemon reachable, both config files present + parseable). Fail fast, actionable messages.
2. Connect via Docker Go SDK (current docker context: socket, rootless, remote).
3. Remove stale container with the configured name (crash-safe restart).
4. Create container: image from `piccolo.yaml` (pull once on `No such image`); bind mount abs workspace → `/root/.picoclaw`; env resolved (literals + `env:` refs); ports + network mode from manifest; labels `piccolo.workspace=<abs path>` and `piccolo.managed=true`; `Init: true`; default image entrypoint; `OpenStdin/AttachStdin/AttachStdout/AttachStderr: true`, `Tty: false`.
5. Attach **before** start (never lose early stderr), then start.
6. Print: `Starting PicoClaw gateway container "<name>" from workspace <path> ...`
7. Pump container stdout/stderr to process streams; forward host SIGINT/SIGTERM → graceful container stop (timeout, then kill).
8. On exit: stop + remove container; exit with the container's exit code when observable.

The Docker SDK SHALL be isolated behind an internal `Runner`/`Process` interface so SDK types never leak and unit tests run daemon-free.

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
- No detached serve / `piccolo logs` / `piccolo stop` (fast-follow candidates).
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
5. `piccolo serve` starts the container, attaches stdio, prints the diagnostic line, forwards SIGINT/SIGTERM to a graceful stop, removes the container, exits with the container's exit code.
6. `piccolo serve` recovers from a crashed previous run (stale container replaced by name).
7. Generated `config.json`: ONE gateway, multiple agents in `agents.list`, container-side workspace paths, ≥1 `model_list` entry.
8. `piccolo.yaml` contains no PicoClaw-domain configuration.
9. Generated `AGENT.md` files have valid frontmatter and useful role instructions.
10. Unit tests: manifest strict parsing, config generation determinism, doctor checks (daemon-free via interface), init non-destructive behavior, container spec composition (mounts/env/ports/labels).

## Decisions Log

- **Runtime delivery**: Docker container via Go SDK — no `picoclaw` on `$PATH`.
- **Config split**: `piccolo.yaml` = launch-only; `config.json` = PicoClaw behavior, generated once, user-owned forever. No duplication.
- **Workspace = picoclaw home via default mount**: init writes everything directly into the workspace dir (default cwd, user-chosen); serve mounts it onto `/root/.picoclaw`. `PICOCLAW_HOME` never set. No `.piccolo/` subfolder, no `home:` key.
- **Config format**: PicoClaw reads JSON (schema v3); YAML only for `piccolo.yaml` and `AGENT.md` frontmatter.
- **API keys**: plaintext in `config.json`; mitigated by generated `.gitignore` + doctor placeholder warnings.
- **Serve lifecycle**: foreground attach for the PoC; detached deferred.
- **Interaction modes**: terminal (gateway image) or web console (launcher image, port 18800), chosen in the wizard.

## Open Questions (verify during implementation)

- Launcher image entrypoint behavior when `config.json` is pre-mounted (same skip-onboarding logic as the plain image?).
- Exact runtime tool names accepted in `AGENT.md` frontmatter `tools:` (config-side names verified; frontmatter allowlist names to double-check).
- `~/.picoclaw/...` tilde expansion for `agents.list[].workspace` inside the container (defaults use it upstream; per-agent entries should be tested).
- Container stop grace period (suggest 10s, then kill).
