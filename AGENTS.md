# AGENTS.md — Implementation Guide

Instructions for coding agents implementing Piccolo. Read [README.md](README.md) first for the user-facing model. This file contains the binding constraints, verified upstream facts, and acceptance criteria.

## Hard Constraints

- The binary produced by this project SHALL be named `piccolo`.
- A reachable Docker daemon SHALL be required. Piccolo talks to it through the Docker Go SDK (`github.com/docker/go-sdk/client`, pinned exactly: `v0.1.0-alpha013`, plus `github.com/moby/moby/api` and `github.com/moby/moby/client`). The `docker` CLI SHALL NOT be a hard dependency.
- There SHALL be no `picoclaw` binary requirement on `$PATH`, no binary-path flag, and no binary downloader.
- `piccolo.yaml` SHALL contain Piccolo-owned configuration: container launch settings plus non-secret setup metadata. There SHALL be NO duplicated PicoClaw runtime configuration between `piccolo.yaml` and `config.json` (no assistant name, model, tools, agent lists, channel tokens, allowlists, or enabled state in `piccolo.yaml`).
- `config.json` and agent workspace files are generated **once** by `piccolo init` and are **user-owned** afterwards. Piccolo SHALL never silently regenerate or overwrite them.
- The implementation SHALL run a single gateway container hosting multiple PicoClaw agents — not one container per agent.
- Do NOT implement custom Piccolo memory, native PicoClaw tools, MCP servers, or custom runtime logic.
- Do not assume PicoClaw features — verify against the pinned version's source before relying on any flag, field, or behavior.
- An action's declared inputs SHALL ALL render. State facts gate which actions are selectable and SHALL NEVER remove a field from a form; `whenUnmet` is parsed for wire compatibility and ignored. A suppressed field can be neither reviewed nor replaced, and the surviving fields get submitted as if they were the whole answer.
- The answers document distinguishes three states explicitly: non-empty **sets**, empty **clears**, absent **keeps**. Only an untouched sensitive field may be absent. A secret's value SHALL NOT be sent outbound or placed inside a field as a masked placeholder.

## Verified PicoClaw Facts (v0.3.1)

Verified against `github.com/sipeed/picoclaw@v0.3.1` source (present in the Go module cache at `~/go/pkg/mod/github.com/sipeed/picoclaw@v0.3.1/`). Treat as ground truth:

- `PICOCLAW_HOME` overrides the base dir for all PicoClaw data (default `~/.picoclaw`). Config file: `<home>/config.json` — **JSON, schema `version: 3`**. No YAML main-config support. Piccolo does NOT set `PICOCLAW_HOME`: we mount onto the in-container default (`/root/.picoclaw`, since `HOME=/root`).
- Multi-agent in one gateway: `agents: { defaults, list: [ { id, default, name, workspace, model, skills, subagents: { allow_agents: [] } } ], dispatch }` (`pkg/config/config.go`: `AgentsConfig`, `AgentConfig`).
- Tool toggles are real config fields: `tools.web.enabled`, `tools.exec.enabled`, `tools.spawn.enabled`, `tools.subagent.enabled`, plus per-tool entries (`web_fetch`, `message`, `read_file`, `write_file`, `edit_file`, `list_dir`, `cron`, `mcp`, `skills`, ...).
- Models: top-level `model_list`: `{ model_name, provider, model, api_base, api_keys, ... }`. Agents reference models by `model_name`. `api_keys` (`SecureStrings`) has **no verified env-expansion** — assume plaintext on disk.
- Agent workspaces: `AGENT.md` (YAML frontmatter: `name, description, tools, model, maxTurns, skills, mcpServers` — `pkg/agent/definition.go`), `SOUL.md`, `USER.md`, `memory/MEMORY.md`. Legacy `AGENTS.md` is a fallback; use `AGENT.md`.
- Official images: `docker.io/sipeed/picoclaw:<version>` (gateway/agent) and `docker.io/sipeed/picoclaw:launcher` (web console + gateway, ports `18800`/`18790`, needs `PICOCLAW_GATEWAY_HOST=0.0.0.0`).
- Image `/entrypoint.sh`: skips onboarding when `config.json` exists in home, removes stale `.picoclaw.pid`, then `exec picoclaw gateway "$@"`. Rely on this default entrypoint.
- PicoClaw home also holds `auth.json`, `.security.yml`, `.picoclaw.pid`, `logs/`, `launcher-auth.db`, default `workspace/`, global `skills/`, channel state — runtime writes back, so mount the WHOLE `picoclaw/` dir, not just `config.json`.
- Health API: `/health` and `/ready` are **unauthenticated**; only `POST /reload` requires a bearer token (`pkg/health/server.go`). Served on `gateway.port` (default **18790**, config key `gateway.port`, env `PICOCLAW_GATEWAY_PORT`).
- The gateway binds `localhost` by default. A process on the container's loopback is **unreachable through published ports** — when publishing, piccolo injects `PICOCLAW_GATEWAY_HOST=0.0.0.0` into the container env (unless the manifest already sets it, or network mode is `host`). Verified by smoke test.
- Clients: the ONLY terminal client is `picoclaw agent` (readline REPL; `-m` for one-shot). There is no `picoclaw-launcher-tui` — the launcher is web-only (`picoclaw-launcher`, React dashboard :18800, proxies chat to the gateway via `/pico/ws`, spawns `picoclaw gateway -E` as its own child in the launcher image). First run: `:18800` redirects to `/launcher-setup` (create dashboard password), afterwards `/launcher-login`. Verified live: launcher image works with a pre-mounted `config.json`, the gateway health port (:18790) is served by the launcher's gateway child.

