## Why

The baked runtime image measures **8.26 GB** (`.Size`), with **5.75 GiB of actual rootfs**: `/nix/store` alone is **5.2 GiB** — 4.28 GiB of it attributable to the 24 file-handling commands, plus ~0.58 GiB of Nix/Devbox machinery that exists even when no tools are installed. A cold build takes ~6m50s. That size is what forced the runtime-maintenance, retention, and CI designs to work around the image instead of ignoring it.

The size is a packaging artifact, not the tools. Both official bases are **Alpine with apk** — in fact `renderDockerfile` already begins with `RUN apk add --no-cache bash curl ca-certificates …`, so apk is the mechanism the image uses today for everything except the toolchain. Measured on Alpine 3.21, the same 16 tools cost **1308 MB of rootfs**:

| package | Devbox/Nix exclusive | apk marginal |
|---|---|---|
| libreoffice | 1.73 GiB | 761 MB |
| tesseract | 1.00 GiB | **39 MB** (Nix ships one 1015 MB path with **129 language files**; apk installs English data only) |
| zbar | 0.18 GiB | 2.1 MB |
| pandoc | 0.22 GiB | 235 MB |
| ffmpeg | 0.08 GiB | 90 MB |
| core (10 packages) | ~1.1 GiB | 107 MB |

Devbox cannot shed this: it is a Nix frontend, and nixpkgs closures are self-contained by design. The only way to stop paying for it is to stop using it.

## What Changes

- **Devbox and Nix are removed from the runtime image.** The toolchain layer becomes one `apk add --no-cache <packages>` followed by the existing per-command verification. The `devbox` installer, `devbox.json`, `piccolo-integrations-devbox.json`, `nixbld` group and 32 users, the Nix `ENV PATH`, `devbox shellenv`, gc-root symlinks, `nix-collect-garbage`, and the `/usr/local/bin` binary copies all go away.
- **BREAKING: `runtime.toolchain.packages` values are Alpine apk names now**, not nixpkgs attribute names. Commands are unchanged.
- **`piccolo integration build` is retired** (source-build integrations were Devbox/Nix flake refs and have no apk equivalent). `piccolo integration add <pkg>` from a release is the supported path; an existing `source: build` manifest entry fails with an actionable message instead of building.
- Default list keeps **full parity**: every command the file-handling skill advertises stays present, minus `7za`, which neither Alpine `p7zip` nor `7zip` provides (both ship `7z`/`7zz`). Verified: all 16 apk packages exist and provide all 24 advertised commands.
- Integration bundles are unaffected — they already ship their own `/nix/store` closure inside their release tarball and never needed Nix installed.
- No CI builds the base image; the user builds it after setup, as today.

## Capabilities

### Modified Capabilities

- `runtime-image-build`: toolchain installs via the base image's package manager; Devbox requirement removed
- `runtime-toolchain`: `piccolo.yaml` names apk packages
- `release-based-integrations`: source-build path removed

## Impact

- `internal/runtimeimage` — Dockerfile generator, default package list, `Plan` loses `DevboxJSON`/`IntegrationsJSON`/`Integrations`, `ContextTar` ships only the Dockerfile + bundles
- `cmd/piccolo` — `integration build` command removed, source-build plan helpers removed, `setup.go` Devbox wordings
- `internal/manifest`, `internal/integration`, `internal/runtimeplan` — `build` source retained only to produce a clear error
- Docs: AGENTS.md (toolchain-is-image-content decision, 8.3 GB measurement), README image-build section
- Existing workspaces: the image tag changes (different Dockerfile text), so serve/restart reports a stale image and the user rebuilds. Nothing is rebuilt implicitly.
