## Why

This repository had **two tracked `.DS_Store` files and no ignore rule** (fixed in `4b2b0f1`). The same gap is product behaviour: `generate.EnsureGitignore` writes a workspace `.gitignore` covering secrets, PicoClaw runtime files, integration auth, and the runtime cache — but not OS directory metadata. Any macOS user who runs `git add -A` in their workspace commits Finder databases alongside their agent files, and once tracked, an ignore rule does not untrack them.

Worse, the gap is invisible. `EnsureGitignore` is called from exactly one place (`init.go:212`), so a workspace created before an entry became required **never gains it** — there is no heal path on `serve`, `setup`, or `workspace migrate`, and `doctor` never looked at `.gitignore` at all. Adding the entry alone would fix only future workspaces and leave the existing hole unreported.

## What Changes

- `.DS_Store` becomes a required ignore entry. A bare pattern covers every depth with one line, verified with `git check-ignore` at root, `internal/`, and a deeper path.
- `doctor` gains a read-only hygiene check naming missing rules and the remedy, collapsed into one line so a legacy workspace missing five rules produces one warning, not five. Warning only — never a `FAIL`.
- The remedy is real and verified, not assumed: re-running `piccolo init` in an existing workspace **appends missing entries and preserves user files** — checked against a workspace whose `SOUL.md` carried a custom edit, which survived, and whose custom `.gitignore` line survived.

## Capabilities

### Modified Capabilities

- `structured-workspace-layout`: the generated ignore file covers OS metadata, and its absence is detectable

## Impact

- `internal/generate/generate.go`: `GitignoreOSMetadata` constant, entry added to `EnsureGitignore`, new `MissingGitignoreEntries`
- `internal/doctor/doctor.go`: `checkGitignoreHygiene`, wired into `Run`
- Tests: exact-content assertions updated; new heal/idempotence test; a `Run`-level test so the wiring cannot be dropped silently
- No behaviour change for new workspaces beyond one extra line in their `.gitignore`