## Workspace & Mounting

- Workspace = the target directory itself (default: cwd; `-D/--dir` flag overrides). `piccolo.yaml` lives at the root; PicoClaw-owned files live under `picoclaw/`; integration state under `integrations/`; runtime caches under `runtime/`.
- Workspace resolution rule is shared by ALL commands: `-D/--dir` flag, else cwd. `serve`/`doctor` fail with `no piccolo workspace found — run 'piccolo init' first` when `piccolo.yaml` is absent.
- Mounts: `<workspace>/picoclaw` → `/root/.picoclaw`, `<workspace>/integrations` → `/root/.piccolo/integrations`, and `<workspace>/runtime/media` → `/tmp/picoclaw_media` plus per-agent media mirrors. `runtime/media` is the only directory under `runtime/` that Piccolo creates. Nothing SHALL be bind-mounted over `/usr` or `/nix`: the toolchain is image content, and integration bundles unpack their own runtime closure into `/nix/store`, so shadowing either makes the image's own binaries unexecutable in the running gateway while one-shot containers (which take no such bind) still work.
- All `workspace` paths in `config.json` SHALL be container-side paths, e.g. `~/.picoclaw/agents/main` (image runs with `HOME=/root`; upstream defaults use `~`, tilde expansion supported).

### `piccolo.yaml` schema

