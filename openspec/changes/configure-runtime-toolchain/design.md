## Context

Piccolo now builds a Devbox-capable PicoClaw runtime image and runs `piccolo-setup-tools` after the gateway is healthy. The current setup script installs a fixed package list and symlinks a fixed command list into `/usr/local/bin` by resolving real `/nix/store` binaries through `devbox shellenv`.

Live use exposed the next boundary:

- Poppler's `pdftoppm`, `pdfinfo`, and `pdftocairo` are available through `poppler-utils` but not symlinked onto PATH.
- LibreOffice is needed for reliable PPTX/DOCX/XLSX conversion but is missing.
- ImageMagick (`magick`, `convert`, `identify`) is needed for image normalization/inspection but is missing.
- Agents may try Python OpenCV (`cv2`), which is not installed and should not be the default first-line approach.
- Exec safety guards still block many `/tmp` and path traversal patterns; generated guidance must keep uploaded media as input paths but place derived outputs inside the workspace.

The desired direction is to make the runtime toolchain a Piccolo-owned workspace setting in `piccolo.yaml`, not a hidden fixed list in code or a duplicated PicoClaw runtime setting in `config.json`.

## Goals / Non-Goals

**Goals:**

- Make configured Devbox packages and exposed command names explicit in `piccolo.yaml` under a runtime/toolchain namespace.
- Generate strong defaults that include Poppler helpers, LibreOffice, and ImageMagick.
- Apply the configured toolchain from `piccolo.yaml` during `serve`/`restart` and through an explicit apply command.
- Preserve the lean image approach: tools install into persisted Devbox/Nix volumes after container start rather than being baked into the image.
- Remove stale exposed-command symlinks when commands are removed from config, while leaving the Nix cache intact unless the user prunes/resets it explicitly.
- Add diagnostics and maintenance commands for runtime toolchain state.
- Add setup controls for `tool_feedback` and web console enable/disable.
- Fix setup navigation so ESC is the consistent back/quit key.

**Non-Goals:**

- Do not add OpenCV/`cv2` to default installs in this change.
- Do not implement profiles yet; the stronger media/office set is the default for now.
- Do not put PicoClaw runtime tool toggles or agent model/tool settings into `piccolo.yaml`.
- Do not silently rewrite user-owned `config.json` or agent files outside explicit setup/sync flows.
- Do not mount or persist all `/tmp`.

## Decisions

### Decision 1: Add `runtime.toolchain` to `piccolo.yaml`

Add a Piccolo-owned runtime section:

```yaml
runtime:
  toolchain:
    packages:
      - zbar
      - file
      - python3
      - jq
      - yq-go
      - poppler-utils
      - tesseract
      - pandoc
      - libxml2
      - exiftool
      - ffmpeg
      - sqlite
      - unzip
      - p7zip
      - libreoffice
      - imagemagick
    commands:
      - zbarimg
      - file
      - python3
      - jq
      - yq
      - pdftotext
      - pdfinfo
      - pdftoppm
      - pdftocairo
      - tesseract
      - pandoc
      - xmllint
      - exiftool
      - ffmpeg
      - ffprobe
      - sqlite3
      - unzip
      - 7z
      - 7za
      - libreoffice
      - soffice
      - magick
      - convert
      - identify
```

Rationale: packages and commands are different concepts. Devbox package names do not always equal command names (`poppler-utils` provides multiple PDF commands, `p7zip` provides `7z`, `libxml2` provides `xmllint`, etc.). Capturing both makes the source of truth explicit and debuggable.

Alternative considered: make only packages configurable and maintain command mappings internally. Rejected for now because unknown/custom packages need a way to expose commands without code changes.

### Decision 2: Runtime image is Devbox-capable; toolchain is applied from workspace config

The runtime image should continue to install Devbox, `nixbld` group/users, and the setup script, but the desired package/command list should come from the mounted workspace's `piccolo.yaml` at serve/apply time.

Rationale: this aligns with future public Piccolo-maintained images. Users should be able to edit `piccolo.yaml` and run `piccolo restart` or an apply command without rebuilding an image just to add/remove a package.

Alternative considered: bake the package list into each local image build. Rejected because it makes `piccolo.yaml` less authoritative and requires image rebuilds for routine tool changes.

### Decision 3: Apply reconciles Devbox project and PATH symlinks

Applying a toolchain should:

1. Ensure the persisted Devbox global/default directory exists.
2. Create or update its `devbox.json` from the configured package list.
3. Run `devbox install`.
4. Evaluate `devbox shellenv`.
5. Resolve each configured command with `command -v`.
6. Symlink each resolved real binary into `/usr/local/bin`.
7. Remove Piccolo-owned symlinks in `/usr/local/bin` for commands no longer configured.

Use a Piccolo-owned marker or sidecar state file to avoid deleting unrelated user/container binaries.

