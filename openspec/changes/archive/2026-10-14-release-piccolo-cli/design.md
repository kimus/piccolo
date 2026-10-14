## Context

Piccolo currently has CI for lint/build/test but no release workflow or end-user installer. Users must clone and run `go build`, which is acceptable during development but not for product use.

This change distributes only the host `piccolo` CLI. PicoClaw, integrations, Devbox/Nix runtime tooling, and provider credentials remain managed by Piccolo at workspace/container runtime.

## Goals / Non-Goals

**Goals:**

- Publish Piccolo CLI binaries to GitHub Releases on bare version tags such as `3.1.0`.
- Support macOS and Linux on amd64 and arm64.
- Publish checksums for every release asset.
- Provide a small GitHub Pages installer script:
  `curl -fsSL https://kimus.github.io/piccolo/install.sh | sh`.
- Support pinned installs through `--version 3.1.0`.

**Non-Goals:**

- No Homebrew package in this change.
- No Windows release requirement in the first version.
- No installation of Docker, PicoClaw, Devbox, Nix, or integration packages by the host CLI installer.
- No automatic self-update command.

## Decisions

### Bare version tags

Release tags and names use bare versions:

```text
3.1.0
```

No `v` prefix is used. The release workflow triggers on semantic-looking bare tags.

### Release assets

Asset names use lowercase OS/architecture fields:

```text
piccolo_3.1.0_darwin_arm64.tar.gz
piccolo_3.1.0_darwin_amd64.tar.gz
piccolo_3.1.0_linux_arm64.tar.gz
piccolo_3.1.0_linux_amd64.tar.gz
checksums.txt
```

Archives contain the `piccolo` binary and lightweight metadata such as README/LICENSE when present.

### Release implementation

GoReleaser is a strong fit because it handles cross-compilation, archives, checksums, changelog generation, and GitHub Release upload. A hand-written workflow is also viable, but GoReleaser reduces custom shell logic.

### Static installer

The static installer detects host OS/architecture, resolves either latest release or requested `--version`, downloads the matching archive plus checksums, verifies SHA-256, and installs `piccolo` to a writable bin directory.

Default install path preference:

```text
$PICCOLO_INSTALL_DIR if set
~/.local/bin if writable/creatable
/usr/local/bin if writable
otherwise fail with guidance
```

### GitHub Pages

Use a minimal static site containing `index.html` and `install.sh`. The site can live under `docs/` and be served by GitHub Pages from `main`, or be deployed by a Pages workflow. The first version should prefer the simplest setup that keeps the install script versioned in the repo.

## Risks / Trade-offs

- **Shell installer trust concerns** → Keep script small, readable, and documented; require checksum verification before installing.
- **GitHub API rate limits** → Exact versions can use direct release URLs; latest resolution may use GitHub API and should show `GITHUB_TOKEN` guidance when rate-limited.
- **PATH confusion** → Installer must print clear guidance if the target directory is not on PATH.
- **Tag format mismatch with Go conventions** → Bare tags are intentional for product UX; this project distributes binaries rather than relying on Go module version tags.