```yaml
version: 1
container:
  image: docker.io/sipeed/picoclaw:v0.3.1  # pinned tag, never "latest" in generated manifests
  name: piccolo-gateway
  network: bridge                           # none | bridge | host
  ports: []                                 # web console enabled: ["127.0.0.1:18800:18800"]
  env: {}                                   # literal or "env:HOST_VAR" (resolved at serve time)
  previous_image: ""                        # single rollback slot, recorded on promote
setup:                                      # optional non-secret setup metadata
  channels:
    discord:
      application_id: ""                    # used to regenerate invite/setup links
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
    { "model_name": "<alias>", "provider": "<provider>", "model": "<model-id>", "api_keys": ["<key>"], "api_base": "<optional>" }
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
- **Model entries use the explicit-provider form** (`provider` + bare `model`, never a `provider/model` concatenation). PicoClaw resolves in two steps: when `provider` is set, `model` is sent to that provider **unchanged** (a redundant `<provider>/` prefix is NOT stripped upstream, so piccolo strips it at generation); when `provider` is empty, PicoClaw infers it from the first `/` segment. Explicit provider is upstream's preferred shape (see picoclaw `docs/operations/troubleshooting.md`, OpenRouter free-tier). Both `init` (wizard collects provider + model separately) and `setup models` emit it.
- Deterministic output: stable key order, 2-space indent. No Piccolo branding inside the file.

## Command Behavior

### `piccolo init`

1. Check Docker daemon reachability; warn (not fail) if unreachable.
2. Ask wizard questions: assistant name; owner name; purpose template (personal/research/coding/operations/blank); specialist agents (research, planner, coder, ops); expose the optional **web console** (`--dashboard`; gateway always runs, this only adds the launcher image + `127.0.0.1:18800` + `PICOCLAW_GATEWAY_HOST=0.0.0.0` env); model provider + model + API key (placeholder allowed with warning); web search (`--web-search`, default yes); shell exec (default no); workspace path (default cwd).
3. Preview the generation plan (files, image, ports), then final confirmation.
4. Write files. **Create-only**: if files exist, list them and require explicit per-file confirmation before any overwrite.
5. Ensure `picoclaw/config.json`, `picoclaw/.security.yml`, `picoclaw/.picoclaw.pid`, `picoclaw/launcher-auth.db`, `picoclaw/logs/`, integration auth state, and runtime caches are git-ignored: create `.gitignore` or append if entries are missing.

- Preferred prompt library: `github.com/charmbracelet/huh`. Keep wizard logic separated from file generation (upgrade path to full Bubble Tea).
- Support non-interactive flags (`--name`, `--owner`, `--purpose`, `--yes`, ...) for scripting.
- The huh form requires a real terminal: on a zero-size pty, upstream bubbles panics in `textinput.placeholderView` (`makeslice: len out of range`). `runInit` therefore guards the interactive path with `term.IsTerminal` and points piped/CI callers to `--yes` + flags instead.
- Interactive flow verified end-to-end via a sized-pty harness: all questions, multi-select toggle, password input, plan preview, per-file overwrite prompts, and generation produce a doctor-clean workspace.

### `piccolo doctor`

Checks: Docker daemon reachable (SDK ping); image present locally (warn + pull hint if missing, not a failure); `piccolo.yaml` exists/parses strictly/pinned tag; structured workspace layout exists (or legacy flat-layout migration guidance is reported); `picoclaw/config.json` exists/parses/`version == 3`; `agents.list` non-empty with exactly one `default: true`; every referenced agent workspace exists **on the host** (translate `~/.picoclaw/...` → `<workspace>/picoclaw/...`) with parseable `AGENT.md` frontmatter; `model_list` non-empty (warn on placeholder/empty keys); every `env:VAR` ref resolves (warn); stale container with configured name (warn — `serve` will replace it); web-console consistency (a `:18800` binding paired with a non-launcher image → warn about the dead port; a disabled console is clean, never warned).

Output: `[OK]`/`[WARN]`/`[FAIL]` status lines (see README for the exact format). Exit `0` unless any `[FAIL]`; warnings never fail.

### `piccolo serve`

Starts the gateway container **detached** and gates the command:

1. Critical doctor subset first (daemon reachable, both config files present + parseable). Fail fast, actionable messages.
2. Derive the gateway port from `config.json` `gateway.port` (default 18790).
3. Recover an interrupted promotion first: if `<name>.prev` exists while nothing holds the canonical name, rename it back, start it, and report the interruption. The canonical name SHALL always belong to the container that should be running.
4. Report runtime-image drift as a warning with the reason (read from the current image's content labels) and the rebuild command. Piccolo SHALL never rebuild implicitly, and an unverifiable check SHALL NOT block startup.
5. Compose the launch spec: image/name/network/ports/env from `piccolo.yaml`; auto-publish `127.0.0.1:<gwPort>:<gwPort>` when the manifest doesn't already map that container port (skip for `network: host`); inject `PICOCLAW_GATEWAY_HOST=0.0.0.0` unless the manifest env already sets it; labels `piccolo.managed=true`, `piccolo.workspace=<abs path>`.
6. **Promote when the existing container runs a different image**: stop the current gateway keeping it (`ContainerStop`, no remove), rename it to `<container.name>.prev` (frees the canonical name, releases the published port), then create/start the target image under the canonical name. Same image → plain replace, no parking.
7. Gate: poll `http://127.0.0.1:<gwPort>/health` (1s interval, 90s budget), then **execute** every installed integration's recorded status command through `docker exec` in the gateway. Readiness requires that the binary runs, not that it is configured: exit 126/127, an exec error, or silent-with-failure output is a gate failure; a non-zero exit that produced a status payload is a pass (setup gap, not broken image). No integrations → health alone.
8. Gate passes: remove the parked target, record the replaced image as `container.previous_image`, print health + console + next-step hints, exit 0.
9. Gate fails: print the stage, the failing probe command and its output, plus the candidate's last log lines **before** removing it; then stop/remove the candidate, rename the parked target back, start it, and exit non-zero. If the restore itself fails, say plainly that neither gateway is running — never report a rollback that did not restore service. No revert target exists → keep the container for debugging as before.

