# AGENTS.md — Implementation Guide

Instructions for coding agents implementing Piccolo. Read [README.md](README.md) first for the user-facing model. This file contains the binding constraints, verified upstream facts, and acceptance criteria.

## Hard Constraints

- The binary produced by this project SHALL be named `piccolo`.
- A reachable Docker daemon SHALL be required. Piccolo talks to it through the Docker Go SDK (`github.com/docker/go-sdk/client`, pinned exactly: `v0.1.0-alpha013`, plus `github.com/moby/moby/api` and `github.com/moby/moby/client`). The `docker` CLI SHALL NOT be a hard dependency.
- There SHALL be no `picoclaw` binary requirement on `$PATH`, no binary-path flag, and no binary downloader.
- `piccolo.yaml` SHALL contain Piccolo-owned configuration: container launch settings plus non-secret setup metadata. There SHALL be NO duplicated PicoClaw runtime configuration between `piccolo.yaml` and `config.json` (no assistant name, model, tools, agent lists, channel tokens, allowlists, or enabled state in `piccolo.yaml`).
- Every credential-bearing file is written `0600` and any pre-existing wider mode is tightened: `piccolo.yaml` (the passphrase), `picoclaw/config.json` (plaintext credentials under `--no-encrypt`), `picoclaw/config.json.bak` (whatever the replaced version held), and `picoclaw/.security.yml`. The rule lives in ONE place, `internal/fileperm` (`Private`, `Tighten`, `EnsurePrivate`, `WritePrivate`) — read by the manifest writer, the JSON doc writer, the init plan, and doctor. Add a credential-bearing file there, never at a call site. Two facts make this non-obvious: `os.WriteFile` applies its mode only on CREATE (so a rewrite of an existing `0644` file changes nothing without an explicit chmod), and `os.Rename` gives the target the TEMP file's mode (so a write silently upgrades a file the owner made read-only unless the original bits are remembered and restored). Tightening SHALL only REMOVE bits (`0444 -> 0400`, never `0444 -> 0600`): a read-only file is a supported way to signal the user does not want Piccolo editing it, and handing the write bit back quietly undoes that. `piccolo doctor` warns while a file that actually holds a plaintext credential, or the passphrase, is group/world readable — and stays silent on an encrypted `config.json`, which holds nothing.
- The one permitted secret in `piccolo.yaml` is `secrets.passphrase`, the enc:// decryption passphrase. It is an accepted trade, not an oversight: it buys a workspace that starts with no host setup and costs the at-rest protection `enc://` would otherwise give, because `piccolo.yaml` is committed by design. Every message, help text and doc that mentions encryption SHALL state that limitation rather than implying a copied workspace is safe.
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
- **MCP is gated twice, and both gates fail silently.** `tools.mcp.enabled` must be true or `ensureMCPInitialized` returns before it ever reads `tools.mcp.servers` (`pkg/agent/agent_mcp.go:79`); the toggle defaults to **false** (`pkg/config/defaults.go:428-431`). Separately, a *declared* AGENT.md `tools:` key becomes a strict exact-name allowlist (`pkg/agent/tool_allowlist.go:143-164` → `pkg/tools/registry.go:171-184`), while MCP tools are named `mcp_<server>_<tool>` only at runtime (`pkg/tools/integration/mcp_tool.go:143-152`), so no static list can contain them. A gateway in either state answers `/health` and runs its integration binaries: registered is not reachable.
- MCP stdio servers must speak **newline-delimited JSON**. PicoClaw uses the official SDK transport (`github.com/modelcontextprotocol/go-sdk`, `mcp/transport.go:100,116`), which has no `Content-Length` framing; a server emitting LSP-style headers fails the handshake with `calling "initialize": invalid character 'C' looking for beginning of value` — and because that error fails the turn outright, it breaks **every** chat message, not just the integration.
- Tool toggles are real config fields: `tools.web.enabled`, `tools.exec.enabled`, `tools.spawn.enabled`, `tools.subagent.enabled`, plus per-tool entries (`web_fetch`, `message`, `read_file`, `write_file`, `edit_file`, `list_dir`, `cron`, `mcp`, `skills`, ...).
- Models: top-level `model_list`: `{ model_name, provider, model, api_base, api_keys, ... }`. Agents reference models by `model_name`. `api_keys` (`SecureStrings`) has **no env-expansion** — verified: `ModelConfig.APIKeys` carries no `env:` tag (`pkg/config/config.go:786`), unlike web-tool and channel keys, which do.
- `picoclaw.HasUsableKey` treats an unfilled upstream example value (`YOUR_DISCORD_BOT_TOKEN`, the shape in `config/config.example.json`) as not usable. The older rule matched only the hyphenated `your-` prefix, so an untouched example read as a real credential and doctor stayed silent. The shape test stays narrow — all-uppercase, contains `_` — because real tokens carry a lowercase letter, hyphen, or colon (`sk-…`, `xoxb-…`, `123456:ABC…`); `TestDoctorStillAcceptsRealCredentialShapes` pins that narrowness, since a false positive here is a warning on a working credential.
- Secure fields have three accepted value forms, resolved by `pkg/credential` at load: plaintext, `file://<path>` (relative to the config file's directory, traversal and symlink escapes refused), and `enc://<base64>` (AES-256-GCM; needs `PICOCLAW_KEY_PASSPHRASE` plus an SSH key reachable at `PICOCLAW_SSH_KEY_PATH`, `PICOCLAW_HOME`, or `~/.ssh`). Piccolo currently generates plaintext only.
- `enc://` needs TWO inputs, and PicoClaw derives its AES key from the SSH key file's **raw bytes**: `ikm = HMAC-SHA256(key = SHA256(ssh_key_bytes), msg = passphrase)`, then `HKDF-SHA256(ikm, salt, "picoclaw-credential-v1", 32)` → AES-256-GCM over `base64(salt[16] || nonce[12] || ciphertext)`. The key file is never parsed as an SSH key — it is a high-entropy second secret that both sides must read identically, so any reformat of it silently invalidates every credential derived from it. Missing passphrase → `enc:// passphrase required`; wrong passphrase or wrong key → `decryption failed … message authentication failed`. **Both abort the whole config load and the container exits 1**, so they are startup failures, never degraded service.
- **`PICOCLAW_SSH_KEY_PATH` is required, not optional.** `DefaultSSHKeyPath()` is `~/.ssh/picoclaw_ed25519.key`, and inside the gateway container `HOME=/root` with nothing mounted at `/root/.ssh` — a key found by that search is image content, gone on recreate. `allowedSSHKeyPath` accepts only `$PICOCLAW_SSH_KEY_PATH` (exact match), inside `$PICOCLAW_HOME`, or inside `~/.ssh`; Piccolo never sets `PICOCLAW_HOME`, so the env-var branch is the only one reachable, and it is why the key lives under the already-mounted `picoclaw/keys/`.
- **`picoclaw onboard -E` cannot be used to encrypt a Piccolo workspace.** It calls `createWorkspaceTemplates`, which unconditionally rewrites `AGENT.md`, `SOUL.md`, `USER.md` and `memory/` into `agents.defaults.workspace` — the user-owned `~/.picoclaw/agents/main`. Confirmed by reading `cmd/picoclaw/internal/onboard/helpers.go`. Piccolo therefore implements the format itself (`internal/credential`), pinned to the upstream release by a both-directions fixture test.
- **`.security.yml` outranks `config.json` for every field it names.** PicoClaw's own `SaveConfig` writes all secure fields into `picoclaw/.security.yml` and emits a `[NOT_HERE]` sentinel (or omits the field) in `config.json` — no passphrase required, so one web-console save or `picoclaw model add` is enough to move them. `LoadConfig` then merges the sidecar over config.json (`pkg/config/config.go:1480`), per entry: `model_list` keys are `"<model_name>:<duplicate-ordinal>"` with a bare-name fallback, an entry naming `api_keys` **replaces** it even when the list is empty (empty clears), and a *null* entry is skipped (config.json survives). Channels merge per settings field, and an explicit `token: ""` there overrides a real config.json token. A sidecar that does not parse aborts the whole config load.
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
secrets:                                    # optional block; absent means plaintext
  passphrase: ""                            # enc:// passphrase, stored here (default on writes it)
```

- Strict decoding: unknown keys SHALL cause a parse error. This is also what makes a comment-preserving writer safe: a file carrying a stray key cannot be loaded, so it can never be re-written with that key transplanted into it.
- `piccolo.yaml` rewrites SHALL preserve comments. `Manifest.Write` renders FROM the struct and transplants comments onto that render by location (mapping keys matched by name, sequence items by index) — it SHALL NOT merge the old document into a tree the writer then maintains, because a field such a walker misses is deleted from the file holding the passphrase rather than merely un-commented. Comments before the first key attach to the yaml **Document** node, not the root mapping, so the `piccolo init` header is only kept by merging the document node too. A key the struct no longer emits (an empty `omitempty` block) loses its comment, and SHALL NOT be re-added to host it.
- `secrets.passphrase` holds the enc:// decryption passphrase. This is a deliberate trade: it buys a workspace that starts with no host setup, and it costs the at-rest protection `enc://` would otherwise provide, because `piccolo.yaml` is the one workspace file meant to be committed and shared. Only whitespace-only is rejected; any other string is a valid passphrase. An absent `secrets:` block means the workspace stores plaintext.
- `env:HOST_VAR` refs resolve from host env at `serve`; missing refs → doctor warning + serve-time error.

### `config.json` generation

Generated from wizard answers, PicoClaw schema v3. Minimum shape:

```json
{
  "version": 3,
  "agents": {
    "defaults": {
      "workspace": "~/.picoclaw/agents/main",
      "restrict_to_workspace": false,
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
5. Ensure `picoclaw/config.json`, `picoclaw/.security.yml`, `picoclaw/.picoclaw.pid`, `picoclaw/launcher-auth.db`, `picoclaw/keys/`, `picoclaw/logs/`, integration auth state, runtime caches, and OS directory metadata (`.DS_Store`) are git-ignored. The required entries live in ONE list, `generate.GitignoreEntries`, read by init, by doctor's check, and by the flat-layout migration: they previously had three copies and had drifted, so a workspace missing the credential sidecar was reported as clean. Add an entry there, never at a call site. Appending never rewrites existing lines, so a user's own rules survive; re-running `init` is therefore the documented heal for a workspace created before an entry was required, and `piccolo doctor` reports missing entries (warning only) because nothing else would.

- Credentials are encrypted by default; `--no-encrypt` opts out and `--passphrase VALUE` supplies the passphrase non-interactively. Because the default is on, `--yes` without `--passphrase` is refused with both ways out named — silently generating one would hand the user a secret they never chose and cannot distinguish from a mistake. The choice must be carried in BOTH paths: `--yes` never enters `wizard.Run`, so it is copied in `inputsFromPrefill` as well as in the wizard. Key generation happens after "Proceed?" and the plan is rebuilt before writing, so an abort creates no key file and a completed run never writes a plaintext byte.

- Preferred prompt library: `github.com/charmbracelet/huh`. Keep wizard logic separated from file generation (upgrade path to full Bubble Tea).
- Support non-interactive flags (`--name`, `--owner`, `--purpose`, `--yes`, ...) for scripting.
- The huh form requires a real terminal: on a zero-size pty, upstream bubbles panics in `textinput.placeholderView` (`makeslice: len out of range`). `runInit` therefore guards the interactive path with `term.IsTerminal` and points piped/CI callers to `--yes` + flags instead.
- Interactive flow verified end-to-end via a sized-pty harness: all questions, multi-select toggle, password input, plan preview, per-file overwrite prompts, and generation produce a doctor-clean workspace.

### `piccolo setup` credential toggle

`piccolo setup` → Container toggles encryption for an existing workspace. Enabling prompts for a passphrase (confirmed twice), generates the key if absent, and rewrites the plaintext already in `config.json` — a toggle that only affected future writes would leave the credentials exactly where they were while the menu claimed "on". Disabling refuses to rewrite existing ciphertext and says so: Piccolo has nowhere to put the plaintext it would produce, and silently decrypting into `config.json` behind a toggle is not the user's decision made for them.

### Credential storage location

In an encrypted workspace, a credential lives in `picoclaw/.security.yml` and NOWHERE else. PicoClaw's loader merges that file over `config.json` field by field and lets it win, so a value duplicated in `config.json` is never read while remaining readable on disk — the exact state `enc://` exists to remove. `setup.EncryptWorkspace` therefore moves each credential into the overlay (sealing it there, copying an existing `enc://` value verbatim rather than re-encrypting), and deletes the `config.json` value only once the overlay has accepted it. The overlay is created when there is something to move and never otherwise; PicoClaw's own empty stubs for absent models are pruned, since they hold tokens nothing loads.

This reverses the earlier "Piccolo never writes `.security.yml`" rule, and the two operations that rule conflated are now separate: writing a NEW credential into a field the overlay governs is still refused (that value would be ignored), while healing the overlay's own plaintext is the point of `secrets encrypt`.

### `piccolo secrets`

`encrypt` and `check`. `encrypt` generates the workspace key if absent, rewrites every plaintext credential in `config.json` as `enc://`, records `secrets.passphrase`, and appends any missing gitignore entry. It declines a field the sidecar governs — naming the sidecar key — because a ciphertext written where the gateway reads the sidecar is a secret stored for nothing. It resolves the passphrase BEFORE generating a key, so a refused prompt leaves no stray key file in a workspace whose credentials are still plaintext.

`piccolo setup` seals at one choke point, `applyAndConfirm`, which both the model and channel forms pass through, so a path cannot be forgotten; its `encrypted` posture requires BOTH a key file and a recorded passphrase, since either half alone describes a workspace that cannot boot. When the passphrase is missing it refuses the whole write rather than storing plaintext in a workspace the user believes is protected.

`--passphrase` is accepted as a flag, deliberately. Because the value goes into `piccolo.yaml` anyway — a committed file — refusing a flag would protect nothing while making non-interactive use impossible. What is enforced instead is that a prompted passphrase is confirmed twice, since a typo would otherwise be committed as the only way in.

`secrets check` verifies decryptability from the workspace alone, because the alternative failure is a container that exits at boot with an error naming `config.json`.

### `piccolo doctor`

Checks: Docker daemon reachable (SDK ping); image present locally (warn + pull hint if missing, not a failure); `piccolo.yaml` exists/parses strictly/pinned tag; structured workspace layout exists (or legacy flat-layout migration guidance is reported); `picoclaw/config.json` exists/parses/`version == 3`; `agents.list` non-empty with exactly one `default: true`; every referenced agent workspace exists **on the host** (translate `~/.picoclaw/...` → `<workspace>/picoclaw/...`) with parseable `AGENT.md` frontmatter; `model_list` non-empty (warn on placeholder/empty keys); secret sidecar (read `picoclaw/.security.yml` before judging any key; an unparseable sidecar is `[FAIL]`, because PicoClaw aborts the whole config load on it): an `[OK]` line whenever the file exists, model and channel keys judged by whichever file governs them — `[OK] model: x (y) — key from picoclaw/.security.yml` when config.json no longer carries it, WARN when the sidecar overrides a config.json key the user can still see (their `piccolo setup` edit there is a no-op), WARN when it clears one (empty list or empty string, gateway runs without the credential), and WARN for a sidecar key no `model_list` entry claims (a rename or delete left the credential guarding nothing); never print a value from that file, only field and key names; exec posture (warn when `agents.defaults.restrict_to_workspace` is true — an absent key counts as true, per `pkg/config/defaults.go:29` — naming that the exec guard ignores `allow_read_outside_workspace`; a fresh workspace with the generated `false` is clean, never warned); MCP reachability (when any `tools.mcp.servers` entry is registered: warn if `tools.mcp.enabled` is not true, if a server is individually disabled, or if the coordinator declares a `tools:` allowlist that cannot name MCP tools — and print an `[OK]` line when the servers really are reachable, since this failure was silent; silent when no integration is installed); MCP registration (an integration recorded in `piccolo.yaml` with a `mcp_server_key` and no matching `tools.mcp.servers.<key>` entry in `config.json` is installed but **never registered** — registration is the last act of `piccolo integration setup`, behind a confirmation gate the user can decline, so warn and name that command instead of leaving the user with "I added it and it silently does nothing"; an integration with no recorded `mcp_server_key` is not missing one); running image versus manifest (inspect the container named `container.name` and compare the image it was created from with `container.image`: equal → `[OK] running container image: …`, different → WARN naming both and `piccolo restart`, because the running gateway holds the OLD integration binaries while bind-mounted `config.json` is re-read immediately; absent container → silent, since the stale-container check already covers it); every `env:VAR` ref resolves (warn); a container with the configured name that exists but is NOT running (warn — `serve` will replace it; a running container built from the current image is the service, not a leftover, and warning about it gave every healthy workspace a permanent false warning; a running one built from a different image is reported once, by the running-image check, with the verb that fits — restart); web-console consistency (a `:18800` binding paired with a non-launcher image → warn about the dead port; a disabled console is clean, never warned).

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

### A copy is an output — fix the source

Every file in this section is rendered from `internal/generate/templates.go`. **Editing a workspace copy is not a fix**, in the repo or in a scratch workspace like `.data/`: it changes one output while the template still renders the old thing for everyone else, and no review or CI step sees an untracked scratch directory. The rule of thumb: a fix is real only when a fresh `piccolo init` produces it.

| workspace copy | source to change |
|---|---|
| `picoclaw/skills/file-handling/SKILL.md` | `generate.FileHandlingSkillMD()`; its tool list comes from `runtimeimage.DefaultCommands` |
| `picoclaw/agents/<id>/AGENT.md` | `generate.agentMD()` |
| `picoclaw/agents/<id>/SOUL.md` | `generate.soulMD()` |
| `picoclaw/agents/main/USER.md` | `generate.userMD()` |
| `picoclaw/agents/main/memory/MEMORY.md` | `generate.memoryMD()` |

Because the copies are user-owned and create-only, a template fix never reaches an existing workspace. Piccolo records what it wrote in `piccolo.lock` (workspace root: digest + source template per generated file) so `piccolo doctor` can tell the two states apart and report them differently:

- **customised** — the user's bytes. Reported as user-owned; Piccolo never suggests refreshing it, because that would advertise destroying their work.
- **stale** — byte-identical to what Piccolo wrote, but the current template renders something else. WARNed, naming the template to fix and `piccolo setup` as the remedy. Never a `FAIL`.

Currency is claimed only for templates that need no wizard answers (today: the file-handling skill). Agent files depend on inputs Piccolo does not retain, so doctor reports edited-or-not and stays silent about their currency rather than inferring it from a version string.

### Main agent (`agents/main`)

`AGENT.md` frontmatter — deliberately **no `tools:` line**:

```md
---
name: <AssistantName>
description: Coordinator assistant for <Owner>.
skills: [file-handling]
---
```

Omitting `tools:` is a capability decision, not cosmetics: PicoClaw reads a declared
`tools:` key as a strict, exact-name allowlist, and MCP tool names
(`mcp_<server>_<tool>`) are only known at runtime, so declaring a list on the
coordinator silently revokes every tool an installed integration provides. Undeclared,
the coordinator gets every tool `config.json` enables.

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
22. Applying an integration's MCP server entry runs a real `initialize` handshake in a one-shot container from the workspace runtime image, and writes `tools.mcp.enabled: true` only when the server answers with a JSON-RPC result; a failed, refused, framed, or impossible-to-run probe leaves the toggle as-is while still registering the server and stating why.
23. `piccolo doctor` distinguishes the three integration states a user cannot tell apart from the outside: installed-but-never-registered (warn, name `piccolo integration setup <ref>`), registered-but-unreachable (warn, name the closed gate), and reachable (an `[OK]` line) — and never invents a finding for an integration that declares no `mcp_server_key`.

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
- **Workspace path guard ships OFF**: `agents.defaults.restrict_to_workspace` is generated as an explicit `false`. PicoClaw defaults it to `true` when the key is absent (`pkg/config/defaults.go:29`), so "omit it" means "keep the guard on". And the guard is not the mild restriction it sounds like: the exec tool is built from the raw flag, not from the read access relaxed by `allow_read_outside_workspace` (`pkg/agent/instance.go:85-86,113`), and it inspects every absolute-path token in the command line (`pkg/tools/shell.go:1172,1278`) against only `/dev` pseudo-devices and `tools.allow_read_paths`. Guard-on plus exec-on therefore refuses ordinary commands — a `/status` turn that ran `uptime` and `uname` failed with `Command blocked by safety guard (path outside working dir)`, an error string the agent cannot interpret or work around. The container is the security boundary. Per-agent confinement stays available as a deliberate choice (`piccolo setup` → Tools → Workspace path guard) and `doctor` WARNs while it is on — a warning, never a `FAIL`, because the file is the user's. An existing workspace keeps whatever its `config.json` says; the warning is the remedy path, not a migration.
- **Registered means reachable, and Piccolo proves it before promising it.** Registration and enablement are separate writes because they are separate promises: an integration's `configPatch` authorizes its own `tools.mcp.servers.<key>` entry, while `tools.mcp.enabled` is what makes agents depend on it. So Piccolo registers, then runs a real `initialize` handshake against the recorded stdio command in a one-shot container built from the workspace's own runtime image, and writes `tools.mcp.enabled: true` only when the server answers with a JSON-RPC result. Two verified facts force the split. While the toggle is false, PicoClaw skips MCP initialization entirely (`pkg/agent/agent_mcp.go:79`; default false per `pkg/config/defaults.go:428-431`), so a registered server is inert — and the integration cannot flip the toggle itself, because the patch allowlist confines it to its own entry. With the toggle **on** and a server that cannot handshake, PicoClaw fails the whole turn, so every chat message errors instead of only that integration's. An unverifiable probe (no engine, nothing spawnable in the entry) therefore registers without enabling, and says so. This exists because the old silent shape was indistinguishable from success: `piccolo-gws status` passed, `/health` passed, and a user who asked the assistant to check their email was told no mailbox was configured while `credentials.enc` and a live JSON-RPC handshake said otherwise. `piccolo setup` → Tools still exposes the toggle so a verified flip stays visible and reversible.
- **The handshake verdict names the transport, not just "unreachable".** A first line starting `Content-Length:` is reported as LSP framing against MCP's newline-delimited JSON, because that is the actionable fact: `piccolo-gws` 0.3.4 accepted both framings on stdin but wrote only framed replies, which PicoClaw's client (the official MCP Go SDK) rejects with `invalid character 'C' looking for beginning of value`. Fixed in piccolo-gws 0.3.5 (`src/mcp.rs`: `write_message` now `writeln!`), verified end-to-end by feeding that binary's real reply through Piccolo's verdict; the GitHub release is still the maintainer's step.
- **The MCP toggle is verified at every path that can raise it, in every image that could serve it.** `tools.mcp.enabled` turns on only after each registered server completes an `initialize` handshake in a one-shot container — whether the enable came from `piccolo integration setup` applying a server entry or from `piccolo setup` → Tools by hand. Guarding only the registration path would leave the manual toggle (shipped in the same feature) as an unguarded way to break the gateway, which is what happened: the enable was right for the image `piccolo.yaml` names while the still-running container held the old binary. So the check probes each distinct image that could serve the toggle — the manifest's and the running container's — and reports a verification only with the image it was verified in, because "verified" without a target is the claim that misleads. A failed verification asks an explicit question that defaults to No; no daemon means "unverifiable", never a silent pass. `piccolo status` prints the same mismatch as a `stale:` line, because status is where a user looks when chat behaves badly and the symptom is a chat failure, not an image error.
- **The coordinator declares no `tools:` allowlist.** PicoClaw turns a declared key into a strict exact-name list and MCP tools are named `mcp_<server>_<tool>` at runtime, so any allowlist Piccolo writes at generation time silently revokes every integration tool the coordinator should have. Specialists keep their reduced lists — the reduction is the point — and `doctor` warns when an allowlist on the default agent is doing that damage. `piccolo setup`'s AGENT.md sync must therefore never *add* a `tools:` line to the coordinator: it would convert "no allowlist" into a strict one.
- **API keys**: `enc://` ciphertext by default, with the passphrase in `piccolo.yaml`. `--no-encrypt` falls back to plaintext (git-ignored + doctor placeholder warnings then carry the mitigation). Turning encryption off later does not rewrite existing ciphertext. This is the *generation* posture, not the steady state: once anything saves through PicoClaw the credentials move to `picoclaw/.security.yml`, which then decides what the gateway loads. Piccolo reads it and never prints a value from it, and an explicit `piccolo secrets encrypt` pass seals plaintext values inside it, because leaving them there makes `config.json`'s `enc://` decorative — the gateway loads the overlay. Nothing creates the file: absence is the good state, and it appears only when PicoClaw itself saves. `enc://` generation is now the default; `file://` refs remain unstarted.
- **`setup` refuses an inert credential write, and `serve` refuses to start on an unreadable sidecar.** Reporting was half the fix: a user who types a key into a field the sidecar overrides still gets a stored credential that will never be read, and `✓ wrote model x` for a change that did not happen. So `setup` declines the credential, names the sidecar key to edit, and still applies the non-secret fields of that entry; the key field says it is inert BEFORE the input, because a warning after the fact is how the user learns on their next failed turn; and a Discord token about to be refused skips its live check rather than reporting success for a credential Piccolo will not store. A rename and a delete both report the sidecar key they strand. The field still renders and stays reviewable — declining the write is not the same as suppressing the input. Piccolo still never creates, edits, or deletes `.security.yml`: it is PicoClaw's file, may hold `enc://` values, and Piccolo cannot re-encrypt without the passphrase.
- **Serve lifecycle**: detached + health-gated (`/health` on published 127.0.0.1 port) is the default; `--foreground` for attached stdio. Gateway gets `PICOCLAW_GATEWAY_HOST=0.0.0.0` injected when ports are published (container-loopback unreachable otherwise — verified by smoke test).
- **Chat clients**: terminal = `piccolo chat` wrapping PicoClaw's own `picoclaw agent` REPL via docker exec; web = launcher image at `:18800`. No custom TUI is built.
- **Web console (optional)**: gateway always runs (required); the web console is an add-on enabled by `--dashboard`. The `:18800` binding in `piccolo.yaml` is the single source of truth — no separate boolean that can drift from the ports. Console-off uses the lean gateway image and runs no dashboard process (no dead `:18800`); console-on uses the launcher image. `--web` was renamed to `--web-search` (agent tool toggle) to avoid ambiguity with `--dashboard`.
- **WiP: no compatibility or migration work.** Piccolo is a pre-1.0 proof of concept. Do not write migrations, back-compat shims, or upgrade paths for previously-created workspaces or images. When a change alters what a workspace's image must contain, the correct response is a **warning that tells the user to rebuild** (`piccolo integration add`/`upgrade`/`build`, or `piccolo image build`) — never an implicit automatic rebuild, and never a data migration. Re-creating a workspace by hand is acceptable. Additive fields are still required where they cost nothing (unknown descriptor/`config.json` keys must keep being ignored), but that is a parsing rule, not a migration policy.

## Open Questions (verify during implementation)

- Container stop grace period (currently 10s, then kill).

Resolved during implementation: runtime tool names verified (`web_search`, `web_fetch`, `message`, `spawn`, `subagent`, `exec`, `read_file`, ...); `~/.picoclaw/...` tilde expansion verified by a live agent turn; gateway-image entrypoint behavior verified; `/health` requires no auth; launcher image (`--dashboard`) smoke-tested end-to-end (health via gateway child, `:18800` → `/launcher-setup` password flow).
