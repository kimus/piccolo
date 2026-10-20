# Piccolo

Piccolo is a thin, opinionated setup and launcher for [PicoClaw](https://github.com/sipeed/picoclaw) — create, validate, and run a multi-agent AI assistant with three commands. PicoClaw runs inside a Docker container managed by Piccolo; you never install PicoClaw by hand.

> **Status:** PoC. See [AGENTS.md](AGENTS.md) for implementation guidance.

## How it works

```text
piccolo init      interactive wizard → creates the workspace in the current directory
piccolo setup     arrow-navigable editor for an existing workspace
                  (channels like Discord, models, tool toggles — merge-patched,
                  with provider setup steps, diff preview, and config.json.bak)
piccolo integration add kimus/piccolo-gws
                  install/select a release-packaged integration in the gateway
                  image (does not patch config.json itself)
piccolo integration setup kimus/piccolo-gws
                  run the integration-owned setup command in a temporary
                  container from the configured image
piccolo secrets   encrypt this workspace's credentials (enc://) / check they decrypt
piccolo doctor    validates Docker, workspace, and generated files
piccolo serve     starts the PicoClaw gateway container in the background,
                  waits until http://127.0.0.1:18790/health answers
piccolo restart   promote/replace the gateway with the current config (reverts on failure)
piccolo reload    apply config.json changes without recreating the container
piccolo chat      talk to the assistant in the terminal (PicoClaw's own REPL)
piccolo status    container + health state
piccolo logs      gateway logs (-f to follow)
piccolo stop      stop and remove the container
piccolo integration rollback   go back to the previously running runtime image
```

```text
┌────────────────────────────┐
│ piccolo                    │
│ setup + container launcher │
└────────────┬───────────────┘
             │ writes once (init), then user-owned
             ▼
┌─────────────────────────────────────────┐
│ workspace dir (default: current dir)    │
│   piccolo.yaml       (piccolo-owned)    │
│   picoclaw/          (PicoClaw home)    │
│   integrations/      (integration state)│
│   runtime/           (tool/cache data)  │
└────────────┬────────────────────────────┘
             │ bind-mounted into container
             ▼
┌────────────────────────────┐
│ Docker container           │
│ docker.io/sipeed/picoclaw  │
│ entrypoint → picoclaw      │
│              gateway       │
└────────────────────────────┘
```

## Install

```sh
curl -fsSL https://kimus.github.io/piccolo/install.sh | sh
```

Install a specific version:

```sh
curl -fsSL https://kimus.github.io/piccolo/install.sh | sh -s -- --version 0.3.0
```

The installer downloads the matching GitHub Release archive, verifies `checksums.txt`, and installs only the host `piccolo` CLI. It does not install Docker, PicoClaw, or integrations.

Manual installs can download a release archive and `checksums.txt` from GitHub Releases, verify the SHA-256 digest, and place `piccolo` on `PATH`.

## Requirements

- Go 1.25+ (only when building from source)

`piccolo --version` reports the build date so a stale local binary is obvious:

```text
piccolo version 0.3.0 (built 2026-10-15T12:00:18Z, go1.26.5)   # stamped release build
piccolo version 0.3.0-dev (dev build, not stamped; ...)          # plain `go build`
```
- A reachable Docker daemon (Docker Desktop, colima, podman socket, ...). The `docker` CLI is **not** required — Piccolo talks to the daemon through the Docker Go SDK.

## Quickstart

```sh
# build
go build -o piccolo ./cmd/piccolo

# create an assistant workspace in a new directory
mkdir my-assistant && cd my-assistant
piccolo init        # wizard: assistant name, agents, model, tools, optional web console

# validate
piccolo doctor

# start the gateway in the background (returns when healthy)
piccolo serve

# talk to your assistant
piccolo chat                 # interactive REPL
piccolo chat -m "hello!"     # one-shot

# inspect / follow / stop
piccolo status
piccolo logs -f
piccolo stop
```

The gateway always runs. If you enabled the optional **web console** (launcher image), open `http://127.0.0.1:18800` after `serve` starts.

## Workspace layout

`piccolo init` creates a structured workspace (default: current directory; choose another with the wizard or `-D/--dir`):

```text
<workspace>/
  piccolo.yaml       # Piccolo-owned launch config and non-secret setup metadata
  piccolo.lock       # what generation wrote: digest + source template per generated file
  .gitignore         # ignores secrets and runtime caches
  picoclaw/          # PicoClaw-owned home, mounted to /root/.picoclaw
    config.json      # PicoClaw config (JSON schema v3, generated once, then yours)
    .security.yml    # optional PicoClaw secret overlay (git-ignored)
    launcher-auth.db # web launcher auth DB when dashboard is enabled (git-ignored)
    logs/            # PicoClaw gateway/launcher logs (git-ignored)
    agents/
      main/          # coordinator agent (always)
        AGENT.md  SOUL.md  USER.md  memory/MEMORY.md
      research/      # optional specialists
      planner/
      coder/
      ops/
  integrations/      # integration-owned state, mounted to /root/.piccolo/integrations
    gws/
      auth/
      backend.json
  runtime/           # media mirror only (older workspaces may also hold unused devbox/nix dirs)
    media/           # -> /tmp/picoclaw_media and per-agent media mirrors
```

Mount ownership is intentionally split:`/root/.picoclaw` is PicoClaw's home only (including PicoClaw runtime files like `.security.yml`, `logs/`, and `launcher-auth.db`), while integration packages persist their state under `/root/.piccolo/integrations/<name>` (for Google Workspace, `<name>` is `gws`). The toolchain is **not** mounted from the host: it is baked into the runtime image with the base distribution's package manager (`apk`). A host bind over `/nix` would hide the runtime closure that integration bundles unpack there. `runtime/devbox` and `runtime/nix` may still exist in older workspaces from the previous mechanism. They are unused leftovers: Piccolo no longer creates them, never mounts them, and never deletes them. To clear one, note that a Nix store directory is read-only (`555`), so a plain `rm -rf` fails partway:

```sh
chmod -R u+w <workspace>/runtime/nix && rm -rf <workspace>/runtime/nix <workspace>/runtime/devbox
```

## Configuration ownership

There is **no duplicated configuration** between the two files:

| File | Owner | Contents |
|---|---|---|
| `piccolo.yaml` | Piccolo | Container launch config plus non-secret setup metadata |
| `picoclaw/config.json` | You (after init) | PicoClaw runtime behavior: agents, models, tools, channels, tokens |

`picoclaw/config.json` and the agent files are generated **once** by `piccolo init` and are yours afterwards — edit them directly to change models, add agents, or tune tools. Piccolo never silently regenerates or overwrites them.

Because your copies are yours to keep, a fix to Piccolo's own templates does not reach an existing workspace. Piccolo therefore records what it wrote in `piccolo.lock` — digest plus source template per generated file — and `piccolo doctor` reports the two very different states separately:

| Finding | Meaning | What to do |
|---|---|---|
| *customised by you* | the bytes on disk differ from what Piccolo wrote | nothing; it is your file, and Piccolo will not overwrite it |
| *predates the current template* | byte-identical to what Piccolo wrote, but Piccolo's template has changed since | refresh via `piccolo setup` → "Sync AGENT.md allowlists and file-handling skill" |

A stale report names the source (for example `internal/generate/templates.go:FileHandlingSkillMD`), because the fix belongs there and not in the copy: editing a workspace copy changes one output while the template keeps rendering the old text for everyone else. Both findings are warnings or notes — neither ever fails `doctor`, and Piccolo never rewrites anything on its own.

### `piccolo.yaml`

```yaml
version: 1
container:
  image: docker.io/sipeed/picoclaw:v0.3.1  # pinned tag; :launcher when the web console is enabled
  name: piccolo-gateway                     # deterministic container name
  network: bridge                           # none | bridge | host
  ports: []                                 # web console enabled: ["127.0.0.1:18800:18800"]
  env:                                      # "env:VAR" reads the host env at serve time
    {}
  previous_image: ""                        # rollback slot: image run before the last promotion
setup:                                      # optional non-secret setup metadata
  channels:
    discord:
      application_id: "123456789012345678"
```

## Commands

### `piccolo init`

Interactive wizard: assistant name, owner, purpose template, specialist agents (research/planner/coder/ops), whether to expose the optional web console, model provider + API key, tool toggles (web search default on, shell exec default off), workspace path.

The PicoClaw gateway always runs; the **web console** is an optional add-on. It is recorded purely by the `:18800` binding in `piccolo.yaml` — to enable it later, set `container.image` to the `:launcher` tag and add `"127.0.0.1:18800:18800"` to `container.ports`; to disable it, use the pinned gateway image and remove that binding. `piccolo doctor` warns if a `:18800` binding is paired with a non-launcher image, and also checks the channel the console's chat actually rides on (see [`piccolo doctor`](#piccolo-doctor)).

- Create-only: existing files are never overwritten without explicit per-file confirmation.
- Non-interactive flags (`--name`, `--owner`, `--purpose`, `--yes`, ...) are supported for scripting.
- Ensures `config.json`, PicoClaw runtime files, integration auth state, runtime caches, and OS metadata such as `.DS_Store` are git-ignored (missing lines are appended; existing lines, including yours, are never rewritten).


### `piccolo integration`

Containerized integrations are self-describing packages baked into the gateway image before PicoClaw starts. Normal installs use GitHub Release bundles addressed by package reference:

```sh
piccolo integration add kimus/piccolo-gws          # latest release
piccolo integration add kimus/piccolo-gws@0.3.4   # exact release tag/selector
piccolo integration add kimus/piccolo-gws --force # rebuild even if unchanged
piccolo integration setup kimus/piccolo-gws
piccolo restart

# later, advance latest-channel integrations
piccolo integration remove <name>    # undo manifest record + image + registered MCP server; state preserved
piccolo integration upgrade kimus/piccolo-gws
piccolo integration remove <name>    # undo manifest record + image + registered MCP server; state preserved
piccolo integration upgrade        # all latest-channel integrations
piccolo restart

# verify provider connectivity
piccolo integration status kimus/piccolo-gws
```

`add` and `upgrade` resolve the release **before** asking anything, and skip the work entirely when nothing would change:

```text
$ piccolo integration add kimus/piccolo-gws --yes
✓ Already up to date: kimus/piccolo-gws 0.3.0 (sha256:131f8cf4172f)
  image: piccolo/picoclaw-runtime:efd4e52201df
  state: integrations/kimus/piccolo-gws (preserved)
```

When something *would* change, the confirmation lists it first — resolved version, artifact name and digest, base image, toolchain packages, and which integrations are unaffected — because the prompt previously asked "install or update?" with no information at all. Only after a yes does Piccolo download the (multi-megabyte) artifact and build. `--force` overrides the short-circuit; `--yes` skips the prompt but not the check.

Package refs use `[provider:]owner/repo[@selector]`; the default provider is GitHub. Short aliases such as `gws` are not supported. The selector is provider/repository-owned and opaque, so `kimus/piccolo-gws@0.3.4` and `johndoe/piccolo-maker@v2.1.10` are both valid if those GitHub Release tags exist.

`add` installs or updates the selected release artifact, verifies its digest from `piccolo-integration-release.json`, runs the installed binary's `describe --json` in a container, and records Piccolo-owned integration metadata in `piccolo.yaml`. It preserves state and does **not** patch provider-specific `config.json` entries.

`list` shows locally installed integrations without live provider checks:

```sh
piccolo integration list
piccolo integration list --json
```

`setup` drives the integration's declared actions in a one-shot container from the configured image, with `picoclaw/` mounted at `/root/.picoclaw` and `integrations/` mounted at `/root/.piccolo/integrations`. Integration state is package-scoped under `integrations/<owner>/<repo>` on the host and `/root/.piccolo/integrations/<owner>/<repo>` in the container.

Ownership is split deliberately:

- **The integration** decides what needs doing and reports state facts (`describe`, `state`, `call <action-id>`). It renders no terminal UI and never writes `config.json`.
- **Piccolo** renders the menu and its inputs from that declaration, collects secrets on stdin rather than through argv or the environment, relays OAuth browser callbacks from the host into the container's loopback, and applies any `config.json` change an action proposes — to `tools.mcp.servers.<key>` for that integration and nowhere else, after showing the diff. One writer means one backup convention and one confirmation gate.
- **Provider-side work stays yours**: creating the OAuth client in the provider console (a Desktop-app client, which registers no redirect URI), and installing any app or bot with the permissions you want.

After setup, run `piccolo restart`.

### How the setup form behaves

Every field an action declares is always shown. State facts decide which **actions** you can pick; they never delete a field from the form — a hidden field can neither be reviewed nor replaced, and an action whose own fields disappear is a form that submits itself empty.

- **A field left as shown keeps its value.** Where the integration reports what it stores, non-secret fields are pre-filled, so pressing enter resubmits what is already there and deleting a value deliberately clears it.
- **A required field cannot be skipped or submitted empty.** Piccolo blocks advancing and refuses to invoke the action, with a message pointing at `esc`. An integration that declares no requiredness keeps the old behaviour: everything is optional.
- **A secret is never shown or sent back.** A stored client secret is reported as *already set*, and the field stays empty: pressing enter keeps it, typing replaces it. There is no masked placeholder sitting in the field as a value, because half-editing one produces a wrong secret that looks present.
- **`esc` goes back to the menu.** Nothing is saved for the action you abandoned, and a configuration change proposed by an action that already finished is still offered at the end. Quitting from the menu itself exits without applying anything.

Pre-filling and requiredness depend on the integration publishing them (`state.values`, `inputs[].required`). An older release still renders every field correctly; its fields simply start empty and none are enforced as required.

The wire encoding carries those decisions explicitly: a non-empty value sets, an empty value clears, and a key that is absent means keep (reserved for an untouched secret).

`upgrade [package]` advances latest-channel release installs. Fixed selector installs remain pinned; change versions by running `piccolo integration add <owner/repo@selector>` again. Setup is not rerun automatically.

Source-built integrations are **no longer supported**: that path resolved a Nix flake reference, and the runtime image installs with `apk`. Install from a published release instead — the release bundle carries its own runtime closure, so it needs nothing extra in the image. A `piccolo.yaml` that still records `source: build` loads normally, and any image rebuild refuses it with the remedy:

```sh
piccolo image build
# Error: integration "github:kimus/piccolo-gws" is recorded as built from source ("github:kimus/piccolo-gws?ref=main"), which is no longer supported: … Reinstall it from a published release with 'piccolo integration add github:kimus/piccolo-gws' (…)
```

`status <package>` runs the integration-owned connectivity checks in a one-shot container and renders provider-specific results through a generic Piccolo status format. For Google Workspace, this is where Gmail, Calendar, and Drive connectivity is checked:

```sh
piccolo integration status kimus/piccolo-gws
piccolo integration status --all
piccolo integration status kimus/piccolo-gws --json
```

Piccolo itself stays provider-neutral: if a check fails, the integration supplies the service-specific message and remediation. Status uses the same structured mounts as setup (`picoclaw/` and `integrations/`) and can run even when the gateway is stopped.

A failing report is not a Piccolo error: integrations signal unhealthy checks by exiting non-zero while printing their status document, so Piccolo renders the full report (and emits JSON with `--json`) and then exits non-zero itself. Only unparseable or missing output is reported as an invocation failure.

Integration authors publish release bundles plus provide an executable whose descriptor command returns JSON like:

```json
{
  "schema": "piccolo.integration.v1",
  "name": "gws",
  "displayName": "Google Workspace",
  "commands": ["piccolo-gws"],
  "setup": {
    "command": ["piccolo-gws", "setup", "--config-dir", "/root/.piccolo/integrations/kimus/piccolo-gws"],
    "requiresTty": true,
    "ports": ["127.0.0.1:8000:8000"]
  }
}
```

Only use integrations you trust; integration packages install code into the assistant runtime image.

### `piccolo integration setup`

For integrations that declare actions, **Piccolo renders the setup menu** with the same
`huh` widgets used by `piccolo init`/`piccolo setup`; the integration ships no terminal UI.

```text
? Google Workspace — what do you want to do?
  ▸ Set OAuth client credentials
    Login / authorize Google account
    Check Gmail, Calendar, and Drive connectivity
    Done
```

Every action does one thing. After it finishes, Piccolo re-reads the integration's
state facts and re-renders the menu, so what is left to do rises to the top and
newly unlocked actions appear — no composite "do everything" entry, and no
re-running `setup` between steps. Pick **Done** to leave; that is also when the
proposed `config.json` change is previewed and confirmed, once.

How it works:

```text
integration describe  → stable action menu (ids are opaque to Piccolo)
piccolo runs: state   → live facts gate the menu (no doomed flows)
piccolo runs: call X  → answers on STDIN · NDJSON events on STDOUT · logs on STDERR
```

- **Menu gating.** An action whose `requires` facts are false is shown as unavailable with the missing prerequisite named, and Piccolo refuses to invoke it.
- **Conditional inputs.** An input may declare `whenUnmet: <fact>`; Piccolo skips it once that fact holds, so the credentials form does not re-ask for a client ID you already stored, and login stops re-asking for scope once authorized.
- **Credentials are values, not files.** Google Cloud Console shows a client ID and a client secret; Piccolo asks for those two fields (the secret masked) and the integration writes what its backend needs, mode `0600`, inside its own state directory. No download, no file browser, no copying into the workspace. Secrets travel on stdin only — argv is visible in `docker top`, env in `docker inspect`. (A `path` input still exists in the vocabulary for service-account-style integrations that genuinely need a key file.)
- **Never register a redirect URI in Google Cloud Console.** Piccolo prints the callback port it is listening on (e.g. `http://localhost:43111`), and the integration re-picks it on **every** attempt, so a registered port works once and then breaks. The OAuth client must be of type **Desktop app**, which needs no registered redirect URI because Google's installed-app flow accepts a loopback redirect on any port. If you feel obliged to fill in a redirect URI, the client you created is a **Web application** — the wrong type. Nothing can pin the port either: the delegated backend binds an ephemeral one and exposes no option for it.
- **Browser callbacks.** When an action emits `wait_redirect`, Piccolo binds the loopback port taken from that URL's own `redirect_uri`, captures exactly one request, and relays the raw query into the still-running container. Integrations publish no ports, and Piccolo never interprets or logs the credential material.
- **Config writes are Piccolo's.** An integration *proposes* a `configPatch`; Piccolo validates it against a fixed allowlist (only that integration's `tools.mcp.servers.<key>` entry), shows a diff, asks for confirmation, then writes `config.json.bak` and the file atomically. A patch reaching any other path is refused. That confinement is deliberate — an integration must not be able to flip tool toggles — so Piccolo owns the one toggle registration depends on, `tools.mcp.enabled`, and writes it only after it has verified the server itself.
- **Headless** (no TTY, CI): Piccolo refuses rather than hanging, so setup stays deterministic.

