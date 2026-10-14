## Why

The file-handling skill can teach agents to use deterministic decoders/parsers, but the official PicoClaw image is too minimal for that workflow: it lacks common tools, a package manager, and Devbox/Nix. Piccolo needs a local build path for a custom PicoClaw runtime image that can run Devbox-backed file utilities now, with a future path to a public prebuilt image.

## What Changes

- Add a Piccolo command that builds a local custom runtime image for the current workspace or project.
- Provide a generated Dockerfile/build context that extends the pinned official PicoClaw image and installs Devbox/Nix support.
- Provide a generated `devbox.json` containing common file-handling utilities used by the `file-handling` skill, such as QR/barcode, file type, OCR/PDF, metadata, media, structured-data, and archive tools.
- Let the build command optionally update `piccolo.yaml` to use the locally built image after explicit confirmation.
- Keep the command local-first: no public registry dependency and no publishing in this change.
- Document that a future release can replace the local image with a public Piccolo-maintained image.

## Capabilities

### New Capabilities
- `runtime-image-build`: Build and adopt a local custom PicoClaw runtime image with Devbox support and common file-handling tooling.

### Modified Capabilities

## Impact

- CLI: new command, likely `piccolo build image` or `piccolo image build`.
- Docker integration: build-image support through the existing Docker SDK runner or an isolated build interface.
- Workspace generation/setup: optional generation of `devbox.json` and runtime image build context.
- Manifest handling: explicit update of `piccolo.yaml` `container.image` to the local built tag.
- Doctor/status/docs: validate or explain custom image usage and file-handling tool availability limits.
- Tests: command planning, Docker build abstraction, manifest update behavior, generated build context, and documentation.