`piccolo restart` delegates to serve (it no longer stops-and-removes first: that discarded the revert target). `--foreground/-f` keeps the attached behavior — pump stdio, forward SIGINT/SIGTERM to a graceful stop, remove the container, propagate its exit code — and runs the same park/gate/commit-or-revert around the attached run.

`piccolo integration rollback` switches `container.image` back to `container.previous_image` (or `--to`) and reuses this promotion path. It SHALL verify the target exists locally before touching the running gateway, and SHALL restore the manifest setting if the gate rejects it.

Container roles travel by **name**, not label: this Docker SDK sets labels only at create time and `ContainerUpdateOptions` has no `Label` field, so a candidate label could never be cleared when it won.

The Docker SDK SHALL be isolated behind an internal `Runner`/`Process` interface so SDK types never leak and unit tests run daemon-free.

### `piccolo stop` / `piccolo status` / `piccolo logs`

- `stop`: graceful `ContainerStop` (grace 10s) + remove by name; message when not running.
- `status`: container state (found/running/status/exit code) + live `/health` probe with body.
- `logs [-f] [--tail N]`: stream via `ContainerLogs`, demux the docker framing, stop following on Ctrl-C.

### `piccolo chat`

Terminal chat by wrapping PicoClaw's own client through `ExecCreate`/`ExecAttach` (no custom TUI), as a verbatim pass-through — no stream rewriting:

- `chat` (interactive): TTY exec of `picoclaw agent`; local terminal goes raw (`golang.org/x/term`), bytes flow both ways unmodified, restore on exit, propagate the remote exit code. Requires a TTY and a running gateway.
- `chat -m <msg>` (one-shot): non-TTY exec of `picoclaw agent -m <msg>`; demux output verbatim; wait for the exec exit code via `ExecInspect`.

The upstream banner, lobster emoji, and client log lines are shown as-is; gateway logs remain available via `piccolo logs`.

The web console is optional and enabled solely by a `:18800` binding in `piccolo.yaml` (which pairs with the launcher image); the browser chat lives at `http://127.0.0.1:18800`. The gateway runs regardless of this choice.

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

- No clearing of an optional-and-sensitive field (absence means keep there by construction), and no gesture to wipe stored credentials — a `reset_credentials` action stays an open question, not something to improvise.
- No custom memory MCP server, SQLite store, tasks/reminders/scheduler.
- No native PicoClaw tool registration or hooks.
- No PicoClaw binary bundling, downloading, or `$PATH` usage.
- No detached/foreground ambiguity: serve is detached + health-gated; `--foreground` attaches.
- No custom terminal chat UI — `piccolo chat` wraps `picoclaw agent` via exec.
- No `piccolo add-agent` (manual edit of `config.json` + workspace; doctor catches mistakes).
- No full channel automation; setup may guide credentials and provider links, but provider-side app/server installation remains user-driven.
- No regeneration/migration of an existing `config.json`.
- No migration from piccolo-full beyond code reuse.

## Implementation Notes

