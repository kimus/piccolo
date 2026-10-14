## Why

Baking the toolchain made every runtime image ~8.3 GB instead of 440 MB, and the warn-and-rebuild policy means each toolchain, integration or base change creates a new tag that nothing ever removes. Measured on one workspace after a handful of legitimate operations: **20 Piccolo runtime tags, 74 GB of images, ~51 GB reclaimable** — plus a build cache at 19.6 GB. Rollback needs old images to stay, so "delete everything else" is not a safe answer, and Piccolo currently offers no answer at all.

## What Changes

- `piccolo image list` — the locally built Piccolo runtime images with size, creation time, and a marker for which one is `container.image`, which is the recorded rollback target, and which are held by an existing container (running or stopped).
- `piccolo image prune` — removes only Piccolo runtime images that are provably unreferenced, after printing what will go and the bytes it reclaims. Requires `--yes`.
- Retention floor, never pruned: the image in use, `container.previous_image`, anything a container references, and the base PicoClaw/launcher images.
- `piccolo doctor` warns when unreferenced runtime images exceed a size threshold and names the prune command — the same shape as every other warning in this project: report, point at the fix, never act on the user's behalf.

## Capabilities

### New Capabilities

- `runtime-image-retention`: inventory and explicit reclamation of locally built runtime images, with a retention floor that protects the running gateway, the rollback target, and any container's image

### Modified Capabilities

(none — accumulation reporting is its own requirement in `runtime-image-retention`; drift
reporting in `runtime-maintenance` is unchanged by it)

## Impact

- `cmd/piccolo/image.go`: `list` and `prune` subcommands
- `internal/docker`: list images by repository prefix, inspect referencing containers, remove an image by id
- `internal/doctor`: unreferenced-image count/size check (warn, never fail)
- README/AGENTS: replace the "run docker images yourself" guidance with the Piccolo commands
