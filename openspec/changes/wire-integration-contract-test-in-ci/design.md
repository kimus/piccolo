## Context

Two measurements shape this.

Building the workspace default image is now a 6m50s / 8.28 GB operation (toolchain layer 5.78 GB). A CI job that builds it on every push would be slower and larger than the code it checks, and would fill the runner disk before finishing.

The contract test itself needs very little: it runs `piccolo-gws state` and each setup action's argv inside a container built from the image, with the integration binaries present. The integration layer is the same whether the toolchain is 16 packages or 1. Measured locally, the test runs in **2.8s** against an already-built image.

```text
docker images piccolo/picoclaw-runtime:d6b345b91e2c
  toolchain layer      5.78 GB
  integration COPY     107 MB
  integration RUN      107 MB
  base + apk + devbox  ~160 MB
```

So the expensive part is not required by the test. What the test genuinely requires is that the image is built by Piccolo's own generator, so its bundle layout, PATH and argv assumptions are the real ones.

## Goals / Non-Goals

**Goals:**
- An argv regression fails CI, not the next live session.
- The CI build stays small and fast enough that nobody is tempted to skip the job.
- The image under test is produced by the same code path a workspace uses.

**Non-Goals:**
- No CI coverage for the full default toolchain (a multi-gigabyte build with no added signal for this test).
- No assertion in CI that the *agent-facing* toolchain works end to end — that is `piccolo runtime status` in a real workspace, and it needs the full image.
- No release-automation change: this is a test job, not a publish step.

## Decisions

### Build the contract image with an explicit minimal toolchain

`piccolo image build` already replaces the package list with repeated `--package`. It has no equivalent for commands, so a build cannot currently be reduced below the 24-command default set — and the toolchain layer verifies every command exists, so packages and commands must be reduced together.

Add `--command` (repeatable) with the same replace-semantics. CI then builds:

```sh
piccolo image build --package jq --command jq
```

which is the base image, the Devbox/Nix bootstrap, one trivial package, and the integration layer with its `gws` closure. Expected order of a few hundred MB, not 8 GB.

Rejected: a hidden `--minimal` preset. Presets drift from what workspaces actually build; the two overrides are the same mechanism a user can reach for, and CI just uses it.

### The job is scoped to the surface it protects

Run it on pushes touching `cmd/piccolo/integration*`, `internal/integration`, `internal/runtimeimage`, and the workflow itself, plus a scheduled run so an upstream `piccolo-gws` release cannot silently break the contract while Piccolo's code is untouched. A test that only fires when its own inputs change is a test people keep enabled.

### Pin the integration under test

The job installs a fixed `piccolo-gws` selector rather than `latest`. Otherwise a release in the other repository turns Piccolo's CI red with no Piccolo change, which is the fastest way to make a job get ignored.

### Keep the local path unchanged

`PICCOLO_CONTRACT_IMAGE` still selects an arbitrary pre-built image, so a developer with the full workspace image can run the test against that instead. CI uses it with the minimal image; humans can use it with anything.

## Risks / Trade-offs

- **The minimal image is not the shipped image.** It proves argv and bundle layout, not that libreoffice exists. Accepted: those are different questions, and only this one is cheap enough to run on every push.
- **CI disk.** A few hundred MB per run on ephemeral runners; if the job is ever pushed toward the full image, the runner disk becomes the failure mode rather than the time limit.
- **Nix bootstrap in CI** downloads from cache.nixos.org; a flaky upstream turns the job red. Mitigated by scoping the trigger and by the fact that the base layers are unchanged between runs.
- **Cross-repo coupling** remains: the test needs a published integration release. Pinned selectors make that explicit rather than accidental.

## Migration Plan

None. The new flag defaults to current behaviour, and the job is additive.

## Open Questions

- Should the scheduled run also verify `piccolo integration add` from a clean workspace, or stay strictly argv?
- Is `jq` the right minimal package, or should the job use a package the integration layer itself depends on so a missing-closure failure is also caught?
