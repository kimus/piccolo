## Why

Installing a second release integration surfaced this in a live build: a bundle
packaged on macOS carries an AppleDouble sidecar (`._piccolo-math`) beside every
real binary, and Piccolo derived the bundle's command list from a raw `bin/`
listing. The result was an image layer that installed a resource fork into
`/usr/local/bin` and then **verified** it — the build died with exit 127 on a
file that is not a program. Even when verification passes, the garbage ships.

## What Changes

- Command detection ignores packaging residue (`._*`, `.DS_Store`, `Thumbs.db`).
- The bundle install loop in the generated Dockerfile skips the same names, so a
  sidecar can neither become a "command" nor be copied onto PATH.
- A fixture archive that contains sidecars is now covered by tests on both sides.

## Capabilities

### New Capabilities

### Modified Capabilities

- `release-based-integrations`: the recorded command set for an installed release
  is the set of programs the artifact provides, excluding packaging metadata

## Impact

- `cmd/piccolo/integration_release.go` (`extractIntegrationArchive`, `isBundleJunk`)
- `internal/runtimeimage/runtimeimage.go` (`renderBundleInstallBlock`)
- Tests in both packages; no manifest or wire-format change
