## Context

Measured after implementing the baked toolchain, on one workspace:

```text
20  piccolo/picoclaw-runtime tags
73.92 GB images        50.98 GB reclaimable
19.61 GB build cache   13.87 GB reclaimable
17.31 GB containers     5    stopped containers not named piccolo-gateway
```

Each image is ~8.3 GB because the toolchain is image content now. Layers are shared by digest, so the sum of image sizes overstates what a removal actually frees — which is exactly why the tool must measure rather than add up.

Rollback depends on an old image existing, so this cannot be "delete everything not running".

## Goals / Non-Goals

**Goals:**
- Make the disk cost visible in Piccolo's own terms, not by reading `docker images` output.
- Reclaim safely: an explicit command with a retention floor that cannot break the running gateway or rollback.
- Warn when accumulation gets large; never delete silently.

**Non-Goals:**
- No automatic prune on serve/restart/build. Deleting images without being asked is the kind of surprise this project has decided against.
- No build-cache garbage collection (Docker owns that).
- No removal of base PicoClaw images or anything Piccolo did not build.
- No N-deep retention history for integration versions; the rollback slot stays one image.

## Decisions

### Retention floor

Prune keeps, unconditionally:

- the image named by `container.image`
- the image named by `container.previous_image` (rollback target)
- any image referenced by **any** container, running or stopped — a stopped container is a real reference, and removing its image strands it
- the base images (`DefaultBaseImage`, launcher flavor), which other tools may share

Anything else that matches Piccolo's own tag prefix is fair game.

Reason: every one of those protects something a user can still ask Piccolo to do (serve, rollback, inspect a crashed run).

### Referenced means containers, not tags

`docker images` cannot tell you whether an image is in use; the container list can. Prune therefore enumerates containers (all states) and collects their image references first. This also means a hand-started container using a Piccolo tag protects that image — correct, and cheap to honour.

### Report reclaimed bytes as measured, not summed

Layer sharing means removing three 8.3 GB tags may free 8.4 GB, not 24.9 GB. Prune computes the size sum for the *plan*, and reports what the daemon actually reclaimed afterwards, labelling the first as an upper bound.

### Threshold for the warning

Warn when unreferenced Piccolo runtime images exceed **2 images or 10 GB**, whichever comes first. Two tags is the point where a workspace is holding more than "current + rollback", and 10 GB is where an accident becomes a disk-full event. The warning is `WARN`: disk usage is not a broken workspace.

## Risks / Trade-offs

- **Prune can still strand history.** A user who prunes and then wants an older integration version must rebuild it (a download plus a build). Accepted: keeping every tag forever is the failure this change exists to fix.
- **Sizes are approximate.** Shared layers make per-image size overlapping; the plan output says "up to".
- **A protected-by-container image can be ancient.** One forgotten stopped container pins 8.3 GB forever. Mitigated by naming it in the inventory line ("referenced by stopped container X"), so the user can decide.
- **No dedup across workspaces.** Two workspaces with identical content share the tag and the layers already, so this is not a problem — but two workspaces with different toolchains each hold a large image.

## Open Questions

- Should `--keep N` exist, or is the retention floor enough for now?
- Should `piccolo doctor` also surface the 19.6 GB Docker build cache as a hint (informational, pointing at `docker builder prune`)?
