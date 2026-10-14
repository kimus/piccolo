## 1. Reference Model and Manifest

- [x] 1.1 Add integration package-reference parsing for `[provider:]owner/repo[@selector]` with default provider `github`
- [x] 1.2 Remove short alias support from integration add/upgrade/setup/status resolution and return package-ref guidance for alias-like input
- [x] 1.3 Extend manifest integration metadata with package, requested ref, source, channel, selector, resolved version, artifact digest, and package-scoped state path
- [x] 1.4 Add backward-compatible manifest parsing for existing integration metadata and decide legacy `github:` source-build migration behavior
- [x] 1.5 Update workspace layout helpers to compute `integrations/<owner>/<repo>` state paths

## 2. GitHub Release Artifact Resolution

- [x] 2.1 Implement GitHub release resolution for latest non-draft/non-prerelease releases and exact selectors
- [x] 2.2 Define and parse `piccolo.integration.release.v1` release manifest assets
- [x] 2.3 Select Linux runtime platform artifacts from the release manifest
- [x] 2.4 Download artifacts and verify SHA-256 digests before building images
- [x] 2.5 Add actionable errors for missing releases, missing manifests, missing platform artifacts, digest mismatch, and GitHub API rate limits

## 3. Runtime Image Installation

- [x] 3.1 Generate Docker build contexts that copy verified release artifact contents into `/usr/local/bin`
- [x] 3.2 Preserve descriptor validation in one-shot containers after release artifact installation
- [x] 3.3 Ensure add/version-change preserves integration state and does not run setup automatically
- [x] 3.4 Keep existing source-build Docker/Devbox path available behind `piccolo integration build`
- [x] 3.5 Ensure build/install paths preserve launcher base image behavior and structured workspace mounts

## 4. Commands and UX

- [x] 4.1 Change `piccolo integration add <package-ref>` to install or update release package selections idempotently
- [x] 4.2 Add `piccolo integration list` and `piccolo integration list --json`
- [x] 4.3 Redefine `piccolo integration upgrade [package]` to advance latest-channel integrations and leave fixed selectors pinned
- [x] 4.4 Add `piccolo integration build <package-ref>` for explicit source builds with build-dependency warnings
- [x] 4.5 Update setup/status commands to resolve package identities and use package-scoped state paths

## 5. Tests and Documentation

- [x] 5.1 Add unit tests for reference parsing, selector preservation, and alias rejection
- [x] 5.2 Add unit tests for release manifest parsing, platform selection, digest verification, and failure modes
- [x] 5.3 Add integration command tests for add/list/upgrade/build manifest behavior using fake release artifacts
- [x] 5.4 Update README integration docs and examples to package refs only
- [x] 5.5 Run `go test ./...` and `openspec validate add-release-based-integrations --strict`
