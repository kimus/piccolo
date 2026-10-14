## 1. Inventory

- [ ] 1.1 `piccolo image list` subcommand (also `piccolo images`)
- [ ] 1.2 Enumerate locally built runtime images by Piccolo's tag prefix; report tag, size, age
- [ ] 1.3 Enumerate containers in ALL states and collect their image references
- [ ] 1.4 Annotate each image: `in use`, `rollback target`, `referenced by <container>` (stopped ones named so the user can decide)
- [ ] 1.5 `--json` for scripting: unreferenced list plus total reclaimable upper bound
- [ ] 1.6 Test: classification of current / previous / container-referenced / unreferenced with a daemon-free seam

## 2. Prune

- [ ] 2.1 Build the retention floor: `container.image`, `container.previous_image`, any container reference, base images
- [ ] 2.2 Without `--yes`, print what would be removed and the size upper bound, and remove nothing
- [ ] 2.3 With `--yes`, remove only images outside the floor; report each removal and the measured reclaimed total afterwards
- [ ] 2.4 Never remove a base PicoClaw image or anything Piccolo did not build
- [ ] 2.5 Refuse cleanly when the daemon is unreachable; keep going (and report) when one removal fails
- [ ] 2.6 Test: each protected category survives; unreferenced images go; dry-run removes nothing

## 3. Runner surface

- [ ] 3.1 Add image listing by name prefix to the Docker runner
- [ ] 3.2 Add "list containers with their image references" to the runner
- [ ] 3.3 Add image removal returning the reclaimed space actually reported by the daemon
- [ ] 3.4 Keep SDK types behind the existing runner seam

## 4. Warning

- [ ] 4.1 Doctor check: unreferenced runtime images > 2, or their size sum > 10 GB → `WARN` with count, size, and the prune command
- [ ] 4.2 Removing nothing on the user's behalf; warnings never change the exit status
- [ ] 4.3 Report "could not check" instead of a false clean when the daemon is unreachable
- [ ] 4.4 Test each branch daemon-free via the Engine seam

## 5. Docs

- [ ] 5.1 README: replace the manual `docker images` guidance with `piccolo image list` / `piccolo image prune`, and keep the "do not remove the rollback target" warning
- [ ] 5.2 AGENTS.md: add the retention floor and acceptance criteria for list/prune/warn
- [ ] 5.3 `go build ./...`, `go vet ./...`, `go test ./...`