Rationale: direct symlinks preserve cwd and support relative paths, unlike `devbox run`; removing stale symlinks makes removals visible immediately. Nix store cleanup remains explicit.

### Decision 4: Cache cleanup is explicit and tiered

Default apply/restart should not aggressively delete Nix store contents. Provide explicit maintenance:

- `piccolo runtime status`: show configured packages/commands, exposed-command availability, and approximate volume/cache size when possible.
- `piccolo runtime apply`: apply `piccolo.yaml` toolchain to the running container, or report that `serve`/`restart` is needed.
- `piccolo runtime prune`: safe-ish garbage collection/pruning if supported by the installed Nix/Devbox environment.
- `piccolo runtime reset-cache`: delete the Piccolo Devbox/Nix volumes so the next serve reinstalls from scratch.

Rationale: removing a package should immediately remove its command exposure, but freeing disk is slower and potentially destructive enough to require an explicit command.

### Decision 5: Strong media/office defaults now, profiles later

Defaults should include LibreOffice and ImageMagick now. Profiles such as `minimal`, `media`, or `office` can be introduced later if defaults become too heavy.

Rationale: current user need is robust file/media handling out of the box, and the persisted-volume model amortizes install cost across restarts.

### Decision 6: Setup distinguishes agent settings from container/runtime settings

`tool_feedback` belongs to PicoClaw agent defaults in `config.json` and should be toggled from setup's tools/debugging area. Web console belongs to container settings and should be toggled by updating the image/ports consistently.

For web console, custom runtime images must preserve base flavor:

- dashboard off → gateway-based runtime image
- dashboard on → launcher-based runtime image and `127.0.0.1:18800:18800` binding

Doctor should continue detecting drift between port bindings and image flavor.

### Decision 7: File-handling skill gives path and tool-order discipline

Update the skill to teach:

- Uploaded media paths under `/tmp/picoclaw_media/...` are valid inputs when shown in message tags.
- Outputs/intermediates should be placed under the current workspace, such as `./work`, `./converted`, or `./ocr`, not generic `/tmp` and not `../` traversal.
- QR/barcode: prefer `zbarimg`; optionally preprocess with `magick`; avoid `cv2` unless explicitly needed.
- PDF: use `pdfinfo`, `pdftotext`, and for scanned pages `pdftoppm`/`pdftocairo` plus `tesseract`.
- Office files: use `soffice`/`libreoffice` for conversion when visual rendering is needed; unzip/XML inspection is acceptable for text-only extraction.
- Images: use `file`/`identify`, `magick`/`convert`, and `tesseract` before Python image libraries.

### Decision 8: ESC is the setup navigation key

Setup should advertise and implement:

- Home menu: ESC quits setup.
- Nested menus/forms: ESC cancels the current screen and returns to the previous menu.
- Ctrl+C remains immediate quit from anywhere.
- `q` should not be advertised unless explicitly implemented.

Rationale: current UI text advertises `q`, but standard `huh` selects do not honor it. ESC matches user preference and common modal UI behavior.

## Risks / Trade-offs

- [Risk] Stronger defaults increase first install size/time → Mitigation: persisted Devbox/Nix volumes amortize the cost; future profiles can reintroduce lighter defaults.
- [Risk] Nix package names or command names may differ across platforms/versions → Mitigation: doctor and apply report missing commands clearly; defaults use verified Nix names where possible.
- [Risk] Deleting stale symlinks could remove non-Piccolo binaries → Mitigation: track Piccolo-owned symlinks/state and only remove those.
- [Risk] Runtime apply after health means the gateway may be live before tools finish installing → Mitigation: serve should report toolchain setup progress and final success/warnings; doctor/status should detect incomplete toolchain.
- [Risk] Web console toggling with custom runtime images may cause image/port drift → Mitigation: setup updates both atomically and doctor warns on inconsistent configurations.
- [Risk] Tool feedback can expose command args/paths in chat → Mitigation: setup text explains the tradeoff; default/recommend it especially when remote exec is enabled.

## Migration Plan

- Existing `piccolo.yaml` files without `runtime.toolchain` continue to parse; Piccolo treats missing toolchain as the generated default and can write it through explicit setup/sync or init for new workspaces.
- Existing custom runtime images continue to run, but doctor warns if the runtime image lacks the current setup script or if configured commands cannot be exposed.
- `piccolo setup tools` or a runtime apply command can add the new manifest section and refresh skills/config with confirmation.
- Rollback is editing/removing `runtime.toolchain` entries and running `piccolo runtime apply` or `piccolo restart`; cache reset is explicit if disk cleanup is desired.

## Open Questions

- Should `piccolo runtime prune` attempt Nix GC first, or should initial implementation only provide `reset-cache` for predictable cleanup?
- Should setup include an interactive package/command editor immediately, or should the first version document YAML edits and provide apply/status validation?
- Should `tool_feedback` default to enabled for all new workspaces, or only when remote exec is enabled?
