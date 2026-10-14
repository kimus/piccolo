## Why

Piccolo's file/media handling now depends on a runtime toolchain, but the installed packages and exposed commands are still mostly hard-coded. Recent live use showed gaps: Poppler helper commands were installed but not exposed, LibreOffice and ImageMagick were missing for office/image conversion, agents attempted unavailable `cv2`, and safety-guard failures need clearer path discipline.

This change makes the runtime toolchain configurable and stronger by default while keeping Piccolo's config split clean: `piccolo.yaml` owns container/runtime setup, and `config.json` owns PicoClaw agent behavior.

## What Changes

- Add a Piccolo-owned runtime toolchain section to `piccolo.yaml` with configured Devbox/Nix packages and command names to expose on PATH.
- Generate strong default media/office tooling at init, including Poppler helpers, LibreOffice, and ImageMagick.
- Apply the configured toolchain during `serve`/`restart` so edits to `piccolo.yaml` can be reconciled without rebuilding the image solely to change packages.
- Keep the Devbox/Nix volumes persistent, but add explicit runtime maintenance commands/status for applying, pruning, or resetting toolchain/cache state.
- Update doctor to validate configured packages/commands and diagnose missing exposed commands.
- Update the `file-handling` skill so agents prefer deterministic CLI tools, avoid `cv2` by default, and write outputs/intermediates inside the workspace instead of `/tmp`.
- Add setup UI controls for `tool_feedback` and web console enable/disable.
- Fix setup navigation messaging/behavior so ESC quits from the home menu and goes back from submenus/forms; stop advertising unsupported `q` navigation.

## Capabilities

### New Capabilities

- `runtime-toolchain`: Configure and apply container-side file/media utility packages and exposed commands.
- `runtime-maintenance`: Inspect, apply, prune, and reset persisted Devbox/Nix runtime state.
- `setup-ux`: Setup can toggle tool feedback/web console and has consistent ESC navigation.

### Modified Capabilities

- `runtime-image-build`: The runtime image becomes a Devbox-capable base; the current workspace toolchain is applied from `piccolo.yaml` at serve time rather than treated as a fixed baked package list.
- `file-interpretation`: Generated skill guidance prefers stronger default media/office tools and gives explicit workspace-output path discipline.

## Impact

- `piccolo.yaml` schema, init generation, strict manifest parsing, doctor validation, and README examples.
- Runtime image setup script and serve/restart post-health setup flow.
- Docker runner support for runtime maintenance commands and volume cleanup/status if needed.
- `piccolo setup` UI and patching logic for `agents.defaults.tool_feedback` and web-console-related container settings.
- Generated `skills/file-handling/SKILL.md` and setup sync behavior.
- Tests covering manifest schema, deterministic generation, toolchain apply/symlink behavior, doctor warnings, setup toggles, and ESC navigation semantics.