A registered integration is not automatically a usable one, and nothing used to say the difference out loud. Two gates stand between a server entry and a tool the model can call: `tools.mcp.enabled` (PicoClaw returns from MCP initialization before it reads the servers map while it is false, and it defaults to false), and a `tools:` line in an agent's `AGENT.md` — which PicoClaw treats as a strict, exact-name allowlist, while MCP tools are named `mcp_<server>_<tool>` only at runtime, so no written-out list can contain them. Piccolo generates the coordinator with **no `tools:` allowlist** so every tool `config.json` enables reaches it (specialists keep their reduced lists), and it verifies the handshake before the toggle is ever turned on: each registered server's recorded stdio command is started in a one-shot container, given one `initialize` request, and judged on the first line it writes back. Only a JSON-RPC result enables the tool — **on both paths**, `piccolo integration setup` applying a server entry and `piccolo setup` → Tools flipping the toggle by hand, because a manual enable is the one that can leave you with a verified-good image and a stale container. `piccolo doctor` reports the result as a status line — `✔ ok mcp: 1 server(s) reachable by coordinator "main" (…)` — or names whichever gate is closed, and warns when an integration in `piccolo.yaml` was installed but never registered at all.

The toggle is withheld rather than forced, because an enabled MCP tool with an unreachable server is worse than a disabled one: PicoClaw fails the **whole turn** when a registered server cannot initialize, so every chat message errors, not just the ones asking about that integration. One real example: `piccolo-gws` 0.3.4 accepted both framings on stdin but answered with LSP-style `Content-Length:` headers, which the MCP transport PicoClaw uses cannot read (`invalid character 'C' looking for beginning of value`); 0.3.5 answers one JSON line per message. That bug came back a second way worth knowing about: after upgrading, the **new image** handshook perfectly while the **still-running container** held the 0.3.4 binary, and chat failed anyway — because `integration add` bakes an image and rewrites `container.image`, but only a restart swaps the running binaries, while `config.json` is re-read immediately. So the enable check probes every image that could serve the toggle (the manifest's and the running container's), and both `piccolo doctor` and `piccolo status` now report the running container's image against `piccolo.yaml`:

```text
✔ ok   running container image: piccolo/picoclaw-runtime:cd4c6f2a1baa
⚠ warn container "piccolo-gateway" serves image piccolo/picoclaw-runtime:5f6018fa00b8, but
       piccolo.yaml names …:cd4c6f2a1baa
       → the running gateway holds the OLD integration binaries while config.json is re-read
         immediately; run 'piccolo restart' before enabling anything the new image provides
```

If the verification fails on the manual path, Piccolo prints every failing server with its reason, says plainly that enabling anyway means all chat traffic fails, and asks — defaulting to keeping MCP off. On the registration path the server entry is still written, because it is the integration's own configuration output and worth keeping; only the enablement is withheld. If there is no daemon to ask, Piccolo reports the check as unverifiable rather than passing it silently.

An integration that declares no actions keeps the previous TTY-attach behavior unchanged —
the descriptor stays `piccolo.integration.v1`, so old integrations and old Piccolo builds both work.

### `piccolo setup`

The editing counterpart of `init` for an existing workspace. `piccolo setup` opens an arrow-navigable home menu (↑/↓ move, enter open, q quit); deep-links work too: `piccolo setup channels`, `piccolo setup channels discord`, `piccolo setup models`, `piccolo setup tools`.

- **v1-editable**: channels (Discord/Telegram/Slack — list, add, toggle, edit with pre-filled values), models (add, edit, rename, delete, and the default/image selection), web/exec/spawn/subagent/load_image/mcp toggles, an advanced remote-exec toggle, and the workspace path guard (`agents.defaults.restrict_to_workspace`, off by default). Agents/container/identity sections are listed but require manual edits for now.
- Discord setup asks whether you already have an application, helps you create one when needed, requires the Application ID + bot token, validates the token with Discord when possible, and prints direct install/test links afterwards.
- **Comment-preserving `piccolo.yaml`**: your notes in `piccolo.yaml` survive a rewrite by `piccolo setup` or `piccolo secrets encrypt`, including the `# Generated by piccolo init` header and comments on nested fields, list items and `env` entries. Content always comes from Piccolo's own view of the workspace, so a value you changed is updated under its comment, and a field Piccolo no longer writes loses both field and note.
- **Merge-patched writes**: unknown keys and untouched subtrees of your user-owned `config.json` are preserved; every change shows a diff preview, requires confirmation, and backs the previous file up to `config.json.bak`.
- Bot tokens and model keys are stored plaintext in `config.json` by default (PicoClaw has no env expansion for model keys — verified). Non-secret setup metadata such as Discord Application ID is stored in `piccolo.yaml` so setup can regenerate links later.
- **Credentials are encrypted by default.** A new workspace stores each API key and bot token as PicoClaw `enc://` ciphertext, generated with a workspace key at `picoclaw/keys/`. The passphrase is written to `piccolo.yaml` under `secrets.passphrase`, so the gateway starts with nothing exported and no prompt. Non-interactively that means `piccolo init --yes --passphrase VALUE`; `--no-encrypt` gives plaintext instead, and `piccolo setup` → Container toggles it either way on an existing workspace.