- Go. CLI: Cobra. Prompts: `charmbracelet/huh`. YAML: `gopkg.in/yaml.v3`.
- Docker deps pinned exactly (go-sdk is pre-1.0, never float): `github.com/docker/go-sdk/client v0.1.0-alpha013`, `github.com/moby/moby/api v1.52.0`, `github.com/moby/moby/client v0.1.0`, `github.com/containerd/errdefs v1.0.0`.
- Reuse from `../piccolo-full`: `internal/isolation/dockerapi` pattern (prepare/start split, attach-before-start, stale removal by name, pull-once, ownership labels, demuxed stdio) and the `Runner`/`Process` interfaces.
- Deterministic generation everywhere: sorted keys, stable templates — workspace diffs must be readable.
- Package layout: `cmd/piccolo` (CLI: root/init/doctor/serve/lifecycle/chat/workspace), `internal/manifest` (piccolo.yaml), `internal/picoclaw` (config.json schema v3), `internal/generate` (plan/render/write), `internal/doctor` (checks), `internal/docker` (SDK runner), `internal/wizard` (huh prompts), `internal/workspace` (dir/layout resolution).

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
11. `piccolo.yaml` contains no duplicated PicoClaw runtime configuration; only launch settings and non-secret Piccolo setup metadata.
12. Generated `AGENT.md` files have valid frontmatter and useful role instructions.
13. Unit tests: manifest strict parsing, config generation determinism, doctor checks (daemon-free via interface), init non-destructive behavior, container spec composition (mounts/env/ports/labels), health-port/gateway-host injection helpers.
14. The runtime image contains the toolchain: a gateway started from a locally built image executes every configured `runtime.toolchain.commands` entry and every installed integration binary with **no** host bind mounts beyond media/integrations/`picoclaw`, and no start-time install step runs.
15. Two different versions (or digests) of one installed integration produce different runtime image tags, and the tag is computable from `piccolo.yaml` metadata alone — a drift check downloads no artifact and starts no container.
16. A locally built image carries `piccolo.base`, `piccolo.toolchain`, and one `piccolo.integration.<owner>/<repo>` label per installed release; drift reporting uses them to name what moved.
17. A stale runtime image produces a warning naming the reason and `piccolo image build --set`; it never triggers a build, and an unverifiable check never blocks `serve`.
18. Promotion parks the running gateway as `<name>.prev`, gates the candidate on health **plus executed integration binaries**, and reverts (with captured diagnostics) when the gate fails, exiting non-zero; success removes the parked container and records `container.previous_image`.
19. An interrupted promotion (parked target, nothing on the canonical name) is detected and restored by the next `serve`, and reported by `doctor`/`status`.
20. `piccolo integration rollback` adopts the recorded previous image through the same gate, and refuses with the gateway untouched when that image is not present locally.
21. `piccolo image list` marks in-use / rollback-target / container-referenced images, and `piccolo image prune` removes nothing outside that floor; without `--yes` and without a terminal it removes nothing at all.

## Decisions Log

