## Why

`piccolo serve` bind-mounts the workspace Nix cache over the container's `/nix`, which **hides the image's own store**. Measured on a running gateway: the image carries 1464 store entries, the container sees 68, and as a result `gws`, `piccolo-gws`, `python3` and `jq` are all unexecutable inside the gateway — while the *same* image runs them fine in an integration one-shot, because one-shots don't take that bind. An integration therefore works when Piccolo configures it and fails when Piccolo runs it, and `status` reports the gateway healthy throughout, because `/health` says nothing about the store.

Separately, the content-derived image tag omits the installed integration version, so `piccolo integration add <pkg>@<older>` can reuse the current tag while recording a version the image does not contain — reproduced: manifest said `0.2.0` while the image still reported the `0.3.1` descriptor fields.

## What Changes

- **Bake the toolchain into the image** as its own layer, placed before the integration layers, so layer caching keeps it reused while integrations change and rebuild.
- **Remove the runtime binds** for `/nix` and the Devbox profile, and delete the serve-time tool-install step. The Nix store becomes read-only image content that nothing shadows.
- **Put integration identity into the tag**: each installed release integration contributes `package@version+digest`, computed from **release metadata only** — never by downloading artifacts.
- **Name release-bundle context paths by package** instead of positional index, so adding an integration doesn't invalidate unrelated layers.
- **Label image content**: base image, toolchain digest, and one label per integration at `version+digest`. Labels are for humans and queries; the tag stays the cache identity.
- **Warn, never rebuild automatically**: when the image in use differs from the desired image, `serve`/`restart`/`status`/`doctor` say so and name the rebuild command. Per the project's WiP policy there are no migrations and no implicit rebuilds.

## Capabilities

### New Capabilities

### Modified Capabilities

- `runtime-image-build`: toolchain baked as a cached layer, shadowing binds removed, integration identity in the tag, package-named bundle paths, content labels
- `runtime-maintenance`: serve-time tool installation replaced by build-time toolchain; image drift surfaced as a warning with the rebuild command
- `release-based-integrations`: integration identity computable from resolve-time metadata, so planning and drift checks never download artifacts

## Impact

- `internal/runtimeimage`: Dockerfile layer order, tag inputs, bundle context naming, generated `LABEL` lines
- `internal/workspace`: `RuntimeBinds()` loses the `/nix` and Devbox entries (media mirror stays)
- `cmd/piccolo/serve.go`: drops the `piccolo-setup-tools` invocation and `devboxInstallTimeout`; adds a desired-vs-current image check
- `cmd/piccolo/integration.go`: `fetchReleaseBundlesForManifest` stops downloading every installed bundle just to plan
- `internal/doctor`: new check for image/toolchain/integration drift with the rebuild hint
- Tests: tag identity, label content, planning without downloads, doctor warnings
- Existing workspaces (including any already-created `.data`) need one manual `piccolo integration add`/`upgrade`/`image build` afterwards; no migration code, by policy
