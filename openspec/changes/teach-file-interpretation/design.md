## Context

> **Partially superseded by `bake-runtime-toolchain-into-image`.** "Missing file-handling tools are
> installed on demand" is false: the toolchain is image content and no installer exists in the
> container. The requirement was rewritten to "reported, not installed", which is also what the
> generated skill text now says. Everything else here still stands.



Piccolo generates PicoClaw workspaces once and then treats `config.json` and agent files as user-owned. Generated agents currently include role guidance and tool allowlists, and Piccolo already supports image understanding through `load_image` plus `agents.defaults.image_model`. However, image understanding is not the same as exact decoding: a vision model can recognize a QR code but should not be expected to extract its payload reliably.

The desired behavior is instructional rather than architectural. The agent should learn a reusable PicoClaw skill for common files and images: use vision for visual understanding, but use deterministic tools through `exec` when exact decoding, text extraction, metadata, or structured parsing is required. In Piccolo's Docker runtime, missing Debian/Ubuntu tools may be installed on demand when exec is available; the install is allowed to be ephemeral because the container environment is disposable.

Remote channels such as Discord add a security boundary. PicoClaw gates exec from non-internal channels with `tools.exec.allow_remote`. Supporting any exec-backed workflow from Discord therefore means intentionally enabling shell execution inside the container for trusted remote channels/users.

## Goals / Non-Goals

**Goals:**

- Teach generated agents a common file-interpretation strategy through a generated PicoClaw `file-handling` skill.
- Make QR-code decoding work as an explicit deterministic workflow using `zbarimg` rather than visual guessing.
- Include a practical table of common file types and preferred tools/commands.
- Allow on-demand package installation inside the disposable container when exec is available.
- Preserve Piccolo's split: `piccolo.yaml` remains Piccolo-owned launch config; `config.json` remains PicoClaw runtime config and user-owned after init.
- Make remote exec risk explicit anywhere Piccolo enables, documents, or diagnoses `tools.exec.allow_remote`.

**Non-Goals:**

- No native Piccolo QR decoder, file parser, OCR engine, or MCP server.
- No custom PicoClaw tool registration.
- No Piccolo-maintained derivative PicoClaw image with file utilities preinstalled.
- No attempt to sandbox exec to only QR/file commands beyond PicoClaw's existing exec safety mechanisms.
- No automatic rewriting of existing user-owned agent files outside explicit setup/sync flows.

## Decisions

### Decision 1: Teach through a PicoClaw skill, not a new runtime tool

Generate `skills/file-handling/SKILL.md` and add `skills: [file-handling]` to generated agent frontmatter. Keep `AGENT.md` concise, with only a short reminder that exact file extraction should use the active file-handling skill.

Rationale: skills are PicoClaw's native reusable instruction package. Using a skill avoids inventing a parallel Piccolo-only "playbook" convention while still staying within Piccolo's boundary: Piccolo sets up PicoClaw assets; it does not implement custom runtime logic or tools.

Alternative considered — implement a dedicated QR/file decoder tool. Rejected because it violates the project's non-goal of custom Piccolo/PicoClaw runtime tools and would require ongoing parser/tool maintenance.

### Decision 2: Treat routine file-tool installation as on-demand and ephemeral

Instructions tell agents that, if a deterministic file-handling command is missing and exec is available, they may install the relevant routine decoder/parser/extractor package with the system package manager, then retry the operation without asking first. Agents ask only if exec is unavailable, installation fails, the package is unusually invasive/heavy, the operation could alter user data, or the request is ambiguous.

Rationale: the user accepts Docker-container ephemerality and prefers teaching over image maintenance. This avoids making Piccolo responsible for custom images while still enabling practical decoding.

Alternative considered — bake tools into a custom image. Rejected for now because it adds image publishing, pinning, scanning, multi-arch, and dashboard/gateway variant responsibilities.

### Decision 3: QR decoding gets an explicit high-priority rule

The `file-handling` skill should include a precise QR/barcode rule:

1. Use the uploaded image's actual file path.
2. Run `zbarimg --quiet --raw <image-path>` using exec.
3. If `zbarimg` is missing, install `zbar-tools` with apt and retry.
4. Return the decoded text exactly.
5. If decoding fails, report the failure.

Rationale: QR is the motivating failure mode, and without explicit guidance the model may stop after visual recognition.

### Decision 4: Model `tools.exec.allow_remote` where Piccolo edits tool config

Piccolo's typed config and setup patching should preserve `tools.exec.allow_remote` and, if a setup flow exposes remote exec, set it explicitly. Generated default config should remain conservative unless the existing shell/remote-exec choice is intentionally changed by the implementation plan.

Rationale: remote QR decoding from Discord requires this field. The behavior should not depend on upstream defaults, especially where source and docs have disagreed historically.

### Decision 5: Keep warnings honest about broad shell access

Any UI/docs/doctor language around remote exec must state that `allow_remote` enables shell commands inside the container from remote channels, subject to PicoClaw's exec safety checks, and should only be enabled for trusted channels/users.

Rationale: this is the main risk. Users may still choose it, especially in disposable Docker environments, but the product should not obscure the capability boundary. Generic diagnostics should not mention QR codes unless they are specifically diagnosing file-handling behavior.

## Risks / Trade-offs

- [Risk] Agents may install packages unnecessarily or fail if apt/network is unavailable → Mitigation: instructions limit this to routine file-handling utilities, install only when the command is missing, retry once, and report installation failure clearly.
- [Risk] Remote exec grants broad command execution from Discord/Telegram/etc. → Mitigation: explicit generic warning in setup/docs/doctor and trust-focused wording.
- [Risk] Large instruction blocks can clutter generated `AGENT.md` files → Mitigation: keep the long guidance in `skills/file-handling/SKILL.md` and only reference the skill from `AGENT.md`.
- [Risk] Existing workspaces are user-owned and will not receive the new skill automatically → Mitigation: provide an explicit setup/sync path or documentation for installing/updating the skill and adding `skills: [file-handling]`.
- [Risk] Tool availability varies across images → Mitigation: use Debian/Ubuntu package names and include unknown-file fallback (`file <path>`) plus failure reporting.

## Migration Plan

No automatic migration is required. New workspaces receive `skills/file-handling/SKILL.md` and generated agents reference it with `skills: [file-handling]`. Existing workspaces can opt in through documented manual edits or the explicit setup/tools sync operation.

Rollback is simple: remove `skills: [file-handling]` from `AGENT.md`, remove the skill file if desired, and disable exec/remote exec in `config.json`.

## Open Questions

- Should generated `config.json` continue to emit only `tools.exec.enabled`, or should it also emit `allow_remote: false` explicitly for clarity?
- Should `piccolo setup tools` expose a separate "Allow exec from remote chats" toggle, or should remote exec remain a manual advanced edit documented for Discord QR use cases?
- Should `doctor` warn whenever `tools.exec.allow_remote=true`, or only when QR/file-decoding instructions are detected?
