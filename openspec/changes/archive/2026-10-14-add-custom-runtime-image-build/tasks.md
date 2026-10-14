## 1. Investigation and Build Recipe

- [x] 1.1 Inspect/smoke-test the official `docker.io/sipeed/picoclaw:v0.3.1` image to determine base distro, shell, writable paths, and whether Devbox/Nix installation is feasible.
- [x] 1.2 Determine working Devbox/Nix install steps for the base image and record any constraints in implementation comments/docs.
- [x] 1.3 Verify Nix package names for common file-handling tools (`zbarimg`, `file`, `python3`, `jq`, `yq`, `pdftotext`, `tesseract`, `pandoc`, `xmllint`, `exiftool`, `ffprobe`, `sqlite3`, archive tools).
- [x] 1.4 Investigate the uploaded-media path restriction and document whether direct `/tmp/picoclaw_media` access can be safely enabled or whether agents must copy media into the workspace first.

## 2. Build Context Generation

- [x] 2.1 Add an internal package or module that renders a deterministic runtime-image Dockerfile and `devbox.json`.
- [x] 2.2 Ensure the generated Dockerfile extends the configured pinned PicoClaw image and installs Devbox/Nix without changing PicoClaw's default entrypoint behavior.
- [x] 2.3 Add tests for deterministic Dockerfile and `devbox.json` rendering.
- [x] 2.4 Add documentation comments explaining local build context ownership and cleanup expectations.

## 3. Docker Build Support

- [x] 3.1 Extend the Docker runner abstraction with image build support without leaking SDK types outside the internal Docker package.
- [x] 3.2 Implement local image tagging, e.g. `piccolo-picoclaw:v0.3.1-devbox`, derived from the base image tag.
- [x] 3.3 Stream or summarize build output clearly, including errors from Devbox/Nix installation.
- [x] 3.4 Add daemon-free unit tests around build request composition and error handling.

## 4. CLI Command

- [x] 4.1 Add the runtime image build command (`piccolo image build` or final chosen name) with workspace resolution consistent with other commands.
- [x] 4.2 Add flags for non-interactive operation, tag override, skipping manifest update, and explicit manifest update confirmation.
- [x] 4.3 Fail fast when Docker is unavailable or `piccolo.yaml` is missing/invalid.
- [x] 4.4 Print next steps after build: how to adopt the image, restart, and verify Devbox/tools.

## 5. Manifest Adoption

- [x] 5.1 Implement explicit update of `piccolo.yaml` `container.image` to the local custom tag while preserving unrelated fields.
- [x] 5.2 Ensure generated/adopted image tags are pinned and pass existing manifest validation.
- [x] 5.3 Add tests for accept/decline/non-interactive manifest update behavior.

## 6. File-Handling Integration

- [x] 6.1 Update `file-handling` skill guidance to prefer `devbox run <tool>` when the custom image/devbox environment is available.
- [x] 6.2 Document the remaining media path restriction and the verified workflow for making uploaded media workspace-accessible if needed.
- [x] 6.3 Add doctor guidance/warnings for custom image use if the manifest references a Piccolo local runtime image that is not present locally.

## 7. Documentation and Verification

- [x] 7.1 Update README with the local custom image build workflow and future public image note.
- [x] 7.2 Add troubleshooting notes for Devbox build failures, missing package names, and uploaded-media path blocks.
- [x] 7.3 Run `go test ./...`.
- [x] 7.4 Smoke-test building the custom image locally when Docker is available and record manual verification notes.
