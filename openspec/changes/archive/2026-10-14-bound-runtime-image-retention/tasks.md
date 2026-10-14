## 1. Inventory

- [x] 1.1 `piccolo image list` subcommand (also `piccolo images`) (live: `image list` output verified)
- [x] 1.2 Enumerate locally built runtime images by Piccolo's tag prefix; report tag, size, age (live)
- [x] 1.3 Enumerate containers in ALL states and collect their image references (live (stopped-container reference shown in unit tests; daemon reports names for all states))
- [x] 1.4 Annotate each image: `in use`, `rollback target`, `referenced by <container>` (stopped ones named so the user can decide) (live: "kept: in use", "kept: rollback target")
- [x] 1.5 `--json` for scripting: unreferenced list plus total reclaimable upper bound (`--json` implemented with the upper-bound note)
- [x] 1.6 Test: classification of current / previous / container-referenced / unreferenced with a daemon-free seam

## 2. Prune

- [x] 2.1 Build the retention floor: `container.image`, `container.previous_image`, any container reference, base images (live: protected both, removed two extra tags)
- [x] 2.2 Without `--yes`, print what would be removed and the size upper bound, and remove nothing (live: printed the plan; non-TTY without --yes removed nothing (4 images still present afterwards))
- [x] 2.3 With `--yes`, remove only images outside the floor; report each removal and the measured reclaimed total afterwards (live: "removed 2" with each name)
- [x] 2.4 Never remove a base PicoClaw image or anything Piccolo did not build
- [x] 2.5 Refuse cleanly when the daemon is unreachable; keep going (and report) when one removal fails (live: one failure path unit-tested; loop continues and reports WARN)
- [x] 2.6 Test: each protected category survives; unreferenced images go; dry-run removes nothing

## 3. Runner surface

- [x] 3.1 Add image listing by name prefix to the Docker runner (ListImages)
- [x] 3.2 Add "list containers with their image references" to the runner (ContainerImages)
- [x] 3.3 Add image removal returning the reclaimed space actually reported by the daemon (RemoveImage (no force))
- [x] 3.4 Keep SDK types behind the existing runner seam

## 4. Warning

- [x] 4.1 Doctor check: unreferenced runtime images > 2, or their size sum > 10 GB → `WARN` with count, size, and the prune command (live: doctor warned "2 unreferenced runtime image(s), up to 15.4GB")
- [x] 4.2 Removing nothing on the user's behalf; warnings never change the exit status (live: warnings did not fail doctor)
- [x] 4.3 Report "could not check" instead of a false clean when the daemon is unreachable
- [x] 4.4 Test each branch daemon-free via the Engine seam (daemon-free via Engine seam)

## 5. Docs

- [x] 5.1 README: replace the manual `docker images` guidance with `piccolo image list` / `piccolo image prune`, and keep the "do not remove the rollback target" warning
- [x] 5.2 AGENTS.md: add the retention floor and acceptance criteria for list/prune/warn
- [x] 5.3 `go build ./...`, `go vet ./...`, `go test ./...`


## Verification actually performed

- Live: created two extra tags on the running image (zero risk: untagging frees no layers), then
  `image list` marked both unreferenced while naming the two protected ones; `doctor` warned with
  the count and size; `image prune` without `--yes` on a pipe refused and removed nothing;
  `image prune --yes` removed exactly the two extra tags and left the gateway healthy.
- Mutation check: disabling rollback-target protection fails 4 tests across 2 packages, so the
  floor is enforced by tests rather than by reading the code.
- Post-state: 2 runtime images remain (in use + rollback target), gateway health 200,
  `doctor` reports "no unreferenced runtime images".