Be clear about what that buys, because it is less than `enc://` alone would suggest:

| What this does | What this does not do |
|---|---|
| removes readable API keys from the workspace files — a grep, a screenshot, a pasted config no longer surfaces it | **protect a copy of the workspace.** The passphrase sits in `piccolo.yaml`, the one file here meant to be committed and shared, so a clone, tarball, or backup of the directory can be decrypted |
| keeps credentials out of a repo that accidentally commits `config.json` | hide anything from someone with host or Docker access — the passphrase is passed to the container env, visible to `docker inspect` |
| | protect `config.json.bak`, which holds the plaintext the change replaced — it is git-ignored, but a backup you did not mean to keep is still a copy of the old secret |

  Every file that can hold a secret is written `0600`: `piccolo.yaml` (the passphrase), `picoclaw/config.json` (plaintext credentials when you used `--no-encrypt`), `picoclaw/config.json.bak`, and `picoclaw/.security.yml`. An older workspace that predates this gets a `piccolo doctor` warning naming the file and its mode, and the next write tightens it. Tightening only ever removes permissions — a file you made read-only stays read-only, because that is a supported way to tell Piccolo not to edit it. `config.json.bak` matters most: it is a copy of the config from *before* you encrypted it, so it can still contain the plaintext key the sealing run removed, and `piccolo doctor` flags it while it is both readable and holding a real credential.

  If "safe to copy" is what you actually need, the passphrase has to live outside the workspace. Losing it is permanent either way: the credentials must be re-entered, and Piccolo keeps no recovery path. Turning encryption off later does not hand the plaintext back — existing `enc://` values stay encrypted and keep working.

