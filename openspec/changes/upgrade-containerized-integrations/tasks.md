## 1. CLI surface

- [x] 1.1 Add `piccolo integration upgrade` subcommand under the existing integration command group.
- [x] 1.2 Add flags for `--all`, `--to <alias-or-ref>`, `--tag`, `--pull`, and `--yes` consistent with existing add/build behavior.
- [x] 1.3 Validate arguments so exactly one target mode is selected: `<name>` or `--all`.
- [x] 1.4 Return actionable errors for unknown integrations and empty installed integration lists.

## 2. Upgrade planning

- [x] 2.1 Load the structured workspace layout and current `piccolo.yaml` manifest.
- [x] 2.2 Resolve the selected installed integration by name from `runtime.integrations`.
- [x] 2.3 Resolve `--to <alias-or-ref>` through the existing integration alias/ref resolver.
- [x] 2.4 Build the runtime image integration list from all currently installed integrations, replacing only the selected integration's ref/descriptor command when `--to` is provided.
- [x] 2.5 Preserve the launcher base-image selection behavior when the current image is a local Piccolo runtime image.
- [x] 2.6 Print unpinned-ref warnings for stored refs and `--to` target refs.

## 3. Image rebuild and descriptor refresh

- [x] 3.1 Reuse the runtime image build plan to build a fresh image containing the full integration set.
- [x] 3.2 Run descriptor validation from the newly built image for the selected integration during single integration upgrade.
- [x] 3.3 Run descriptor validation from the newly built image for every integration during `--all`.
- [x] 3.4 Use the structured descriptor container mounts: `picoclaw/` and `integrations/`, without mounting `runtime/nix` over `/nix`.
- [x] 3.5 Refresh descriptor-derived manifest metadata (`name`, `display_name`, `commands`, `descriptor_command`, `setup_command`, `setup_requires_tty`, `setup_ports`) from successful descriptor results.

## 4. Safe manifest update

- [x] 4.1 Ensure build failures leave `container.image` and `runtime.integrations` unchanged.
- [x] 4.2 Ensure descriptor failures leave `container.image` and `runtime.integrations` unchanged.
- [x] 4.3 Update `container.image` and refreshed integration metadata only after all required validations succeed.
- [x] 4.4 Preserve integration state directories under `integrations/<name>` and never run setup automatically.
- [x] 4.5 Print success output with the new image tag and next-step guidance for optional setup and required restart.

## 5. Tests

- [x] 5.1 Add unit tests for argument validation and unknown/no integration errors.
- [x] 5.2 Add unit tests for single-integration upgrade preserving other installed integrations in the build plan.
- [x] 5.3 Add unit tests for `--all` descriptor refresh behavior.
- [x] 5.4 Add unit tests for `--to` alias/ref switching and manifest ref updates.
- [x] 5.5 Add unit tests proving build and descriptor failures do not write the manifest.
- [x] 5.6 Add regression tests that upgrade descriptor containers mount `integrations/` but do not mount `runtime/nix`.

## 6. Documentation and verification

- [x] 6.1 Document `piccolo integration upgrade <name>` and `piccolo integration upgrade --all` in README integration workflow docs.
- [x] 6.2 Document unpinned-ref behavior and when to rerun integration setup after an upgrade.
- [x] 6.3 Run `go test ./...`.
- [x] 6.4 Validate `upgrade-containerized-integrations` OpenSpec artifacts.
