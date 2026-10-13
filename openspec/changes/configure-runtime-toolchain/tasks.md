## 1. Manifest and Generation

- [x] 1.1 Add `runtime.toolchain.packages` and `runtime.toolchain.commands` to the manifest schema with strict decoding and deterministic serialization.
- [x] 1.2 Define default runtime packages and commands including Poppler helpers, LibreOffice, and ImageMagick; keep OpenCV out of defaults.
- [x] 1.3 Update `piccolo init` generation so new workspaces include the default `runtime.toolchain` in `piccolo.yaml`.
- [x] 1.4 Add manifest/generation tests for default toolchain output, strict parsing, missing-toolchain compatibility, and stable ordering.

## 2. Runtime Toolchain Apply

- [x] 2.1 Refactor runtime image/setup planning so the image is Devbox-capable while package/command lists are read from workspace manifest at apply time.
- [x] 2.2 Update the setup script or generated setup payload to reconcile Devbox `devbox.json` from configured packages.
- [x] 2.3 Resolve configured commands via `devbox shellenv` and symlink real binaries into `/usr/local/bin`.
- [x] 2.4 Track Piccolo-owned symlinks and remove stale symlinks for commands no longer configured without deleting unrelated binaries.
- [x] 2.5 Ensure `serve`/`restart` runs runtime toolchain apply after gateway health and reports success/failure clearly.
- [x] 2.6 Add tests for apply payload rendering, symlink ownership/removal behavior, missing-command reporting, and serve output.

## 3. Runtime Maintenance Commands and Doctor

- [x] 3.1 Add runtime status command showing configured packages/commands and live command availability when the container is running.
- [x] 3.2 Add runtime apply command for reconciling the configured toolchain against a running gateway container.
- [x] 3.3 Add runtime prune command with safe Devbox/Nix garbage-collection behavior or clear unsupported messaging.
- [x] 3.4 Add runtime reset-cache command that confirms and removes Piccolo Devbox/Nix volumes for a clean reinstall.
- [x] 3.5 Extend doctor to validate runtime toolchain config, missing runtime image/setup capabilities, and unavailable configured commands.
- [x] 3.6 Add daemon-free tests for runtime command orchestration and doctor diagnostics.

## 4. Setup UX

- [x] 4.1 Add setup UI controls for enabling/disabling `agents.defaults.tool_feedback` while preserving unrelated config.
- [x] 4.2 Add setup UI controls for enabling/disabling the web console by updating image flavor and `127.0.0.1:18800:18800` port binding consistently.
- [x] 4.3 Ensure custom runtime gateway/launcher image flavor is preserved or clearly requested when toggling the web console.
- [x] 4.4 Implement ESC behavior: home menu exits setup, submenus/forms go back, Ctrl+C still exits from anywhere.
- [x] 4.5 Update setup help text to advertise ESC behavior and remove unsupported `q` navigation text.
- [x] 4.6 Add setup tests for tool feedback patching, web console toggling, and ESC/back/quit behavior where testable.

## 5. File-Handling Skill and Docs

- [x] 5.1 Update generated `file-handling` skill to document Poppler helper, LibreOffice, and ImageMagick workflows.
- [x] 5.2 Update generated guidance to prefer CLI tools over Python OpenCV by default and reserve `cv2` for explicit/custom cases.
- [x] 5.3 Update path discipline guidance: use `/tmp/picoclaw_media/...` only as input and write outputs/intermediates inside the workspace.
- [x] 5.4 Update setup sync so existing workspaces can explicitly refresh the skill and toolchain-related guidance.
- [x] 5.5 Update README with `runtime.toolchain` config, default packages/commands, apply/restart workflow, and cache cleanup commands.

## 6. Validation and Smoke Tests

- [x] 6.1 Run `go test ./...`.
- [x] 6.2 Run `openspec validate configure-runtime-toolchain`.
- [x] 6.3 Smoke-test a fresh workspace/default runtime: `pdfinfo`, `pdftoppm`, `pdftocairo`, `soffice`, `magick`, `convert`, and `identify` resolve from arbitrary cwd.
- [x] 6.4 Smoke-test media workflows: QR/barcode decode, PDF page render + OCR, and PPTX/DOCX conversion with outputs written under the workspace.
- [x] 6.5 Smoke-test command removal: remove a command from `runtime.toolchain.commands`, apply, and verify its Piccolo-owned symlink is gone while cache remains.