- **`.security.yml` is not exempt.** PicoClaw's overlay overrides `config.json` field by field, so an encrypted `config.json` next to a plaintext `.security.yml` is a workspace that only looks encrypted — the gateway loads the plaintext. `piccolo secrets encrypt` seals the overlay's credential values too, in place, leaving every other node and comment intact. It never creates the file: a workspace without one is already in the good state, and it appears only when PicoClaw itself saves.
- `piccolo secrets check` answers the question a workspace cannot otherwise ask: whether the passphrase and key file present right now can actually decrypt what `config.json` holds. Run it before `serve`, because a credential that cannot be decrypted is a gateway that exits on startup, not one that degrades.
- **`.security.yml` outranks `config.json`.** Any save through PicoClaw itself — the web console, `picoclaw model add`, a version migration — moves every credential out of `config.json` into `picoclaw/.security.yml`, which PicoClaw then merges back over `config.json` on load, per field, and wins. So a key that is no longer visible in `config.json` is usually not missing, and a key that *is* visible there may be dead. Piccolo reads that file to tell the two apart: `piccolo doctor` judges every model and channel key by whichever file actually governs it, and `piccolo setup` refuses to write a credential into a field the sidecar overrides — naming the sidecar key to edit instead, because a secret stored where the gateway will not read it is a silent failure with a fresh token in it. Piccolo never creates, edits, or prints `.security.yml`; it is PicoClaw's file and may hold `enc://` values Piccolo cannot decrypt. Doctor reports the two states of that file that look identical from `config.json`: a credential parked for a channel you switched off (an `[OK]` line naming the file), and a credential for a channel `config.json` no longer lists at all — a `[WARN]`, because PicoClaw re-adds the entry when it merges the sidecar, so deleting a channel does not drop the secret that guards it, and Piccolo cannot remove it for you.
- If the gateway container is running afterwards, setup reminds you to `piccolo reload` (config.json) or `piccolo restart` (piccolo.yaml).

#### Models

`piccolo setup models` is where a workspace's models live:

- **Set default text model** writes `agents.defaults.model_name` — the alias agents inherit unless their own `model` or `AGENT.md` frontmatter pins one. **Set image/vision model** writes `agents.defaults.image_model`, used for image attachments.
- **Editing an entry's alias renames it in place** and re-points every reference: `model_name`, `image_model`, both fallback lists, and each agent's `model`. The confirmation lists what it re-pointed. An alias already in use is refused, and an `AGENT.md` that pins the old name is reported rather than edited — those files are yours.
- **Delete a model** removes the entry, and refuses while something still needs it, naming the field to move first. Entries that appear only in a fallback list are dropped along with it.
- The list marks which entry is the default and which is the image model, so a rename or delete shows what it will touch before you commit.

Failover is worth configuring by hand in `config.json`, because a free model endpoint can empty out with no warning and every turn then fails with `No endpoints found`:

```jsonc
"agents": { "defaults": {
  "model_name": "primary",
  "model_fallbacks": ["spare"],          // tried when the primary has no endpoint
  "image_model": "vision",
  "image_model_fallbacks": ["vision2"]
} }
```

An agent picks its own model with **`model`** on its `agents.list` entry — a bare alias, or `{ "primary": …, "fallbacks": … }`:

```jsonc
{ "id": "coder", "model": { "primary": "coder-model", "fallbacks": ["primary"] } }
```

`model_name` on a list entry is **not** that field: PicoClaw ignores it and the agent quietly keeps the default. `piccolo doctor` warns when it finds one.

#### After adding Discord

`piccolo setup channels discord` configures PicoClaw, but Discord still requires you to install the bot into a server.

Piccolo stores the non-secret Application ID in `piccolo.yaml`:

```yaml
setup:
  channels:
    discord:
      application_id: "123456789012345678"
```

After setup, Piccolo prints an invite URL like:

```text
https://discord.com/oauth2/authorize?client_id=<APPLICATION_ID>&scope=bot&permissions=68608
```

The permission integer `68608` requests:

- View Channels
- Send Messages
- Read Message History

