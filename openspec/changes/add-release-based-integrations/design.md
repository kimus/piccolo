## Context

Current Piccolo integration installs build source packages through Devbox/Nix inside a Docker build layer. This works for development but makes normal upgrades expensive: unpinned refs fetch/build source and may download Rust/Nix toolchains even when the final runtime closure is small.

The desired product model is package-like:

```text
piccolo integration add kimus/piccolo-gws          # latest GitHub release
piccolo integration add kimus/piccolo-gws@3.1.0   # exact release selector
piccolo integration build kimus/piccolo-gws@main  # explicit source build
```

Short aliases are intentionally removed from the supported UX. Integration identity is package-scoped (`provider:owner/repo`) while descriptor metadata (`name`, `displayName`) remains integration-owned display/runtime metadata.

## Goals / Non-Goals

**Goals:**

- Make release artifacts the default integration installation path.
- Preserve provider/repo-owned versioning: the `@selector` is opaque and maps to a GitHub Release tag exactly as provided.
- Use scoped state directories: `integrations/<owner>/<repo>` on the host and `/root/.piccolo/integrations/<owner>/<repo>` in containers.
- Keep descriptor validation as the final truth after installing binaries into the image.
- Keep source builds available via `piccolo integration build`.
- Provide `piccolo integration list` as an offline manifest inventory command.

**Non-Goals:**

- No short integration aliases such as `gws`.
- No global SemVer normalization or forced `v` prefix policy.
- No custom provider-specific setup logic in Piccolo core.
- No automatic setup during add/upgrade/build.
- No signature/provenance enforcement in the first version beyond digest verification.

## Decisions

### Package reference grammar

Use:

```text
[provider:]owner/repo[@selector]
```

Examples:

```text
kimus/piccolo-gws
kimus/piccolo-gws@3.1.0
github:johndoe/piccolo-maker@v2.1.10
```

The default provider is `github`. The selector is opaque. For GitHub release installs it is the release tag. If omitted, Piccolo resolves the latest non-draft, non-prerelease GitHub Release.

### `add` selects release artifacts

`piccolo integration add <package-ref>` means install or update the selected package release while preserving state. If the package is already installed at the selected resolved version and digest, the command is idempotent and reports no changes.

Version changes use `add` again:

```text
piccolo integration add kimus/piccolo-gws@3.2.0
```

This replaces the prior `upgrade --to` model.

### `upgrade` advances floating installs

`piccolo integration upgrade <package>` advances integrations installed from an omitted selector/latest channel. Fixed selector installs remain fixed and receive a helpful message directing users to `add <package@selector>` to change versions.

`piccolo integration upgrade` without arguments may upgrade all latest-channel integrations. If implementation keeps `--all` for compatibility, the no-arg form should become the preferred UX.

### `build` is explicit source-build path

`piccolo integration build <package-ref>` uses the existing Devbox/Nix source-build machinery. It is intended for development, unreleased refs, branches, and SHAs. Output should warn that source builds can download compilers/build dependencies and recommend `add` for normal use.

### Release artifact contract

For GitHub releases, Piccolo downloads a release manifest asset first. The manifest maps platform to downloadable artifact and digest:

```json
{
  "schema": "piccolo.integration.release.v1",
  "package": "github:kimus/piccolo-gws",
  "version": "3.1.0",
  "artifacts": [
    {
      "platform": "linux/amd64",
      "asset": "piccolo-gws_3.1.0_linux_amd64.tar.gz",
      "sha256": "..."
    }
  ]
}
```

Piccolo selects the target Linux platform matching the Docker build target/daemon platform, downloads the artifact, verifies the digest, expands it into the Docker build context, copies its `bin/*` into `/usr/local/bin`, and then runs descriptor validation from the resulting image.

### Manifest metadata

Record requested and resolved identities separately:

```yaml
runtime:
  integrations:
    - package: github:kimus/piccolo-gws
      requested: kimus/piccolo-gws
      source: release
      channel: latest
      selector: latest
      resolved_version: 3.1.0
      artifact_digest: sha256:...
      state_path: integrations/kimus/piccolo-gws
      name: gws
      display_name: Google Workspace MCP Server
      commands: [piccolo-gws]
```

For source builds, use `source: build` and record build ref/resolved revision when available.

### State path ownership

State path is package-scoped, not descriptor-name-scoped:

```text
integrations/<owner>/<repo>
/root/.piccolo/integrations/<owner>/<repo>
```

This prevents collisions between packages and makes setup/status commands deterministic. Descriptor setup/status commands receive this path from Piccolo.

## Risks / Trade-offs

- **GitHub API rate limits** → Use unauthenticated API for first version with clear `GITHUB_TOKEN` remediation when rate-limited.
- **Third-party release layout variance** → Require `piccolo.integration.release.v1` manifest asset so asset names can vary.
- **Breaking existing source-ref semantics** → Either migrate existing `github:` refs to `source: build` metadata or document the breaking change before implementation.
- **Digest verification without signatures** → SHA-256 verifies transfer/integrity but not publisher identity; leave cosign/SLSA/GitHub attestations as future hardening.
- **Platform mismatch** → Only Linux artifacts are valid for runtime images; Piccolo must select `linux/amd64` or `linux/arm64`, not host macOS.

## Migration Plan

1. Introduce new manifest fields while still reading old integration entries.
2. For old entries with `ref: github:...`, treat them as legacy source-build entries or provide an explicit migration warning.
3. Update README examples from alias/source-build refs to package refs.
4. Keep descriptor validation and setup/status behavior compatible after state path migration.
