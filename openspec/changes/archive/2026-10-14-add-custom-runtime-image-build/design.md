## Context

The `file-handling` skill now gives agents the correct behavior, but the runtime image prevents the behavior from succeeding. The official PicoClaw image used by Piccolo lacks common command-line utilities (`zbarimg`, `file`, `python3`) and lacks a package manager (`apt-get` was not present in the observed container). Devbox can provide reproducible, on-demand file-handling tools if Devbox/Nix is present in the runtime image.

There is a second independent blocker: uploaded channel media is stored under paths such as `/tmp/picoclaw_media/...`, while PicoClaw's exec safety guard blocks absolute paths outside the agent workspace. A custom image alone does not grant exec access to those paths. The first implementation should therefore provide the Devbox-capable image build and document/teach a safe media workflow; if runtime support is needed for direct `/tmp/picoclaw_media` access, that must be handled explicitly through PicoClaw config/upstream behavior rather than hidden in the image build.

## Goals / Non-Goals

**Goals:**

- Add a local-first Piccolo command to build a custom PicoClaw runtime image.
- Base the image on the pinned official PicoClaw image from `piccolo.yaml` or Piccolo defaults.
- Install Devbox/Nix into the custom image so file-handling tools can be provided reproducibly.
- Generate a `devbox.json` with common file-handling utilities used by the `file-handling` skill.
- Let users explicitly adopt the built image by updating `piccolo.yaml`.
- Keep the generated manifest pinned to a concrete local image tag, never `latest`.
- Preserve the future path to a public image without making a registry dependency now.

**Non-Goals:**

- No public image publishing in this change.
- No replacement of the official PicoClaw runtime as the default for all users.
- No hidden serve-time mutation of images or containers.
- No custom PicoClaw runtime logic, native tools, MCP servers, or file decoder implementation in Piccolo.
- No automatic relaxation of PicoClaw exec safety for `/tmp/picoclaw_media` unless explicitly configured and validated.

## Decisions

### Decision 1: Add a local image build command

Add a command such as `piccolo image build` that builds a local runtime image and optionally updates `piccolo.yaml` to use it.

Rationale: users can unblock file-handling workflows immediately without waiting for Piccolo to publish and maintain public images. A future public image can reuse the same Dockerfile/build context.

Alternative considered — install tools into a running container. Rejected because Piccolo replaces containers and the current official image lacks a package manager.

### Decision 2: Generate a deterministic temporary/in-memory build context

The command renders a deterministic Dockerfile and `devbox.json` on demand and streams them to Docker as a build-context tar. It does not create or persist a `.piccolo-build/` directory in the user workspace.

The Dockerfile extends the pinned PicoClaw image and installs only the Devbox binary, leaving PicoClaw's default entrypoint untouched.

Rationale: the build inputs remain deterministic, unit-testable, and reusable for a future public image build.

### Decision 3: Lean image; install tools after run, symlink real binaries onto PATH

Tools are NOT baked into the image. The image carries only Devbox, a global `devbox.json` (the package LIST) at Devbox's default HOME-based location `/root/.local/share/devbox/global/default`, the `nixbld` group/users, and a `piccolo-setup-tools` script. After the gateway is healthy, `piccolo serve` runs `piccolo-setup-tools` once inside the container; packages and the Nix store persist in two named volumes:

- `piccolo-devbox-global` → `/root/.local/share/devbox/global/default`
- `piccolo-devbox-nix-cache` → `/nix` (the store the resolved binaries live in)

Two Devbox behaviors (verified live in this daemon-less single-user Nix container) rule out the naive approaches:

1. `devbox global install` links only a SUBSET of packages into `.devbox/nix/profile/default/bin` (`file`, `jq`, `zbarimg`, `pdftotext`, `ffmpeg`, `sqlite3`, `xmllint` stay unlinked even with a clean store), so putting the profile bin on PATH leaves tools missing.
2. `devbox run -- <tool>` resolves every package, but it always executes in the PROJECT ROOT, not the caller's cwd — so relative-path args (the agent's normal case) break.

