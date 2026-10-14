## Context

`piccolo init` currently frames interaction as a mutually exclusive `Mode`: `terminal` or `web` (`internal/generate`). The mode drives two things at generation time (`Inputs.Manifest()`):

- `terminal` → `DefaultImage` (plain gateway image), no published ports.
- `web` → `LauncherImage`, `container.ports = ["127.0.0.1:18800:18800"]`, and env `PICOCLAW_GATEWAY_HOST=0.0.0.0`.

At serve time (`cmd/piccolo/serve.go`) piccolo already:

- auto-publishes `127.0.0.1:<gwPort>:<gwPort>` for the gateway/health port unless already mapped,
- injects `PICOCLAW_GATEWAY_HOST=0.0.0.0` when ports are published and network != host,
- detects "web mode" purely by scanning `container.ports` for a `:18800` binding (`webConsoleURL`).

Verified upstream facts (pin `v0.3.1`):

- The launcher image ships **both** `picoclaw` and `picoclaw-launcher` binaries; `piccolo chat` (docker exec of `picoclaw agent`) therefore works on the launcher image too.
- The launcher process (`picoclaw-launcher`) **is** the web server on `:18800` and always tries to manage a gateway child (`TryAutoStartGateway` at `main.go:729`). There is no "web off" switch inside a running launcher and no "connect to external gateway" flag — `launcherconfig` has no `enabled` field.
- The plain gateway image serves only the gateway on `:18790` (default), nothing on `:18800`.

Constraint from AGENTS.md: single gateway container, no per-service containers, no custom runtime. The web console cannot be a separate sidecar without fighting the launcher's self-managed-gateway design.

## Goals / Non-Goals

**Goals:**
- Reframe the setup choice so the gateway is always required and the web console is an optional add-on.
- Keep the `:18800` binding as the single source of truth for "console enabled" (no drift-prone boolean).
- Guarantee no dead published `:18800` port and no dashboard process when the console is disabled.
- Avoid the `--web` flag ambiguity: rename it to `--web-search` so it reads distinctly from the new `--dashboard` flag.
- Preserve backward compatibility: existing manifests keep their meaning.

**Non-Goals:**
- No separate `piccolo web` command.
- No two-container / sidecar topology.
- No serve-time image swapping or `serve --web/--no-web` override (the choice lives in `piccolo.yaml`; changing it later is a manifest edit).
- No migration/regeneration of existing `config.json` or `piccolo.yaml`.

## Decisions

### Decision 1: Two images, selected by the console toggle (keep current mechanism)

Web **on** → `LauncherImage` + `:18800` binding. Web **off** → `DefaultImage` (plain gateway), no `:18800`.

This is the mechanism already implemented; the change reframes it from "interaction mode" to "optional console". Rationale: the plain gateway image is leaner for terminal-only users and, crucially, runs **no** dashboard process, so there is genuinely nothing on `:18800` — satisfying "no dead port" by construction rather than by leaving an unreachable listener.

**Alternative considered — one launcher image always, override command to `picoclaw gateway` when console off.** Rejected: it still ships the dashboard binary, requires an entrypoint override, and offers no benefit over selecting the lean image. It also complicates the "web off runs no dashboard" guarantee.

**Alternative considered — two containers (gateway + web sidecar).** Rejected earlier in exploration: the launcher always manages its own gateway child and cannot attach cross-container (PID namespaces differ); two gateways would race over the shared home's `.picoclaw.pid` and channel state.

### Decision 2: `:18800` binding is the single source of truth

Do not add a `setup.web` / `console: true` boolean to `piccolo.yaml`. `webConsoleURL` already derives console-enabled from the binding; generation writes the binding iff the console is enabled. Keeping one representation avoids a boolean that can disagree with the ports. The generation-time input becomes a plain `Dashboard bool` (replacing the `Mode` enum), meaning "web console on/off" — not an interaction paradigm.

### Decision 3: Replace `--mode` with a dedicated `--dashboard` bool

Backward compatibility is not required, so drop the `Mode` enum and the `--mode terminal|web` flag entirely. `init` gains `--dashboard` (bool, default false) and the wizard replaces the terminal/web Select with a Confirm ("Expose the optional web console?").

In the same pass, rename the existing `--web` flag to `--web-search`. Rationale: with a `--dashboard` flag present, a bare `--web` is ambiguous (web console? web search?). The two axes are unrelated — `--web-search` maps to `config.json` `tools.web.enabled` (an agent capability), `--dashboard` maps to the `:18800` binding in `piccolo.yaml` (a host-exposed service) — so explicit names keep them from being conflated. The Go field `WebSearch` and the wizard "Enable web search tools?" prompt are already correctly named; only the flag string changes.

Rationale for a bool over the enum: it reads as the add-on toggle it is, and removing the enum eliminates the misleading "paradigm" framing at the source.

**Alternative considered — keep `--mode` for compatibility.** Rejected: user confirmed compatibility is unnecessary, and carrying a two-valued enum for a binary on/off choice invites the old either/or mental model.

### Decision 4: Serve/status/doctor messaging

- `serve`: unchanged port/env composition. Print the web URL + `/launcher-setup` hint only when `webConsoleURL != ""` (already the case). No behavioral change needed beyond wording.
- `status`: print the console URL only when enabled (derive from the same `:18800` scan).
- `doctor`: no new failure for a disabled console. Add a consistency check: a `:18800` binding present while `container.image` is not the launcher image is a `[WARN]` (published port with no dashboard behind it).

## Risks / Trade-offs

- **[Terminal-only users cannot enable the console without editing `piccolo.yaml`]** → Accepted; documented. The edit is small (swap image to launcher, add the `:18800` binding) and `doctor` validates the result. A future `piccolo web enable` helper is out of scope.
- **[`--mode web` still reads like an either/or paradigm]** → Mitigate via wizard/help rewording; keep the flag value for compatibility.
- **[Someone hand-writes a `:18800` binding with the plain gateway image]** → doctor `[WARN]` for the image/binding mismatch; serve would publish a dead port, so the warning is the guardrail.
- **[Launcher always manages its own gateway child — is that "the gateway is required"?]** → Yes: in both images a gateway runs and is health-gated on `:18790`. The console toggle only changes which image and whether `:18800` is published.

## Migration Plan

No data migration. Existing workspaces behave identically: a `:18800` binding still means console-on (launcher image), its absence still means console-off (gateway image). The breaking change is CLI-surface only — `--mode terminal|web` is removed in favor of `--dashboard`, `--web` is renamed to `--web-search`, and the internal `Mode` enum is replaced by a `Dashboard bool`. Callers scripting `init --mode web` switch to `init --dashboard`; callers using `init --web` switch to `init --web-search`. Rollback = revert the change; no persisted state to unwind.

## Open Questions

- Should `doctor`'s image/binding mismatch be `[WARN]` or `[FAIL]`? Leaning `[WARN]` (serve still runs; it's a config smell, not a hard breakage).
- Do we want a convenience `piccolo web` subcommand later (out of scope here) or keep everything in `piccolo.yaml`?
