## Context

Generated copies are user-owned and create-only (`generate.Plan.Write` skips existing files unless the caller passes an explicit overwrite set). So the workspace accumulates prose that Piccolo produced at some point in the past and can no longer describe: after a template fix, an existing workspace holds a mix of files the user edited and files that are simply older than the code. Both look identical from the outside — "differs from what the current template renders".

Those two states demand opposite responses. Recommending a refresh for a hand-edited file advertises destroying the user's work. Saying nothing about a stale file leaves the agent following instructions we already know are wrong — which is exactly how `piccolo-setup-tools` survived in `.data` after being fixed in code.

## Goals / Non-Goals

- Goal: distinguish hand-edited from stale, as computed facts, with no interactive prompt
- Goal: point at the code a fix belongs in, from the copy's own record
- Goal: make the skill's tool list single-sourced so the most likely drift cannot recur
- Goal: keep `doctor` read-only — classify and report, never rewrite
- Non-goal: re-deriving wizard answers to re-render agent files
- Non-goal: auto-refreshing anything, or a migration for pre-existing workspaces
- Non-goal: trusting the record as the word of God about content it cannot see (see Risks)

## Decisions

### Two findings, never one

`Classify` returns customised *before* stale. If the bytes on disk do not match the digest Piccolo wrote, the user owns that file and "the template moved on" is not actionable advice — it is a destructive suggestion. Ordering here is the entire product decision, and it is mutation-tested.

### The record stores digests and a source label, nothing derived

```json
{ "version": 1, "piccolo": "0.3.0",
  "files": { "picoclaw/skills/file-handling/SKILL.md":
      { "sha256": "sha256:…", "source": "internal/generate/templates.go:FileHandlingSkillMD" } } }
```

Source labels name **code**. A stale copy's warning is therefore a pointer to where the fix belongs rather than an invitation to edit the copy — which is the behaviour gap that produced this change in the first place.

### Currency is claimed only for input-free templates

`CurrentRender(source)` re-renders the file today and compares digests. Only the file-handling skill qualifies; agent files depend on wizard answers (name, owner, purpose, tool toggles) that are not recoverable, so re-rendering them would require re-running `init`. Consequence: for agent files Piccolo reports "edited or not" and says nothing about currency. Guessing from the recorded version string would warn about files whose templates never changed — the wolf-crying failure mode that makes a check get ignored.

### The record is written after Write, for the files actually written

Recording the plan's intended bytes on a run that *skipped* a file would attribute a hand-edited file to generation and report it pristine. `RecordProvenance(dir, written)` therefore merges: entries for skipped files keep what they had, and Piccolo makes no new claim about content it did not write. Same rule on the `setup` path, and only for the rendered skill — the tool-allowlist edits are merges into a user file, so recording them would label user text as generation output.

### Deterministic and committed

Sorted paths, two-space indent, no timestamps, no secrets. So re-running init in a checkout yields no diff, and a changed byte always shows as one. It lives at the workspace root next to `piccolo.yaml`, deliberately not under `picoclaw/` (that is PicoClaw's home and gets mounted into the container) and not under `integrations/` or `runtime/` (state and caches, gitignored by design).

## Risks / Trade-offs

- **A user who edits a file back to exactly what Piccolo wrote is reported as "as generated".** Harmless: the content agrees, so no action is owed.
- **A hand-edited file never gets a staleness hint.** Deliberate: we will not suggest overwriting user prose. The record still shows it is customised, so the user can compare by hand.
- **The record can be deleted or doctored.** It is advisory, not a trust boundary. Absent record → doctor says "no record" and moves on; corrupt record → one warning, no failure.
- **Only one template is currency-checked today.** The gap is visible rather than hidden: `currentRenderSources` sits next to `CurrentRender`, so adding a `Source*` constant without a renderer is a single-file decision.
- **Stale agent-file drift is still possible** (a purpose template fix never reaches existing workspaces and is undetectable). Accepted as out of scope; if it bites, the fix is to record the inputs used, not to guess.

## Migration Plan

None. Workspaces without a record report that state once and the record appears at the next `init` or `piccolo setup` write. Nothing is rewritten implicitly.

## Open Questions

- Should `piccolo setup` offer to refresh a *stale* file it finds? Not now — doctor naming the remedy is the 90% fix, and offering a rewrite needs a diff preview to be safe.