- **Toolchain is image content, not runtime state**, installed by the **base distribution's package manager** (Alpine `apk`) in its own layer, before integration layers. Devbox/Nix was removed: it produced the same 23-command toolchain for 4.28 GiB of store closures plus a ~0.58 GiB floor (image ~8.26 GB, cold build ~6m50s) because nixpkgs closures are self-contained — the OCR package alone shipped 129 language files (1015 MB) where English is 23 MB. apk gives the identical command list at 17 packages for a **2.08 GB image, 1.6 GB rootfs, 58s build**. Measured in the running gateway: LibreOffice 7.6.7.2, pdftotext 24.02.0, tesseract 5.5.0, and a full `pandoc → docx → soffice → pdf → pdftotext` chain.
- **No runtime tool installation.** A missing tool means the image was built without it; the skill tells the agent to report that rather than run a package manager. On-demand install was considered and rejected as complexity the bake already solves: `runtime.toolchain.packages` plus a ~1-minute rebuild is the extension path.
- **`runtime.toolchain.packages` are apk names**, passed to `apk add` verbatim, so an entry can pin (`jq=1.7.1-r0`). apk has no lockfile: the content-derived tag identifies what was *requested*, not the exact resolved bytes, and changing the base image's Alpine version is a rebuild.
- **Source-built integrations are retired.** `piccolo integration build` resolved a Nix flake ref and has no apk equivalent; release bundles carry their own `/nix/store` closure (gws 0.3.4 ships 1261 store paths, including bash and glibc, in its 30 MB tarball), so Nix was never load-bearing for them. A manifest that still records `source: build` loads fine and **rebuilds refuse it** with the `piccolo integration add <pkg>` remedy — while drift checks keep working, because an already-built image still runs.
- **Image identity lives in the tag AND the labels.** The tag covers `owner/repo@version+digest` per installed release (also carried in the Dockerfile's label block, so removing one mechanism still cannot reuse a stale layer) and is computable from metadata only. Labels answer "what is in this image?" and name the reason for a rebuild; the tag stays the cache identity. Content labels are also why a running container's identity survives a promotion rename.
- **Health is not readiness; presence is not proof.** The promote gate executes each installed integration's status command inside the gateway. A container where `command -v piccolo-gws` succeeded and `piccolo-gws state` printed "not found" is the exact failure this catches. A non-zero exit that produced a payload PASSES (unconfigured is a setup gap, not a broken image).
- **Promotion keeps a revert target, and roles travel by name.** `<name>.prev` holds the stopped previous gateway; the canonical name always belongs to what should be running, which leaves one abnormal state to detect. Container labels cannot be used for this: this Docker SDK sets labels only at create time and `ContainerUpdateOptions` has no `Label` field, so a "candidate" label could never be cleared on the winner.
- **Image reclamation keeps an explicit floor.** `piccolo image list` reports locally built runtime images with the reason each was kept; `piccolo image prune` removes only images that are neither `container.image`, nor `container.previous_image`, nor referenced by any container in any state, and base images are never candidates. Sizes are quoted as an upper bound because layers are shared. `doctor` warns at 2 images / 10 GB and deletes nothing.
- **Drift is content, not tags.** Identical content labels with a differing tag means only Piccolo's build recipe moved (`DriftTemplate`), reported as a quiet note — asking for a rebuild there would train users to ignore the warning. Rebuilding remains explicitly user-initiated.
- **`piccolo restart` no longer stops first.** Stopping-and-removing before knowing whether the replacement works turned a bad image into an outage with nothing to fall back to; serve owns the park/gate/commit-or-revert sequence.
- **`image build` composes the whole workspace image** (toolchain + every installed integration), because a rebuild command that silently drops integrations is worse than no rebuild command; it is what the drift warning names. On adopt it also refreshes the recorded version/digest/commands from the artifacts it actually placed in the image — otherwise the record and the image diverge and no rebuild ever clears the warning.
- **Two gaps found while testing with a second integration, documented rather than papered over.** There is no `piccolo integration remove`: uninstalling is a manifest edit plus a rebuild (WiP scope). And the GitHub release fetcher sends no credentials, so private integration repos read as 404 — integration releases must be public for now. Neither is a surprise to leave in the code undocumented.
- **`runtime apply` became a check, not an installer.** Installing into a running container would recreate the two-sources-of-truth problem the bake removes.

- **Runtime delivery**: Docker container via Go SDK — no `picoclaw` on `$PATH`.
- **Config split**: `piccolo.yaml` = Piccolo-owned launch settings + non-secret setup metadata; `config.json` = PicoClaw runtime behavior, generated once, user-owned forever. No duplicated runtime configuration.
- **Structured workspace + split mounts**: init writes PicoClaw-owned files under `picoclaw/`, integration state under `integrations/`, and runtime caches under `runtime/`; serve mounts `picoclaw/` onto `/root/.picoclaw` and integrations onto `/root/.piccolo/integrations`. `PICOCLAW_HOME` never set. No `home:` key.
- **Config format**: PicoClaw reads JSON (schema v3); YAML only for `piccolo.yaml` and `AGENT.md` frontmatter.
- **API keys**: plaintext in `config.json`; mitigated by generated `.gitignore` + doctor placeholder warnings.
- **Serve lifecycle**: detached + health-gated (`/health` on published 127.0.0.1 port) is the default; `--foreground` for attached stdio. Gateway gets `PICOCLAW_GATEWAY_HOST=0.0.0.0` injected when ports are published (container-loopback unreachable otherwise — verified by smoke test).
- **Chat clients**: terminal = `piccolo chat` wrapping PicoClaw's own `picoclaw agent` REPL via docker exec; web = launcher image at `:18800`. No custom TUI is built.
- **Web console (optional)**: gateway always runs (required); the web console is an add-on enabled by `--dashboard`. The `:18800` binding in `piccolo.yaml` is the single source of truth — no separate boolean that can drift from the ports. Console-off uses the lean gateway image and runs no dashboard process (no dead `:18800`); console-on uses the launcher image. `--web` was renamed to `--web-search` (agent tool toggle) to avoid ambiguity with `--dashboard`.
- **WiP: no compatibility or migration work.** Piccolo is a pre-1.0 proof of concept. Do not write migrations, back-compat shims, or upgrade paths for previously-created workspaces or images. When a change alters what a workspace's image must contain, the correct response is a **warning that tells the user to rebuild** (`piccolo integration add`/`upgrade`/`build`, or `piccolo image build`) — never an implicit automatic rebuild, and never a data migration. Re-creating a workspace by hand is acceptable. Additive fields are still required where they cost nothing (unknown descriptor/`config.json` keys must keep being ignored), but that is a parsing rule, not a migration policy.

## Open Questions (verify during implementation)

- Container stop grace period (currently 10s, then kill).

Resolved during implementation: runtime tool names verified (`web_search`, `web_fetch`, `message`, `spawn`, `subagent`, `exec`, `read_file`, ...); `~/.picoclaw/...` tilde expansion verified by a live agent turn; gateway-image entrypoint behavior verified; `/health` requires no auth; launcher image (`--dashboard`) smoke-tested end-to-end (health via gateway child, `:18800` → `/launcher-setup` password flow).
