## 1. Measure before changing the design

- [x] 1.1 Current toolchain footprint: workspace Nix cache 162 MB, current runtime image 440 MB
- [x] 1.2 Baked build measured: **8.28 GB image**, toolchain layer **5.78 GB**, first build ~2m20s warm / ~6m50s cold
- [x] 1.3 In-layer `nix-collect-garbage -d` recovers only ~30 MB — the default closure genuinely is that big (libreoffice/ffmpeg/imagemagick/tesseract). Recorded in README/AGENTS; `runtime.toolchain.packages` is the reduction knob
- [x] 1.4 Rebuild after an integration-only change reuses the toolchain layer (verified: unchanged layers reported `Using cache`, toolchain layer not re-executed)

**Consequence accepted:** the honest measurement contradicted the design's estimate. Nothing about the fix changes — the bind shadowing was total and silent — but the size cost is now documented instead of assumed.

## 2. Integration identity in the tag

- [x] 2.1 Sorted `owner/repo@version+digest` list added to the tag inputs
- [x] 2.2 Identity sourced from release metadata (`ResolvedVersion` + `ArtifactDigest`), never from artifact bytes
- [x] 2.3 Rebuild *decision* path downloads nothing: `internal/runtimeplan` composes a comparable plan with empty bundle `Root`; `ContextTar` now refuses such a plan, so a comparison can never be mistaken for a build
- [x] 2.4 No `--force` cache-busting needed for version churn — version/digest are tag inputs
- [x] 2.5 Test: two versions of one integration produce different tags
- [x] 2.6 Test: install order does not change the tag
- [x] 2.7 Test: metadata-only plan computes a tag with zero artifact downloads
- [x] 2.8 Found while testing: identity is carried **twice** (explicit tag input + the label block inside the Dockerfile). Removing one alone still invalidates the cache; documented so nobody removes both. Mutation-checked.

## 3. Toolchain baked into the image

- [x] 3.1 Toolchain install layer inserted after the Devbox config copy, before integration layers
- [x] 3.2 Cache key is the package list (the `COPY devbox.json` already precedes it)
- [x] 3.3 Built image exposes `python3`, `jq`, `file`, `tesseract`, `gws`, `piccolo-gws` executably with **zero** binds over `/nix` (verified in the running gateway: `binds over /nix: 0`, `runtime status`: 24 available, 0 missing)
- [x] 3.4 Base layer order left stable so unchanged layers stay cached
- [x] 3.5 Found live: `nix-store --add-root <path> --indirect` fails with `no operation specified` (it needs `--realise`). Both the toolchain and source-build layers now use plain symlinks under a gcroots directory — Nix's direct-root mechanism, no operation to get wrong
- [x] 3.6 Found live: a failed build was reported as **success** (the classic builder sends a failing RUN as stream text with HTTP 200). `BuildImage` now surfaces daemon error messages AND verifies the tag exists afterwards; the old path wrote a manifest pointing at an image that did not exist

## 4. Remove the shadowing binds and start-time install

- [x] 4.1 Workspace Nix cache no longer bound over `/nix`
- [x] 4.2 Devbox profile bind removed
- [x] 4.3 Media bind kept (unrelated to the store)
- [x] 4.4 `ensureDevboxTools`, `devboxInstallTimeout` and the setup-progress writer removed from serve
- [x] 4.5 `piccolo-setup-tools` deleted from the image and the build context; `runtime apply` no longer installs (it compares the running image's toolchain label with the configured one and names the rebuild command)
- [x] 4.6 Test: gateway and one-shot run the same binaries from the same image
- [x] 4.7 Docs state that `runtime/devbox` and `runtime/nix` become unused leftovers that Piccolo never deletes (`runtime reset-cache --yes` is the opt-in), and doctor stopped requiring them

## 5. Package-named bundle paths

- [x] 5.1 Bundle context dirs derived from the package reference (`release-integrations/<owner>/<repo>`), sanitized per segment
- [x] 5.2 Test: adding an integration leaves existing paths unchanged
- [x] 5.3 Test: `../` and `..` segments cannot escape the bundle root

## 6. Image content labels

- [x] 6.1 `piccolo.base` emitted
- [x] 6.2 `piccolo.toolchain` emitted (short digest of packages + commands)
- [x] 6.3 One `piccolo.integration.<sanitized package>` label per installed release, value `version+short-digest`; single source of truth shared with `Plan.Labels()` so a comparison cannot check something other than what the build wrote
- [x] 6.4 Labels are image labels, so a container inherits them and a promotion rename cannot desynchronise them
- [x] 6.5 Live check: `docker image inspect` shows all three kinds of label on the built image
- [x] 6.6 Found live: the manifest recorded the *descriptor's* command list while the image verified the *artifact's* list, so the drift plan differed from any build. Recording now follows the artifact (`installedCommands`), and `image build --set` refreshes version/digest/commands (`refreshInstalledIdentity`) — after which desired tag == built tag exactly

## 7. Drift reporting

- [x] 7.1 Desired image computed from metadata only (`internal/runtimeplan`)
- [x] 7.2 Reason naming: base image, toolchain list, integration added/removed/version-changed, predates-labels
- [x] 7.3 Warned in `serve`, `status` (and therefore `restart`) and `doctor`; no implicit rebuild anywhere
- [x] 7.4 Message names `piccolo image build --set && piccolo restart`
- [x] 7.5 Unverifiable → `doctor` reports it once and startup continues; a missing image is reported by the presence check only (no double warning)
- [x] 7.6 Content drift vs **template drift**: identical labels with a differing tag is a quiet note, not a "rebuild me" warning
- [x] 7.7 Tests: stale toolchain, predates-labels, missing image, template-only, and up-to-date cases (daemon-free via the Engine seam)
- [x] 7.8 Live: doctor correctly reported the pre-label image as stale and reports the rebuilt image as matching

## 8. Verification and docs

- [x] 8.1 README rewritten for the baked toolchain, measured size cost, drift reporting, promotion/gate/revert, rollback
- [x] 8.2 AGENTS.md: mounts rule, serve sequence, `piccolo.yaml` schema (`previous_image`), acceptance criteria 14-20, decisions log
- [x] 8.3 Web-console rule still holds (base flavor preserved on rebuild via `:18800` detection) and is now also covered by the base-image drift reason
- [x] 8.4 `go build ./...`, `go vet ./...`, `go test ./...` all green (9 packages)
- [x] 8.5 Live: gateway executes `gws 0.22.5`, `piccolo-gws 0.3.1`, `python3 3.14.7`, `jq`, `file`, `tesseract` with no `/nix` bind

## Not done from the original plan

- Bundle artifacts are still re-downloaded for the build *context* (the context needs every installed bundle's bytes). The download-free guarantee applies to planning/drift, which is what runs on every start. Persistent bundle caching is a separate optimization.
