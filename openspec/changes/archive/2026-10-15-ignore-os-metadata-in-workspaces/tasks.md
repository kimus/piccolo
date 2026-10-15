## 1. The entry

- [x] 1.1 `GitignoreOSMetadata = ".DS_Store"` as a named constant, so the check and the writer share one value
- [x] 1.2 Added to `EnsureGitignore`'s required entries
- [x] 1.3 Verified the bare pattern covers depth with `git check-ignore -v` (root, `internal/`, deeper path) rather than assuming
- [x] 1.4 Exact-content test assertions updated; new test: append heals a legacy file, keeps the user's own rule, idempotent on a second run

## 2. Visibility

- [x] 2.1 `generate.MissingGitignoreEntries(dir)` — read-only, treats a commented line as absent, an absent file as fully missing
- [x] 2.2 `doctor.checkGitignoreHygiene`, wired into `Run`; several missing rules collapse to one WARN line
- [x] 2.3 Remedy text verified against reality: re-running `init` appended the missing rule and preserved a custom `SOUL.md` edit and a custom ignore line
- [x] 2.4 `Run`-level test added — the first version tested the helper directly and passed even with the wiring deleted, which is the same blind spot as the Dockerfile continuation bug

## 3. Checks

- [x] 3.1 `gofmt`, `go vet`, `go build`, `go test ./...` green
- [x] 3.2 Mutation: deleting the `Run` wiring now fails the suite
- [ ] 3.3 Repository hygiene already committed separately: untrack two `.DS_Store` files, ignore at any depth (`4b2b0f1`)
