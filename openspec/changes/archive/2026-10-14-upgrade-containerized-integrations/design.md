## Context

Piccolo currently installs containerized integrations through `piccolo integration add <alias-or-ref>`. The add flow resolves an alias/ref, builds a new local runtime image containing all selected integration commands, runs the newly installed command's descriptor in a one-shot container, and records descriptor metadata in `piccolo.yaml` under `runtime.integrations`.

After installation, users have no first-class lifecycle command for refreshing an integration. Re-running `integration add gws` is semantically awkward and mixes first-time installation with upgrade intent. It also does not clearly communicate that the existing integration state under `integrations/gws` is preserved while the image code is rebuilt.

The runtime image is holistic: a local image contains the base PicoClaw image, runtime-toolchain setup machinery, and every installed integration package. Therefore upgrading one integration requires rebuilding an image from the complete installed integration set, not patching a single binary in place.

## Goals / Non-Goals

**Goals:**

- Provide an explicit `piccolo integration upgrade <name>` flow for already installed integrations.
- Provide `piccolo integration upgrade --all` for refreshing all installed integration descriptors in one rebuild.
- Preserve existing integration state directories and PicoClaw config files.
- Keep upgrades safe: do not update `piccolo.yaml` unless image build and descriptor validation succeed.
- Allow explicit source changes with `--to <alias-or-ref>` for version/ref switches.
- Warn when upgrading from unpinned refs.
- Reuse the existing runtime image build and descriptor validation mechanisms.

**Non-Goals:**

- Do not run integration setup automatically after upgrade.
- Do not migrate provider-specific integration state.
- Do not solve dependency/version locking for all Devbox/Nix packages beyond recorded refs.
- Do not implement an integration marketplace or remote update checking service.
- Do not patch integration code into an existing image in place.

## Decisions

### Rebuild the whole runtime image

`integration upgrade` will rebuild a new local runtime image from the current base image and the complete `runtime.integrations` list. If only `gws` is upgraded, the generated build plan still includes all other installed integrations so the resulting image does not drop them.

Alternative considered: rebuild only the selected integration binary and copy it into the current image. This would require image mutation or a new Dockerfile stage that depends on the old local image and would complicate deterministic tags and descriptor validation. Rebuilding from the manifest's integration set is simpler and more consistent with `integration add`.

### Preserve current image on failure

The command will build and validate first, then write `piccolo.yaml`. If build fails, descriptor execution fails, or descriptor parsing/validation fails, Piccolo leaves the current `container.image` and `runtime.integrations` metadata untouched.

This mirrors the current add behavior and avoids leaving the workspace pointing at a partially validated image.

### Refresh descriptors from the new image

After a successful build, Piccolo runs descriptor commands inside one-shot containers created from the new image. For `upgrade <name>`, only the selected integration descriptor must be refreshed. For `upgrade --all`, every installed integration descriptor must be refreshed.

Descriptor one-shots should use the structured layout mounts needed by integrations: `picoclaw/` as `/root/.picoclaw` plus `integrations/` as `/root/.piccolo/integrations`. They must not mount workspace `runtime/nix` over `/nix`, because integration binaries are installed at image build time and may depend on the image's build-time Nix store.

### Use stored refs unless `--to` is provided

By default, upgrade uses the existing manifest `ref` for the selected integration. With unpinned refs such as `github:kimus/piccolo-gws`, rebuilding may pull newer code, so the command prints a warning.

`--to <alias-or-ref>` lets the user switch versions or sources explicitly. The alias/ref resolution rules are the same as `integration add`; after successful validation the manifest stores the resolved ref.

### No automatic setup rerun

Upgrade changes code and descriptor metadata only. Integration state under `integrations/<name>` is preserved, and setup is not run implicitly because setup may prompt, mutate provider config, or require external auth. On success Piccolo prints next-step guidance to run setup only when reconfiguration is desired or when descriptor/setup instructions indicate it.

## Risks / Trade-offs

- **Unpinned refs can change unexpectedly** → Print an explicit warning before building; allow pinned refs through `--to`.
- **Rebuilding all integrations can be slow** → Make progress output clear and reuse Docker/Nix caches where available.
- **Descriptor refresh for multiple integrations can partially fail** → Treat descriptor refresh as all-or-nothing for manifest writes.
- **Integration setup command may change after upgrade** → Store refreshed descriptor metadata, but do not run setup automatically; tell the user when setup may be needed.
- **Base image selection may accidentally build from an already-local runtime image** → Reuse the current add behavior: if the configured image is a Piccolo local runtime image, derive the original PicoClaw gateway or launcher base from manifest ports.

## Migration Plan

No workspace migration is required. Existing workspaces with `runtime.integrations` can use the new command immediately. Workspaces without installed integrations receive an actionable error.

Rollback is manual and simple: users can restore the previous `container.image` and integration metadata from version control or the previous `piccolo.yaml` content if an upgrade succeeds but the new integration behavior is undesirable.

## Open Questions

- Should Piccolo keep a backup of `piccolo.yaml` before successful upgrade writes, similar to setup's `config.json.bak` behavior?
- Should `upgrade --all` refresh descriptors sequentially and show per-integration progress, or fail fast on the first descriptor error?
- Should Piccolo record a build timestamp or previous image tag for status/debug output, or avoid adding more manifest state?
