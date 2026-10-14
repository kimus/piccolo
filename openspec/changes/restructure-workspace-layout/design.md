## Context

> **Partially superseded by `bake-runtime-toolchain-into-image`.** Two requirements named
> `runtime/devbox` and `runtime/nix` as mounted/created directories. Mounting a host Nix cache over
> `/nix` is the defect that made image binaries unexecutable, so those requirements were rewritten:
> `runtime/media` is the only cache Piccolo creates or mounts, and leftovers in older workspaces are
> neither mounted nor deleted.



Piccolo's original PoC used the workspace directory itself as PicoClaw home and mounted it directly to `/root/.picoclaw`. That kept the implementation small, but it mixed Piccolo launch config, PicoClaw runtime files, integration state, uploaded media, Devbox state, and Nix cache concerns in one place or in opaque Docker volumes.

The containerized integration work made this confusing: users expect Google Workspace state under an integration-name-based path such as `integrations/gws`, and they expect runtime caches such as Nix/Devbox to have visible, understandable locations. The project now needs a clearer workspace contract even though it is a breaking layout change from the original hard constraint.

## Goals / Non-Goals

**Goals:**

- Make `piccolo.yaml` the only top-level Piccolo-owned launch file.
- Put all PicoClaw-owned home data under `picoclaw/`.
- Put all integration-owned durable state under top-level `integrations/`.
- Put local runtime cache/state under top-level `runtime/`.
- Bind mount these directories into the container at the paths expected by PicoClaw, integrations, and Devbox/Nix.
- Provide migration from the previous flat layout.
- Keep `config.json` user-owned after creation/migration.

**Non-Goals:**

- Introduce a `.piccolo/` hidden root.
- Change PicoClaw's in-container default home path (`/root/.picoclaw`).
- Move provider-specific setup logic into Piccolo.
- Preserve Docker named volume cache data automatically in the first version; users may need to reinstall runtime tools into the new workspace-visible cache.

## Decisions

### Workspace root is Piccolo-owned, `picoclaw/` is PicoClaw home

The resolved workspace directory contains `piccolo.yaml` and subdirectories. Piccolo mounts `<workspace>/picoclaw` to `/root/.picoclaw` instead of mounting the workspace root.

This makes ownership visible:

```text
workspace/
  piccolo.yaml       # Piccolo launch/build metadata
  picoclaw/          # PicoClaw home mounted to /root/.picoclaw
```

### Integrations are top-level but mounted into PicoClaw home

Integration state lives at `<workspace>/integrations/<integration-name>`, not inside `picoclaw/`. During container runs, Piccolo mounts `<workspace>/integrations` to `/root/.piccolo/integrations`. The directory name MUST match the integration descriptor name; for example the `gws` integration uses `<workspace>/integrations/gws` and `/root/.piccolo/integrations/gws`.

This keeps provider state visible and separate from PicoClaw core runtime data. It also makes ownership clear inside the container: `/root/.picoclaw` belongs to PicoClaw, while `/root/.piccolo` belongs to Piccolo and Piccolo-managed integrations.

### Runtime caches are workspace-visible bind mounts

Piccolo replaces Docker named volumes for local runtime state with workspace-visible directories:

```text
workspace/runtime/devbox -> /root/.local/share/devbox/global/default
workspace/runtime/nix    -> /nix
workspace/runtime/media  -> /tmp/picoclaw_media
```

Agent media mirror mounts still target each agent workspace's `media` path, but source from the workspace-visible media directory or a bind-mounted equivalent rather than an opaque named volume.

### Migration is explicit and conservative

Doctor detects flat workspaces and reports a migration-required warning/failure with a suggested command. A migration command moves known PicoClaw files/directories into `picoclaw/`, moves existing top-level `integrations/` in place if already present, creates `runtime/` directories, updates `.gitignore`, and leaves a backup manifest of moved paths.

Migration must not overwrite existing destination files without confirmation.

### Path helpers centralize layout decisions

A workspace layout package should expose host/container path helpers for:

- `Root`
- `PicoclawHome`
- `ConfigJSON`
- `AgentsDir`
- `IntegrationsDir`
- `RuntimeDevboxDir`
- `RuntimeNixDir`
- `RuntimeMediaDir`

Commands should stop hardcoding `filepath.Join(dir, "config.json")` and instead use the layout helper.

## Risks / Trade-offs

- **Breaking old workspaces** → Provide detection and migration; do not silently move user files during ordinary `serve`.
- **Path churn across many commands** → Centralize layout helpers and update commands/tests through that API.
- **Nix cache under workspace can be large** → Document `runtime/nix` size and add gitignore entries; keep runtime cache deletion command available.
- **Bind-mounting `/nix` from a host directory may expose platform-specific behavior** → Use Docker bind mount to a workspace directory inside the Linux VM; this is equivalent to named volume persistence but visible to users.
- **Integration descriptors must use stable integration names** → Require integration state directories to match descriptor names, e.g. `gws`, and use `/root/.piccolo/integrations` as the parent mount.

## Migration Plan

1. Add layout helpers and update new workspaces generated by `init` to use the structured layout.
2. Update all commands to read/write `picoclaw/config.json` and `picoclaw/agents` through helpers.
3. Update Docker specs to mount the structured directories.
4. Add `piccolo workspace migrate` or equivalent migration command for flat workspaces.
5. Update `.gitignore` generation to ignore `picoclaw/config.json`, integration auth state, and runtime caches.
6. Update docs and AGENTS guidance to remove the old flat-layout constraint.

Rollback for a migrated workspace is manual but straightforward: stop the container, move files from `picoclaw/` back to root, and restore the previous Piccolo binary. Migration should write a summary file to aid manual rollback.

## Open Questions

- Should the migration command be `piccolo workspace migrate`, `piccolo init --migrate`, or part of `doctor --fix`?
- Should runtime cache directories be configurable in `piccolo.yaml`, or always fixed under `runtime/`?
