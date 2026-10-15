## Why

Piccolo generates workspace prose — the `file-handling` skill and the agent files — writes it once, and then treats it as user-owned forever. That ownership rule is right, and it has a consequence nobody addressed: **a fix to a template never reaches an existing workspace**, and there is no way to find out which copies are stale.

Two instances of the same defect in one session:

- The shipped skill told agents to run `piccolo-setup-tools` when a tool was missing. The template was fixed; `.data` still had the old copy. An agent following it calls a command that has not existed since the toolchain was baked into the image.
- The skill's advertised tool list was hand-written prose duplicating `runtimeimage.DefaultCommands`. It agreed today only by luck: `grep FileHandlingSkillMD --include=*_test.go` returned **nothing** — no test, and no shared source, bound the two lists. Both drift directions are silent: an advertised-but-absent tool sends the agent to a command that cannot run, and an installed-but-unlisted tool stays invisible to it.

Also worth naming: the `.data` workspace is untracked scratch, so a fix made there is invisible to review and to CI. Nothing in the repo said that a copy is not the place to fix something.

## What Changes

- **The skill's tool list is generated** from `runtimeimage.DefaultCommands` — one list, no prose copy, drift structurally impossible.
- **New workspace-root record `piccolo.provenance.json`**: per generated file, the digest Piccolo wrote and the template that produced it, plus the Piccolo version. Deterministic (sorted paths), contains no secrets, committed alongside `piccolo.yaml`.
- **`piccolo doctor` classifies copies into two findings that demand opposite actions** and must never be merged: *customised* (the user changed it — leave it alone, a refresh would destroy their work) and *stale* (byte-identical to what Piccolo wrote, but the template has moved on — refresh, and the fix belongs in the named source). Messages name the source, e.g. `internal/generate/templates.go:FileHandlingSkillMD`.
- **Honest ceiling on staleness**: only templates that need no wizard answers can be re-rendered, so currency is claimed for the file-handling skill alone. For agent files Piccolo reports "edited or not" and stays silent about currency rather than guessing from a version string.
- `piccolo setup` refreshes the record when it rewrites generated content, so Piccolo's own write is never later reported as a user edit.

## Capabilities

### New Capabilities

- `generated-artifact-provenance`: a workspace can say which of its copies are hand-edited and which are stale

### Modified Capabilities

- `file-interpretation`: the skill's tool list comes from the runtime image's configured commands
- `workspace-generation`: generation records what it wrote, and a skipped file keeps its existing entry

## Impact

- `internal/generate/provenance.go` (new), `generate.go` (`File.Source`, `Plan.RecordProvenance`), `templates.go` (`FileHandlingToolNames`)
- `internal/doctor/doctor.go`: new check, warnings only — never a failure
- `cmd/piccolo`: `init` records after writing, `setup` records after a rendered rewrite, `newRootCmd` stamps the version
- Existing workspaces: no record yet, so doctor reports that as an ordinary state; the record appears on the next `init` or `piccolo setup` write. Nothing is rewritten implicitly.

## Bug found while using the path this change adds

Running the sync against a real workspace exposed a pre-existing defect: `setup` computed the skill's path as `<workspace>/skills/file-handling/SKILL.md`, which is where it lived **before** the structured-layout restructure. The real file is `<workspace>/picoclaw/skills/…`, mounted into the container as PicoClaw's home. So the sync created a directory nothing reads, left the actual skill stale, and printed "✓ wrote … and 1 workspace file(s)". `TestSync…` asserted the same wrong path, so the suite agreed with the bug. `workspace.Layout.Skills()` now owns the location, and the test pins it in both directions.