The fix: `piccolo-setup-tools` runs `devbox install`, then `eval "$(devbox shellenv)"` (which exposes every package's real `/nix/store/.../bin` path via `nix print-dev-env`) and symlinks each tool's resolved binary into `/usr/local/bin`:

```sh
cd /root/.local/share/devbox/global/default
devbox install
eval "$(devbox shellenv)"
for tool in zbarimg file python3 ...; do
  ln -sf "$(readlink -f "$(command -v "$tool")")" /usr/local/bin/"$tool"
done
```

The tools then become ordinary PATH binaries that:

- resolve from any cwd (verified: all 15 resolve from `/`),
- accept relative AND absolute args (verified: `zbarimg --quiet --raw qr_test.png` decodes from the workspace; `/tmp/picoclaw_media/<name>` works too),
- preserve the caller's cwd (verified: `python3 -c 'os.getcwd()'` prints the workspace),
- carry zero per-call Devbox overhead (direct exec of the store binary).

The `/nix` store paths are stable across restarts (persisted volume), so the symlinks stay valid; on container recreation the ephemeral `/usr/local/bin` links are re-created by the setup script in seconds (store is warm).

The `nixbld` build group + users (GID 30000, UIDs 30001–30032) are baked into the image because Nix requires them and they live in `/etc/group` + `/etc/passwd`, which are ephemeral and reset on container recreation — otherwise the install fails after a restart with "the group 'nixbld' specified in 'build-users-group' does not exist".

Default package list (verified Nix names):

- `zbar` (provides `zbarimg`), `file`, `python3`, `jq`, `yq-go`, `poppler-utils` (provides `pdftotext`), `tesseract`, `pandoc`, `libxml2` (provides `xmllint`), `exiftool`, `ffmpeg`, `sqlite`, `unzip`, `p7zip`

Rationale: keeps the image small and build fast, makes tool state survive container recreation, and exposes tools as ordinary cwd-independent PATH commands.

Alternative considered — bake tools into the image at build time. Rejected: multi-GB image and slow builds; the volume approach installs once and persists.

Alternative considered — `devbox run` wrappers (cd to project, run tool). Rejected: `devbox run` cds to the project root, breaking relative-path args; symlinking the real binaries avoids the issue entirely.

Alternative considered — global profile bin directly on PATH. Rejected: `devbox global install` links only a subset of binaries here (verified).

### Decision 4: Keep adoption explicit

After building, Piccolo should ask before updating `piccolo.yaml` `container.image` to the local tag, or support a non-interactive flag such as `--yes --set-manifest`.

Rationale: `piccolo.yaml` is Piccolo-owned launch config, but changing images is operationally significant. The user should confirm.

### Decision 5: Media access is handled by PicoClaw config, verified against source

Uploaded channel media lands in `/tmp/picoclaw_media` (PicoClaw `media.TempDir()`). Verified against `picoclaw@v0.3.1`:

- `buildAllowReadPatterns` ALWAYS auto-injects `^/tmp/picoclaw_media(?:/|$)` and passes it to BOTH `read_file`/`list_dir` and `exec` as allowed path patterns, so exec is not blocked on that directory even though exec uses `restrict_to_workspace`.
- Piccolo additionally sets `agents.defaults.allow_read_outside_workspace: true` (this is a container, not a personal computer) so `read_file`/`list_dir` are unrestricted, and enables `agents.defaults.tool_feedback` (`max_args_length: 300`) so tool calls are visible in chat for debugging.
- `resolveMediaRefs` injects the real on-disk path as an `[image:/path]` / `[file:/path]` tag, so the agent uses the actual `/tmp/picoclaw_media/<name>` path directly.
- The media store is in-memory, so `media://` refs from a previous process warn `unknown ref` after restart; this is benign noise for stale history, and fresh uploads resolve normally.

Rationale: the earlier `path outside working dir` failures were caused by the agent referencing `/opt/piccolo/devbox` (now removed) and by `devbox run` cwd rewriting, not by the media dir. With real-binary PATH tools and PicoClaw's native media allowance, no path copying is required.

## Risks / Trade-offs

- [Risk] First tool setup downloads ~1GB of Nix packages → Mitigation: done once by `piccolo serve` post-health with a 30m budget (not the agent's 60s exec timeout), persisted in the `piccolo-devbox-global` + `piccolo-devbox-nix-cache` volumes so later restarts are near-instant.
- [Risk] Nix/Devbox package names differ from Debian names → Mitigation: verified names (`zbar`, `poppler-utils`, `libxml2`, `yq-go`, `p7zip`, ...); skill tells agents to call tools directly and never `apt-get`.
- [Risk] The official PicoClaw image base may not support Devbox → Mitigation: verified Alpine 3.21 + `apk` deps + jetify installer; smoke-tested.
- [Risk] Devbox profile-bin linking is incomplete and `devbox run` rewrites cwd → Mitigation: `piccolo-setup-tools` symlinks each tool's real `/nix/store` binary (resolved via `devbox shellenv`) into `/usr/local/bin`; verified all 15 tools resolve from `/` and decode relative + absolute paths with cwd preserved.
- [Risk] Local tags drift from the base image version → Mitigation: content-derived tag includes the base image ref; rebuilds never stack on a prior Piccolo runtime image (base resets to the official PicoClaw image).

## Migration Plan

No existing workspace changes automatically. Users opt in by running the build command, reviewing/confirming the generated image, and optionally letting Piccolo update `piccolo.yaml`. Rollback is editing `piccolo.yaml` back to the official pinned image and restarting.

## Open Questions

Resolved during implementation:

- Base distro/shell: Alpine 3.21, `apk` available, Devbox installs via the jetify script after `apk add bash curl ca-certificates xz tar git shadow coreutils`.
- Command name: `piccolo image build`.
- Build context: temporary/in-memory only; never written to the workspace.
- Media access: PicoClaw natively allows exec/read on `/tmp/picoclaw_media` (auto-injected pattern) and Piccolo enables `allow_read_outside_workspace`; no copying needed.
