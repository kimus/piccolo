## ADDED Requirements

### Requirement: Setup edits model aliases without orphaning references
Piccolo setup SHALL treat a model_list entry as one object: editing its alias renames that entry in place, updates every reference to it, and never leaves a second entry behind.

#### Scenario: Rename an entry that is in use
- **WHEN** the user edits a model entry and changes its alias
- **THEN** Piccolo renames that entry in place, keeping its position in `model_list` and any keys Piccolo does not model, and re-points `agents.defaults.model_name`, `image_model`, both fallback lists and every agent's `model` / `subagents.model`, naming each reference it re-points

#### Scenario: Rename onto an alias that already exists
- **WHEN** the new alias matches a different entry
- **THEN** Piccolo refuses and leaves both entries untouched

#### Scenario: An AGENT.md pins the old alias
- **WHEN** a rename orphans a `model:` key in an agent's own frontmatter
- **THEN** Piccolo reports that file instead of editing it, because agent workspace files are user-owned

### Requirement: Setup selects the default text model
Piccolo setup SHALL set `agents.defaults.model_name` — the alias agents inherit — from the configured models, without hand-editing `config.json`.

#### Scenario: The list shows what each entry does
- **WHEN** the models screen renders
- **THEN** each entry is marked with the role it currently holds, and the default and image-model items show their current alias

### Requirement: Setup deletes a model entry
Piccolo setup SHALL remove a model_list entry, and SHALL refuse while an agent still needs it.

#### Scenario: Delete a model still referenced as load-bearing
- **WHEN** the chosen alias is `agents.defaults.model_name`, `image_model`, or any agent's own `model`
- **THEN** Piccolo deletes nothing and reports each referencing path, pointing at the screen that moves the reference

### Requirement: Doctor validates every model reference
Piccolo doctor SHALL verify that every model alias config.json names resolves against `model_list`, and SHALL name the field that holds an unresolved one.

#### Scenario: A workspace carries the field PicoClaw ignores
- **WHEN** an `agents.list` entry sets `model_name` instead of `model`
- **THEN** doctor warns that PicoClaw ignores it and names the field that works, rather than failing on a reference nothing reads
