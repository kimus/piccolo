## Context

`renderDockerfile` emits, in order: an apk bootstrap layer, a `nixbld` group + 32 users, a `curl | bash` Devbox installer, `COPY devbox.json`, a Nix `ENV PATH`, the toolchain install layer (`devbox install`, then `install -m 0755` each command's store binary into `/usr/local/bin` plus a gc-root symlink, then `nix-collect-garbage`), the source-integration install layer, release-bundle COPY layers, and content labels.

Measured in the shipped image: `/nix/store` 5.2 GiB (4.28 GiB tool-attributable across 457 paths, ~0.58 GiB floor), `/usr/local/bin` 316 MiB of copies of binaries that still exist in the store, rootfs 5.75 GiB, `.Size` 8.26 GB, cold build ~6m50s.

Both `sipeed/picoclaw:v0.3.1` and `sipeed/picoclaw:launcher` are Alpine 3.21 with apk, and the first Dockerfile layer already uses apk.

## Goals / Non-Goals

- Goal: same advertised commands, image roughly a sixth of the size, build in ~2 minutes
- Goal: keep the guarantees that survive from the Nix design — per-command verification, content-addressed tag, layer ordering that lets integration changes reuse the toolchain layer
- Goal: keep the toolchain list user-editable in `piccolo.yaml`
- Non-goal: on-demand or runtime installation (rejected as too complex; the skill's "do not run a package manager" rule stands)
- Non-goal: CI that builds a base image
- Non-goal: shrinking the default list

## Decisions

### One apk layer replaces the Devbox/Nix stack

```
RUN apk add --no-cache --update <packages> \
 && for tool in <commands>; do command -v "$tool" >/dev/null || { echo "configured command is not provided by the installed packages: $tool" >&2; exit 3; }; done
```

Verification keeps the exact semantics it had: a configured command that no package provides fails the build by name. Binaries land in `/usr/bin` on PATH, so the `install -m 0755` copies and the gc-root pinning both disappear with it — nothing re-garbage-collects the store, so there is nothing to pin.

The layer stays where it is (before integration layers), so the measured cache behaviour survives: an integration change reuses it, a package-list change invalidates it and everything after.

### Bootstrap keeps bash/curl/ca-certificates/tar/git/coreutils

`shadow` existed only to create `nixbld`, and `xz` only for Nix tarballs. Both go. The bundle install block still needs `cp -a`, `install`, and `command`, all present in busybox/coreutils.

### apk names, not nixpkgs names, in `runtime.toolchain.packages`

Renames in the default list: `tesseract` → `tesseract-ocr` + `tesseract-ocr-data-eng`, `yq-go` → `yq`, `libxml2` → `libxml2-utils`, `sqlite` → `sqlite`, `exiftool` → `exiftool`, `p7zip` → `p7zip`; `zbar`, `file`, `python3`, `jq`, `poppler-utils`, `pandoc`, `ffmpeg`, `unzip`, `libreoffice`, `imagemagick` are unchanged. `7za` is dropped from `DefaultCommands` (Alpine provides `7z`/`7zz`/`7zr`).

`commands` are unchanged, so an agent-visible contract does not move even though the package list does.

### Determinism is the user's lever, not new machinery

apk has no lockfile and Alpine repos float, so the resolved bits can differ between two builds of the same tag. Deliberately not solved with a pinning format: the package list passes through to apk verbatim, so `jq=1.7.1-r0` in `piccolo.yaml` pins. The content-derived tag continues to identify what was *requested*; the labels record the same. This is a documented boundary, and a change to the base image tag (Alpine version) is a rebuild.

### Retire the source-build path

`piccolo integration build` resolved `github:owner/repo?ref=…` flake refs through Devbox. There is no apk equivalent, and release bundles already carry their own runtime closure, so the path is deleted rather than re-implemented. `source: build` stays parseable so an existing manifest fails with "no longer supported; reinstall with `piccolo integration add`" instead of a strict-decode error that hides the cause.

### Bundles keep unpacking into `/nix/store`

The gws 0.3.4 tarball contains 1261 `/nix/store` paths including its own bash and glibc; its wrapper's absolute paths must resolve. The bundle layer still copies that closure into `/nix/store`. What changes is that Nix is no longer *installed* — `/nix` becomes ordinary image content, and the existing rule that no host directory may be mounted over it stays exactly as important.

## Risks / Trade-offs

- **Floating apk versions** mean a rebuild can produce different bytes under the same tag. Accepted, with `name=version` pinning documented; the alternative (a lockfile format) is the complexity this change is removing.
- **musl vs glibc.** Tools now link against the base's musl instead of a Nix glibc. `libreoffice`, `tesseract`, `pandoc`, `ffmpeg`, `imagemagick` all install and expose their commands in the verification run; behaviour equivalence (a real PDF and DOCX parse through the gateway) is a task, not an assumption.
- **OCR language coverage shrinks to English** by default. That is the honest trade for 1.0 GiB, and `runtime.toolchain.packages` can add `tesseract-ocr-data-<lang>`.
- **Retiring source builds breaks** anyone using `piccolo integration build`. Accepted under the no-migration rule, with an actionable error.
- **Integration bundles that assume an installed Nix** (rather than carrying a closure) would stop working. gws is verified to carry its own; the assumption is documented rather than tested against unknown integrations.

## Migration Plan

No automatic migration. After the change: `piccolo doctor` reports the image stale (toolchain label differs), the user runs `piccolo image build --set` and `piccolo restart`. Workspaces that wrote their own nixpkgs package names must rename them; the build fails naming the missing command, which points at the cause.

## Open Questions

- None blocking. (`tesseract-ocr-data-eng` vs adding more languages per workspace is a user choice, already expressible in the manifest.)
