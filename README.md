# Piccolo

Piccolo is a thin, opinionated setup and launcher for [PicoClaw](https://github.com/sipeed/picoclaw) — create, validate, and run a multi-agent AI assistant with three commands. PicoClaw runs inside a Docker container managed by Piccolo; you never install PicoClaw by hand.

> **Status:** PoC. See [AGENTS.md](AGENTS.md) for implementation guidance.

## How it works

```text
piccolo init      interactive wizard → creates the workspace in the current directory
piccolo doctor    validates Docker, workspace, and generated files
piccolo serve     runs the PicoClaw gateway container (foreground, stdio attached)
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
piccolo init        # wizard: assistant name, agents, model, tools, terminal or web console

# validate
piccolo doctor

# run (foreground; Ctrl-C stops the container)
piccolo serve
```

With the **web console** interaction mode (launcher image), open `http://127.0.0.1:18800` after `serve` starts.

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
| `piccolo.yaml` | Piccolo | Launch only: container image, name, network, ports, env passthrough |
| `config.json` | You (after init) | PicoClaw behavior: agents, models, tools, channels |

`config.json` and the agent files are generated **once** by `piccolo init` and are yours afterwards — edit them directly to change models, add agents, or tune tools. Piccolo never silently regenerates or overwrites them.

### `piccolo.yaml`

```yaml
version: 1
container:
  image: docker.io/sipeed/picoclaw:v0.3.1  # pinned tag; :launcher for the web console
  name: piccolo-gateway                     # deterministic container name
  network: bridge                           # none | bridge | host
  ports: []                                 # launcher flavor: ["127.0.0.1:18800:18800"]
  env:                                      # "env:VAR" reads the host env at serve time
    {}
```

## Commands

### `piccolo init`

Interactive wizard: assistant name, owner, purpose template, specialist agents (research/planner/coder/ops), interaction mode (terminal or web console), model provider + API key, tool toggles (web search default on, shell exec default off), workspace path.

- Create-only: existing files are never overwritten without explicit per-file confirmation.
- Non-interactive flags (`--name`, `--owner`, `--purpose`, `--yes`, ...) are supported for scripting.
- Ensures `config.json` is git-ignored.

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

Runs the gateway container in the foreground: pulls the image if missing, replaces a stale container of the same name (crash-safe restart), attaches stdio, forwards SIGINT/SIGTERM to a graceful stop, removes the container on exit, and exits with the container's exit code.

All commands resolve the workspace the same way: `-D/--dir` flag, else the current directory. `serve`/`doctor` fail with an actionable message when `piccolo.yaml` is missing.

## What Piccolo does NOT do (PoC scope)

- No custom memory store, tasks/reminders/scheduler, MCP servers, or native PicoClaw tools
- No PicoClaw binary bundling/downloading — the container image is the runtime
- No detached mode yet (`serve -d`, `logs`, `stop` are fast-follow candidates)
- No `add-agent` command — edit `config.json` + create the workspace dir by hand; `doctor` catches mistakes
- No channel credential wizards (Telegram, Matrix, ...)

## Acknowledgements

- [PicoClaw](https://github.com/sipeed/picoclaw) — the agent runtime (v0.3.1, MIT)
- [piccolo-full](https://github.com/kimus/piccolo) — first PoC; source of the Docker runner pattern