Also verify Message Content Intent is enabled in the app's Bot settings:

```text
https://discord.com/developers/applications/<APPLICATION_ID>/bot
```

Then apply the config and test:

```sh
piccolo serve       # if the gateway is not running yet
piccolo reload      # if it is already running
piccolo logs -f     # debug if the bot does not respond
```

Test by DMing the bot, or by mentioning it in a server channel when mention-only mode is enabled.

#### File and image interpretation

Generated agents include the PicoClaw skill `file-handling`, which teaches them to use vision for visual descriptions but deterministic tools for exact extraction. For example, when asked to read a QR code, an agent with `exec` runs:

```sh
zbarimg --quiet --raw /tmp/picoclaw_media/<uploaded>.png
```

Build the local runtime image to bake in the file-handling toolchain (installed with the base image's `apk`) and every installed integration:

```sh
piccolo image build --set
piccolo restart
```

The build uses the configured pinned PicoClaw image as its base, generates a temporary/in-memory Docker build context, and tags a local image such as `piccolo/picoclaw-runtime:<hash>`. It does not require a public Piccolo image registry. Omit `--set` to leave `piccolo.yaml` unchanged; Piccolo will print the `container.image` value to adopt manually. `image build` composes the **whole** workspace image — toolchain plus all installed integrations — which is why it is the command the staleness warning points at.

**The toolchain is baked into the image.** Nothing installs when the gateway starts, and Piccolo binds nothing over `/nix` or `/usr`. That is the fix for a failure mode that was silent and total: mounting a host package cache over the image's own store hid it (1464 entries in the image, 68 visible in the running container), so `gws`, `piccolo-gws`, `python3` and `jq` existed on `PATH` and still could not execute — the integration worked when Piccolo configured it and failed when Piccolo ran it.

The cost is disk, and it is measured rather than assumed: the default list is **17 packages providing 23 commands**, and the runtime image goes from a ~440 MB base to about **2.1 GB** (1.6 GB of rootfs in a running container). A cold build takes about a minute. LibreOffice (761 MB) and pandoc (235 MB) dominate; OCR installs the engine plus **English language data only** — the all-languages variant measured 1015 MB against 39 MB, so extra languages are an explicit `tesseract-ocr-data-<lang>` entry rather than a default.

Layer digests are shared across tags, so rebuilding for an integration change does not pay for the toolchain again. An integration upgrade rebuilt in ~40s with the toolchain layer reporting `Using cache`. Edit `runtime.toolchain.packages` if the full office/media set is not needed, and pin a version by naming it (`jq=1.7.1-r0`) — apk has no lockfile, so the content-derived tag identifies the list you requested, not the exact resolved bytes.

**Old images accumulate, and Piccolo never deletes them silently.** Every distinct toolchain/integration/base configuration is a new ~2 GB tag, and the rollback slot (`container.previous_image`) deliberately keeps an older one reachable. Ask Piccolo what is there:

```sh
piccolo image list          # size, age, and why each image was kept
piccolo image list --json
piccolo image prune         # prints what it would remove, then asks
piccolo image prune --yes
```

Prune keeps three things and says so in its own report: the image in use, the recorded rollback target, and any image a container still references **in any state** — a stopped container is a live reference, and deleting its image strands it. Base PicoClaw images are never candidates. The size it quotes is an upper bound, because tags share layers; that is stated rather than hidden.

`piccolo doctor` warns once unreferenced images reach 2 or 10 GB, naming the command — and still removes nothing.

```yaml
runtime:
  toolchain:
    packages: [zbar, file, python3, jq, yq-go, poppler-utils, tesseract, pandoc, libxml2, exiftool, ffmpeg, sqlite, unzip, p7zip, libreoffice, imagemagick]
    commands: [zbarimg, file, python3, jq, yq, pdftotext, pdfinfo, pdftoppm, pdftocairo, tesseract, pandoc, xmllint, exiftool, ffprobe, ffmpeg, sqlite3, unzip, 7z, 7za, libreoffice, soffice, magick, convert, identify]
```

The toolchain installs in its **own layer, before the integration layers**. Docker's cache is prefix-based, so changing an integration reuses the toolchain layer, and editing the package list invalidates exactly the toolchain layer and what follows it. Each configured command is copied into `/usr/local/bin` and symlinked under `/nix/var/nix/gcroots/piccolo-toolchain`, so the layer's own garbage collection cannot delete a tool the image advertises — and a configured command that the package list does not provide **fails the build**, because an image must not claim tools it cannot run. The `nixbld` group stays baked in: it lives in `/etc/group`, which is ephemeral and otherwise resets on container recreation.

Every locally built image records what it contains, in labels: `piccolo.base`, `piccolo.toolchain`, and one `piccolo.integration.<owner>/<repo>="<version>+<digest>"` per installed release. Those labels are what turn "the image is stale" into a reason you can act on.

Use `piccolo runtime status` to inspect configured and live commands, and `piccolo runtime apply` to check whether the running image already contains the configured toolchain — it reports a mismatch and names the rebuild command, and it installs nothing, because a running container is not where the toolchain is defined.

There is deliberately no `runtime prune` or `runtime reset-cache`: garbage-collecting Nix inside a running container reclaims nothing durable (the store is image content, and the writable layer is discarded on recreate), and the cache directories it used to clear are no longer created. Disk reclamation belongs to the image layer — see `docker image prune` and the planned `piccolo image prune`.

For QR codes the package is `zbar`; other common mappings include image normalization with ImageMagick (`magick`, `convert`, `identify`), OCR with `tesseract`, PDF inspection/rendering with Poppler (`pdfinfo`, `pdftotext`, `pdftoppm`, `pdftocairo`), office conversion with LibreOffice (`soffice`/`libreoffice`), structured data parsing with Python/`jq`/`yq`/`xmllint`, archive listing before extraction, EXIF inspection with `exiftool`, and media inspection with `ffprobe`/`ffmpeg`. The skill tells agents to call these directly, avoid OpenCV/`cv2` unless explicitly needed, write intermediates under the workspace, and never install anything at runtime — a tool that is missing was left out of the image, and the fix is `piccolo image build` with the package added (`apt-get` is also the wrong package manager: the base image is Alpine).

Piccolo also configures container-scoped media/debug settings: `agents.defaults.restrict_to_workspace` is **disabled** and `agents.defaults.allow_read_outside_workspace` is enabled (this is a container, not a personal computer), and `agents.defaults.tool_feedback` is enabled with `max_args_length: 300` so tool execution is visible directly in chat. PicoClaw natively allows exec/read access to its upload dir `/tmp/picoclaw_media`, and Piccolo mirrors that media volume into each agent workspace as `media/` for convenience.

The path guard is off because leaving it on makes `exec` unusable rather than safe. PicoClaw's shell guard takes the raw `restrict_to_workspace` flag, not the read access relaxed by `allow_read_outside_workspace` (`pkg/agent/instance.go:85-113`), and it inspects every absolute path in the command line (`pkg/tools/shell.go:1172,1278`) — so with exec on and the guard on, a command as ordinary as reading `/proc` or naming a binary's real path fails with `Command blocked by safety guard (path outside working dir)`, and the agent sees an error string with no remedy in it. Piccolo writes the flag explicitly `false` because PicoClaw defaults it to `true` when the key is absent (`pkg/config/defaults.go:29`); omitting it would silently put the guard back. The container is the security boundary. Turn it on deliberately from `piccolo setup` → **Tools** → **Workspace path guard** if you want per-agent confinement, and accept that shell commands touching other paths will be refused; `piccolo doctor` warns while the guard is on so the reason for those refusals is never a mystery.

For QR decoding from Discord or another remote chat, the workspace must allow both image loading and shell execution from remote channels:

```json
{
  "tools": {
    "load_image": { "enabled": true },
    "exec": { "enabled": true, "allow_remote": true }
  }
}
```

The relevant agent `AGENT.md` frontmatter must also include `load_image`, `exec`, and `skills: [file-handling]`. `piccolo setup tools` → `Sync AGENT.md allowlists and file-handling skill…` syncs enabled tools into agent allowlists and installs or updates the file-handling skill for existing workspaces. Remote exec is broad access: remote chats can request shell commands inside the container subject to PicoClaw safety checks; enable it only for trusted channels/users.

### `piccolo doctor`

Validates the setup and prints one line per finding, **grouped into the five parts of a
setup**, in the order a broken workspace is worth debugging — the layer everything else
rides on comes first:

| section | the question it answers |
|---|---|
| **Environment** | is Docker up, is the image here, is it current, is the container the one `piccolo.yaml` names |
| **Workspace** | `piccolo.yaml`, the directory layout, `.gitignore`, and the record of what Piccolo generated |
| **Credentials** | the secret sidecar, `enc://` values, the passphrase, the key file, file permissions, exec posture |
| **Assistant** | `config.json`, agents, models, image understanding |
| **Channels & integrations** | channels, the web console's chat, installed integrations, MCP reachability |

A section that produced nothing prints **no heading**. Many checks are deliberately silent
(env references, a stale container, MCP with nothing installed, a console with no `pico`
entry), and an empty heading would advertise a problem where the code decided there was none.

On a terminal each line leads with a glyph **and** the word — `✔ ok`, `⚠ warn`, `✖ fail`.
Neither form is colour-only or glyph-only: piped, `NO_COLOR`, and `TERM=dumb` output keeps
the `[OK]` / `[WARN]` / `[FAIL]` markers this command has always printed, so `grep '\[FAIL\]'`
still means what it did. The remedy of a finding is stored separately from the condition, so
it gets its own `→` line instead of being appended to a paragraph, and long lines wrap at the
terminal width with a hanging indent (piped output is never wrapped — one finding per line,
so it stays copyable and diffable).

```text
Environment
✔ ok   docker daemon reachable
✔ ok   image present: docker.io/sipeed/picoclaw:v0.3.1
⚠ warn runtime image retention: 2 unreferenced runtime image(s), up to 9.8GB
       → reclaim with 'piccolo image prune' (keeps the image in use and the rollback target);
         Piccolo never deletes images on its own

Workspace
✔ ok   manifest: piccolo.yaml
✔ ok   workspace layout: structured
⚠ warn generated file predates the current template: picoclaw/skills/file-handling/SKILL.md
       → refresh with 'piccolo setup' (fix belongs in internal/generate/templates.go:
         FileHandlingSkillMD, not in the copy)

Credentials
✔ ok   secret sidecar: picoclaw/.security.yml
       → secure fields here override config.json
⚠ warn piccolo.yaml is readable by group/others (644) and holds the enc:// passphrase
       → chmod 600 piccolo.yaml
⚠ warn picoclaw/.security.yml apodex-1.1-mini:0 matches no model_list entry
       → a model renamed or deleted in config.json leaves its key behind here

Assistant
✔ ok   picoclaw config: config.json (schema v3)
✔ ok   agent: main (default)
✖ fail agent "research": missing agent file: …/picoclaw/agents/research/AGENT.md
⚠ warn model "gpt-5.4" has a placeholder or empty API key

Channels & integrations
✔ ok   mcp: 1 server(s) reachable by coordinator "main" (google_workspace)
⚠ warn web console chat: channel "pico" is disabled, so the gateway mounts no /pico/ handler
       → the dashboard opens and logs in but cannot chat; enable it ('piccolo setup' → Channels),
         or restart the console, whose launcher re-enables it and rewrites config.json

⚠ warn doctor: 1 blocking, 5 to look at, 10 ok
```

The last line is the whole verdict, and it is the only line `piccolo doctor` exists to
answer.

- `--compact` prints findings only. A passing section still gets its heading and a line
  saying how many checks it held, because a report that dropped headings would hide which
  checks ran at all.
- `--json` emits the same grouping structurally (`sections[].name`, `results[].status /
message / hint`) plus a `summary`, so a script never has to re-derive it from prose.

**A Docker daemon that will not answer is one finding, not a reason to stop.** Earlier
behaviour printed `docker daemon not reachable` and nothing else, which hid the half of the
answer you can act on while Docker is starting: whether the workspace itself is intact.
Now every file and config check still runs, the Docker-dependent checks stay silent (the
daemon was never asked, so an absence is not evidence), and the exit code is still non-zero.

Exit code is non-zero only on `✖ fail` / `[FAIL]`; warnings never fail.

**A disabled channel is never a warning.** PicoClaw does not construct a channel, mount its HTTP handler, or open any connection while its entry is off, so "off" is the absence of a configuration rather than a broken one. Doctor only distinguishes *why* an entry is off: one that still holds a credential (in `config.json`, in `picoclaw/.security.yml`, or both) was configured and switched off, and gets its own `[OK]` line naming where the credential is parked; empty ones are almost certainly PicoClaw's own defaults, which it serializes for every channel type it knows whenever anything saves through the gateway. Those collapse into one line, because deleting them is undone by the next save and a per-channel line would imply each one is a thing to look at.

The console's chat is the exception worth a warning: the dashboard proxies its websocket to the gateway's `pico` channel, so `piccolo.yaml` can look perfectly configured (`:18800` published, launcher image, gateway healthy) while the chat page connects to nothing. Doctor checks that channel's state, and stays quiet when no `pico` entry exists at all — the launcher creates and enables its own on first startup.

### `piccolo serve` / `piccolo restart`

Starts the gateway container **in the background**: pulls the image if missing, auto-publishes the gateway health port on `127.0.0.1:18790` (unless your `piccolo.yaml` already maps it), and injects `PICOCLAW_GATEWAY_HOST=0.0.0.0` inside the container so the published port can reach it.

When the container that exists runs a **different image** than `container.image`, Piccolo promotes instead of replacing:

1. stop the current gateway and rename it to `<name>.prev` — the canonical name is freed, but the known-good container survives as a revert target
2. start the target image under the canonical name
3. gate it: `/health` must answer **and** every installed integration binary must actually execute inside the container (`docker exec`, not `command -v`)
4. pass → remove `<name>.prev`, record the replaced image for `piccolo integration rollback`
5. fail → capture the gate output and the candidate's logs, remove the candidate, restart the previous gateway, exit non-zero

The gate executes because health alone is not readiness: a gateway whose integration binaries cannot run answers `200 OK` happily, and `command -v piccolo-gws` succeeded in exactly such a container. Promoting with a revert target is what turns "new image is broken" from an outage into a warning.

A promotion that was interrupted (crash, Ctrl-C mid-restart) leaves `<name>.prev` with nothing on the canonical name; the next `serve` detects it, restores it, and says so. `doctor` and `status` report the same condition.

`piccolo restart` is now just "serve with the current configuration" — it no longer stops and removes the gateway first, because that destroyed the only thing a failed promotion could fall back to.

Use `--foreground` to attach stdio instead (Ctrl-C stops the container); it promotes with the same park/gate/revert sequence while attached.

### `piccolo integration rollback`

Puts the workspace back on the image it ran before the last successful promotion — for the case where the new gateway is healthy but wrong:

```sh
piccolo integration rollback --dry-run   # show the target and what it contains
piccolo integration rollback             # confirm, then promote through the same gate
piccolo integration rollback --to <image>
```

It refuses before stopping anything if the target image is no longer present locally, and it routes through the same gated promotion, so a rollback that does not work restores the current gateway instead of leaving nothing running. `piccolo.yaml` keeps one rollback slot (`container.previous_image`).

Rollback restores **service**, not configuration: the workspace still records the integration versions it has now, so the image-content warning will legitimately keep reporting a difference until you make the choice durable with `piccolo integration add <owner/repo>@<selector>` or `piccolo image build --set`.

### `piccolo chat`

Terminal chat — wraps PicoClaw's own interactive client (`picoclaw agent`) via `docker exec` and passes its output through unmodified (upstream banner, prompt, and client log lines all show as-is; see `piccolo logs` for gateway logs):

```sh
piccolo chat              # interactive REPL (history, exit/quit)
piccolo chat -m "hello"   # one-shot question
```

For the optional **web console**, init with `--dashboard` (launcher image) and open `http://127.0.0.1:18800` after `serve` — on first run it redirects to `/launcher-setup` where you create the dashboard password. The gateway runs regardless; `--dashboard` only decides whether the console is published.

### `piccolo status` / `piccolo logs` / `piccolo stop`

`status` shows container state, the image it is running, a live `/health` probe, the rollback target when one is recorded, and a parked `<name>.prev` container when a promotion was interrupted. `logs [-f] [--tail N]` streams gateway logs. `stop` gracefully stops and removes the container.

All commands resolve the workspace the same way: `-D/--dir` flag, else the current directory. Commands fail with an actionable message when `piccolo.yaml` is missing.

## What Piccolo does NOT do (PoC scope)

- No custom memory store, tasks/reminders/scheduler, MCP servers, or native PicoClaw tools
- No PicoClaw binary bundling/downloading — the container image is the runtime
- No custom TUI — `piccolo chat` reuses PicoClaw's own terminal client
- No `add-agent` command — edit `config.json` + create the workspace dir by hand; `doctor` catches mistakes
- No `integration remove` command — uninstalling means deleting the entry from `runtime.integrations` in `piccolo.yaml` and rebuilding (`piccolo image build --set && piccolo restart`). Integration *state* under `integrations/<owner>/<repo>` is yours to keep or delete
- No private integration repositories — the release fetcher sends no credentials, so `piccolo integration add` of a private `owner/repo` fails with 404 rather than a permission error. Publish integration releases publicly for now
- No full channel automation — setup can guide credentials and links, but provider-side app/server installation is still done in Discord/Telegram/Slack

## Acknowledgements

- [PicoClaw](https://github.com/sipeed/picoclaw) — the agent runtime (v0.3.1, MIT)
- [piccolo-full](https://github.com/kimus/piccolo) — first PoC; source of the Docker runner pattern
