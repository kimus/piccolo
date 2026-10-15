## ADDED Requirements

### Requirement: Generated workspaces leave the exec path guard off
Piccolo SHALL generate `agents.defaults.restrict_to_workspace` as an explicit `false`, because PicoClaw defaults the flag to `true` when the key is absent and its exec guard consumes that flag rather than the read access relaxed by `agents.defaults.allow_read_outside_workspace`.

#### Scenario: A fresh workspace can run ordinary commands
- **WHEN** `piccolo init` writes `config.json` with shell execution enabled
- **THEN** `agents.defaults.restrict_to_workspace` is present and `false`

### Requirement: Setup toggles the workspace path guard
Piccolo setup SHALL enable or disable `agents.defaults.restrict_to_workspace` from the Tools screen without hand-editing `config.json`.

#### Scenario: Turn the guard on
- **WHEN** the user enables the workspace path guard
- **THEN** Piccolo writes `true` and states that shell commands naming paths outside an agent workspace will be refused

### Requirement: Doctor reports the exec path guard
Piccolo doctor SHALL warn while `agents.defaults.restrict_to_workspace` is in effect, naming the guard that will refuse commands and the screen that changes it, and SHALL NOT fail on it.

#### Scenario: Guard on with shell execution enabled
- **WHEN** the flag is true, or absent, and `tools.exec.enabled` is true
- **THEN** doctor warns that PicoClaw blocks commands naming paths outside the agent workspace and that `allow_read_outside_workspace` does not relax that exec check
