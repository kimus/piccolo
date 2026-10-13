# Piccolo

Piccolo is a thin, opinionated setup and launcher for [PicoClaw](https://github.com/sipeed/picoclaw) — create, validate, and run a multi-agent AI assistant with three commands. PicoClaw runs inside a Docker container managed by Piccolo; you never install PicoClaw by hand.

> **Status:** PoC. See [AGENTS.md](AGENTS.md) for implementation guidance.

## How it works

```text
piccolo init      interactive wizard → creates the workspace in the current directory
piccolo setup     arrow-navigable editor for an existing workspace
                  (channels like Discord, models, tool toggles — merge-patched,
                  with provider setup steps, diff preview, and config.json.bak)
piccolo integration add gws
                  build/select a gateway image containing a self-describing
                  integration package (does not patch config.json itself)
piccolo integration setup gws
                  run the integration-owned setup command in a temporary
                  container from the configured image
piccolo doctor    validates Docker, workspace, and generated files
piccolo serve     starts the PicoClaw gateway container in the background,
                  waits until http://127.0.0.1:18790/health answers
piccolo restart   stop + serve in one step (apply config or piccolo.yaml changes)
piccolo reload    apply config.json changes without recreating the container
piccolo chat      talk to the assistant in the terminal (PicoClaw's own REPL)
piccolo status    container + health state
piccolo logs      gateway logs (-f to follow)
piccolo stop      stop and remove the container
```

```text
┌────────────────────────────┐
│ piccolo                    │
│ setup + container launcher │
└────────────┬───────────────┘
             │ writes once (init), then user-owned
             ▼
┌─────────────────────────────────────────┐
│ workspace dir (default: current dir;    │
│   mounted as picoclaw home in container)│
│   piccolo.yaml       (piccolo-owned)    │
│   config.json        (picoclaw config)  │
│   agents/*/AGENT.md ...                 │
└────────────┬────────────────────────────┘
             │ bind-mounted → /root/.picoclaw
             ▼
┌────────────────────────────┐
│ Docker container           │
│ docker.io/sipeed/picoclaw  │
│ entrypoint → picoclaw      │
│              gateway       │
└────────────────────────────┘
```

## Requirements

- Go 1.25+ (to build)
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

`piccolo init` writes everything **directly into the workspace directory** (default: current directory; choose another with the wizard or `-D/--dir`):

```text
<workspace>/
  piccolo.yaml     # piccolo launch config (YAML, piccolo-owned)
  config.json      # PicoClaw config (JSON schema v3, generated once, then yours)
  .gitignore       # created/appended by init (ignores config.json — it holds API keys)
  agents/
    main/          # coordinator agent (always)
      AGENT.md  SOUL.md  USER.md  memory/MEMORY.md
    research/      # optional specialists
    planner/
    coder/
    ops/
```

The whole directory is bind-mounted to `/root/.picoclaw` in the container (PicoClaw's default home — `PICOCLAW_HOME` is never set). PicoClaw writes back into it at runtime (`auth.json`, memory updates, session/channel state), so everything persists across `serve` restarts.

## Configuration ownership

There is **no duplicated configuration** between the two files:

| File | Owner | Contents |
|---|---|---|
| `piccolo.yaml` | Piccolo | Container launch config plus non-secret setup metadata |
| `config.json` | You (after init) | PicoClaw runtime behavior: agents, models, tools, channels, tokens |

`config.json` and the agent files are generated **once** by `piccolo init` and are yours afterwards — edit them directly to change models, add agents, or tune tools. Piccolo never silently regenerates or overwrites them.

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
setup:                                      # optional non-secret setup metadata
  channels:
    discord:
      application_id: "123456789012345678"
```

## Commands

### `piccolo init`

Interactive wizard: assistant name, owner, purpose template, specialist agents (research/planner/coder/ops), whether to expose the optional web console, model provider + API key, tool toggles (web search default on, shell exec default off), workspace path.

The PicoClaw gateway always runs; the **web console** is an optional add-on. It is recorded purely by the `:18800` binding in `piccolo.yaml` — to enable it later, set `container.image` to the `:launcher` tag and add `"127.0.0.1:18800:18800"` to `container.ports`; to disable it, use the pinned gateway image and remove that binding. `piccolo doctor` warns if a `:18800` binding is paired with a non-launcher image.

- Create-only: existing files are never overwritten without explicit per-file confirmation.
- Non-interactive flags (`--name`, `--owner`, `--purpose`, `--yes`, ...) are supported for scripting.
- Ensures `config.json` is git-ignored.


### `piccolo integration`

Containerized integrations are self-describing packages baked into the gateway image before PicoClaw starts. For example:

```sh
piccolo integration add gws
piccolo integration setup gws
piccolo restart
```

`add` resolves aliases such as `gws` to an integration package reference, asks before building third-party code into the runtime image, runs the installed binary's `describe --json` in a container, and records Piccolo-owned integration metadata in `piccolo.yaml`. It does **not** patch provider-specific `config.json` entries.

`setup` runs the integration-owned setup command in a one-shot container from the configured image with the workspace mounted at `/root/.picoclaw`. The integration owns provider prompts, auth, state files, and any `config.json` merge-patching. After setup, run `piccolo restart`.

Integration authors provide an executable whose descriptor command returns JSON like:

```json
{
  "schema": "piccolo.integration.v1",
  "name": "gws",
  "displayName": "Google Workspace",
  "commands": ["piccolo-gws"],
  "setup": {
    "command": ["piccolo-gws", "setup", "--config-dir", "/root/.picoclaw/integrations/google"],
    "requiresTty": true,
    "ports": ["127.0.0.1:8000:8000"]
  }
}
```

Only use integrations you trust; integration refs install code into the assistant runtime image.

### `piccolo setup`

The editing counterpart of `init` for an existing workspace. `piccolo setup` opens an arrow-navigable home menu (↑/↓ move, enter open, q quit); deep-links work too: `piccolo setup channels`, `piccolo setup channels discord`, `piccolo setup models`, `piccolo setup tools`.

- **v1-editable**: channels (Discord/Telegram/Slack — list, add, toggle, edit with pre-filled values), `model_list` entries, web/exec/spawn/subagent/load_image toggles, and an advanced remote-exec toggle. Agents/container/identity sections are listed but require manual edits for now.
- Discord setup asks whether you already have an application, helps you create one when needed, requires the Application ID + bot token, validates the token with Discord when possible, and prints direct install/test links afterwards.
- **Merge-patched writes**: unknown keys and untouched subtrees of your user-owned `config.json` are preserved; every change shows a diff preview, requires confirmation, and backs the previous file up to `config.json.bak`.
- Bot tokens are stored plaintext in `config.json` (PicoClaw has no env expansion — verified). Non-secret setup metadata such as Discord Application ID is stored in `piccolo.yaml` so setup can regenerate links later.
- If the gateway container is running afterwards, setup reminds you to `piccolo reload` (config.json) or `piccolo restart` (piccolo.yaml).

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

Build the optional local runtime image to provide Devbox/Nix and common file-handling tools:

```sh
piccolo image build --set
piccolo restart
```

The build uses the configured pinned PicoClaw image as its base, generates a temporary/in-memory Docker build context, and tags a local image such as `piccolo/picoclaw-runtime:<hash>`. It does not require a public Piccolo image registry. Omit `--set` to leave `piccolo.yaml` unchanged; Piccolo will print the `container.image` value to adopt manually.

The image is intentionally **lean**: it installs only the Devbox binary, the `nixbld` build group/users Nix needs, and a `piccolo-setup-tools` script. The tools themselves are **not** baked into the image. On the first `piccolo serve`/`restart` after the gateway is healthy, Piccolo applies the `runtime.toolchain` section from `piccolo.yaml`; installed packages and the Nix store persist in two named volumes (`piccolo-devbox-global` and `piccolo-devbox-nix-cache`), so later restarts are near-instant.

```yaml
runtime:
  toolchain:
    packages: [zbar, file, python3, jq, yq-go, poppler-utils, tesseract, pandoc, libxml2, exiftool, ffmpeg, sqlite, unzip, p7zip, libreoffice, imagemagick]
    commands: [zbarimg, file, python3, jq, yq, pdftotext, pdfinfo, pdftoppm, pdftocairo, tesseract, pandoc, xmllint, exiftool, ffprobe, ffmpeg, sqlite3, unzip, 7z, 7za, libreoffice, soffice, magick, convert, identify]
```

`piccolo-setup-tools` writes the configured package list to Devbox, runs `devbox install`, then resolves each configured command's real `/nix/store` binary via `devbox shellenv` and symlinks it into `/usr/local/bin`. The tools thus become ordinary PATH commands that work from **any** working directory with relative or absolute args, with no per-call Devbox overhead. This routes around two Devbox limitations verified in this daemon-less Nix container: `devbox global install` links only a subset of binaries into the profile `bin`, and `devbox run` always cds to the project root (breaking relative paths). The `nixbld` group is baked into the image because it lives in `/etc/group`, which is ephemeral and otherwise resets on container recreation (breaking Nix after a restart). A future Piccolo-maintained public image can replace this local image by editing `piccolo.yaml`.

Use `piccolo runtime status` to inspect configured/live commands, `piccolo runtime apply` to reconcile a running container after editing `runtime.toolchain`, `piccolo runtime prune` to attempt Nix garbage collection, and `piccolo runtime reset-cache --yes` to remove the persisted Devbox/Nix volumes for a clean reinstall.

For QR codes the Devbox package is `zbar`; other common mappings include image normalization with ImageMagick (`magick`, `convert`, `identify`), OCR with `tesseract`, PDF inspection/rendering with Poppler (`pdfinfo`, `pdftotext`, `pdftoppm`, `pdftocairo`), office conversion with LibreOffice (`soffice`/`libreoffice`), structured data parsing with Python/`jq`/`yq`/`xmllint`, archive listing before extraction, EXIF inspection with `exiftool`, and media inspection with `ffprobe`/`ffmpeg`. The skill tells agents to call these directly, avoid OpenCV/`cv2` unless explicitly needed, write intermediates under the workspace, and never use `apt-get` (the base image is Alpine).

Piccolo also configures container-scoped media/debug settings: `agents.defaults.allow_read_outside_workspace` is enabled (this is a container, not a personal computer), and `agents.defaults.tool_feedback` is enabled with `max_args_length: 300` so tool execution is visible directly in chat. PicoClaw natively allows exec/read access to its upload dir `/tmp/picoclaw_media`, and Piccolo mirrors that media volume into each agent workspace as `media/` for convenience.

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

Validates the setup and prints `[OK]` / `[WARN]` / `[FAIL]` lines:

```text
[OK]   docker daemon reachable
[OK]   image present: docker.io/sipeed/picoclaw:v0.3.1
[OK]   workspace: .
[OK]   manifest: piccolo.yaml
[OK]   picoclaw config: config.json (schema v3)
[OK]   agent: main (default)
[WARN] model "gpt-5.4" has placeholder API key
[FAIL] missing agent file: agents/research/AGENT.md
```

Exit code is non-zero only on `[FAIL]`; warnings never fail.

### `piccolo serve`

Starts the gateway container **in the background**: pulls the image if missing, replaces a stale container of the same name (crash-safe restart), auto-publishes the gateway health port on `127.0.0.1:18790` (unless your `piccolo.yaml` already maps it), and injects `PICOCLAW_GATEWAY_HOST=0.0.0.0` inside the container so the published port can reach it. Then it polls `http://127.0.0.1:18790/health` until the gateway answers — on failure it prints the error plus recent container logs and exits non-zero, leaving the container in place for debugging (`piccolo logs`, `piccolo stop`).

Use `--foreground` to attach stdio instead (Ctrl-C stops the container).

### `piccolo chat`

Terminal chat — wraps PicoClaw's own interactive client (`picoclaw agent`) via `docker exec` and passes its output through unmodified (upstream banner, prompt, and client log lines all show as-is; see `piccolo logs` for gateway logs):

```sh
piccolo chat              # interactive REPL (history, exit/quit)
piccolo chat -m "hello"   # one-shot question
```

For the optional **web console**, init with `--dashboard` (launcher image) and open `http://127.0.0.1:18800` after `serve` — on first run it redirects to `/launcher-setup` where you create the dashboard password. The gateway runs regardless; `--dashboard` only decides whether the console is published.

### `piccolo status` / `piccolo logs` / `piccolo stop`

`status` shows container state + a live `/health` probe. `logs [-f] [--tail N]` streams gateway logs. `stop` gracefully stops and removes the container.

All commands resolve the workspace the same way: `-D/--dir` flag, else the current directory. Commands fail with an actionable message when `piccolo.yaml` is missing.

## What Piccolo does NOT do (PoC scope)

- No custom memory store, tasks/reminders/scheduler, MCP servers, or native PicoClaw tools
- No PicoClaw binary bundling/downloading — the container image is the runtime
- No custom TUI — `piccolo chat` reuses PicoClaw's own terminal client
- No `add-agent` command — edit `config.json` + create the workspace dir by hand; `doctor` catches mistakes
- No full channel automation — setup can guide credentials and links, but provider-side app/server installation is still done in Discord/Telegram/Slack

## Acknowledgements

- [PicoClaw](https://github.com/sipeed/picoclaw) — the agent runtime (v0.3.1, MIT)
- [piccolo-full](https://github.com/kimus/piccolo) — first PoC; source of the Docker runner pattern
