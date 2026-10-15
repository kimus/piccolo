## Why

A `model_list` entry is one object, but setup edited it as two: changing an alias
appended a second entry and left the original behind, so `agents.defaults` and every
agent kept pointing at the entry the user had just tried to replace. There was also no
way to set the default text model or delete a model without hand-editing config.json,
and doctor only validated `model_name` — a stale alias in a fallback list or an
`AGENT.md` pin resolved to nothing at runtime.

## What Changes

- Editing an alias renames that entry **in place** (array position and unmodelled keys
  kept) and re-points every reference: `model_name`, `image_model`, both fallback
  lists, `agents.list[].model` and `subagents.model`. Renaming onto an existing alias
  is refused; an orphaned `AGENT.md` pin is reported, not rewritten.
- Setup gains "Set default text model…" and delete, which refuses while the alias is
  load-bearing and names each referencing path.
- Doctor resolves every alias config.json and AGENT.md name, and distinguishes
  `model_name` on a list entry (which PicoClaw ignores) from `model`.

## Capabilities
### Modified Capabilities
- `setup-ux`
