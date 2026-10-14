## Why

The cross-repo contract test (`cmd/piccolo/integration_contract_test.go`, build tag `integration`) is the only thing that proves Piccolo's derived argv is accepted by a real integration binary in a real runtime image. It caught the ordering bug that broke every setup action, and it is mutation-checked. It also runs only when a human remembers to run it: CI executes `make lint`, `go build`, `go test` — no image build, no `-tags integration`.

That gap is now expensive rather than merely untidy. Since the toolchain moved into the image, building it costs **6m50s cold and 8.28 GB on disk**; asking CI to build the workspace default image on every push is a non-starter, which is exactly why nobody has wired the test up.

## What Changes

- `piccolo image build` gains `--command` (repeatable), mirroring the existing `--package` override, so a build can be composed with the smallest toolchain that still exercises the real code path.
- A CI job builds a **minimal** runtime image (`--package jq --command jq`, roughly the base plus the integration bundle's Nix closure — a few hundred MB, not 8 GB) and runs the contract test against it.
- The job is a contract test, not a release gate: it runs on pushes touching the integration/argv surface, so a PR that cannot possibly break it does not pay for a build.

## Capabilities

### New Capabilities

- `integration-contract-testing`: automated cross-repo verification that Piccolo's derived integration argv is accepted by the shipped integration binary inside a real runtime image

### Modified Capabilities

## Impact

- `cmd/piccolo/image.go`: `--command` flag threaded into the build plan
- `.github/workflows/ci.yml`: new `contract` job (Docker available, builds the minimal image, runs `go test -tags integration`)
- `cmd/piccolo/integration_contract_test.go`: reads the image ref from the env it already supports (`PICCOLO_CONTRACT_IMAGE`), plus a clearer skip message when unset
- Documented in AGENTS.md as the standing way to validate argv changes
