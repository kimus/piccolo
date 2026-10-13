## Why

Piccolo can add an integration into a runtime image, but once an integration is installed there is no productized way to rebuild it from its recorded source. Users need an explicit upgrade path that refreshes integration code and descriptor metadata without re-running first-time add flows or losing integration state.

## What Changes

- Add `piccolo integration upgrade <name>` to rebuild an installed integration from its recorded ref.
- Add `piccolo integration upgrade --all` to rebuild and refresh all installed integrations in one image.
- Rebuild the runtime image from the complete installed integration set so existing integrations remain present.
- Run descriptor validation from the newly built image before updating `piccolo.yaml`.
- Preserve provider-owned integration state under `integrations/<name>` and never run setup automatically.
- Support selecting a new source with `--to <alias-or-ref>` for an explicit version/ref switch.
- Warn when upgrading from unpinned refs because the resulting code may change.
- Fail safely: if build or descriptor validation fails, leave the current workspace image and integration metadata unchanged.

## Capabilities

### New Capabilities
- `integration-upgrades`: Rebuild and refresh installed containerized integrations from recorded or explicitly selected refs.

### Modified Capabilities
- None.

## Impact

- CLI: new `piccolo integration upgrade` subcommand and flags.
- Manifest: uses existing `runtime.integrations` metadata; may update stored refs and descriptor-derived metadata after successful validation.
- Runtime image builder: reused to build a new image from the full current integration set.
- Docker runner: reused for build and descriptor one-shot containers.
- Tests: command behavior, safe failure semantics, all-integration rebuilds, unpinned-ref warnings, and descriptor refresh.
