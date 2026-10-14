# setup-ux Specification

## Purpose
TBD - created by archiving change configure-runtime-toolchain. Update Purpose after archive.
## Requirements
### Requirement: Setup toggles tool feedback
Piccolo setup SHALL allow users to enable or disable PicoClaw `agents.defaults.tool_feedback` without manually editing `config.json`.

#### Scenario: Enable tool feedback
- **WHEN** the user enables tool feedback through setup
- **THEN** Piccolo writes `agents.defaults.tool_feedback.enabled: true` to `config.json`
- **AND** preserves or sets a sensible `max_args_length`

#### Scenario: Disable tool feedback
- **WHEN** the user disables tool feedback through setup
- **THEN** Piccolo writes `agents.defaults.tool_feedback.enabled: false` or removes the setting according to the chosen config convention
- **AND** does not alter unrelated agent defaults

#### Scenario: Remote exec recommends feedback
- **WHEN** remote exec is enabled through setup
- **THEN** setup recommends enabling tool feedback for audit/debug visibility

### Requirement: Setup toggles web console consistently
Piccolo setup SHALL allow users to enable or disable the optional web console by updating container image/port settings consistently.

#### Scenario: Enable web console
- **WHEN** the user enables the web console through setup
- **THEN** Piccolo configures a launcher-compatible image or image flavor
- **AND** ensures a `127.0.0.1:18800:18800` port binding is present

#### Scenario: Disable web console
- **WHEN** the user disables the web console through setup
- **THEN** Piccolo removes the web console port binding
- **AND** configures a gateway-compatible image or image flavor

#### Scenario: Custom runtime image flavor is preserved
- **WHEN** a workspace uses a Piccolo runtime image
- **AND** the user toggles the web console
- **THEN** setup chooses or requests the matching gateway/launcher runtime image flavor rather than silently losing runtime toolchain support

### Requirement: Setup uses ESC navigation
Piccolo SHALL implement and advertise ESC as the local navigation key on every interactive screen, including integration setup, not only on `piccolo setup`.

#### Scenario: ESC quits home
- **WHEN** the setup home menu is focused
- **AND** the user presses ESC
- **THEN** setup exits cleanly without applying unrelated changes

#### Scenario: ESC goes back from submenu
- **WHEN** a setup submenu or form is focused
- **AND** the user presses ESC
- **THEN** setup cancels that screen and returns to the previous menu

#### Scenario: Help text matches behavior
- **WHEN** setup renders home or submenu help text
- **THEN** it describes ESC quit/back behavior
- **AND** it does not advertise `q` quit/back unless `q` is actually implemented

#### Scenario: ESC leaves an integration action form
- **WHEN** an integration action is collecting inputs
- **AND** the user presses ESC
- **THEN** that action saves nothing and the menu is redisplayed
- **AND** a configuration proposal earned by an earlier action is not lost

#### Scenario: ESC at the integration menu quits
- **WHEN** the integration action menu is focused
- **AND** the user presses ESC
- **THEN** the session ends without an error and nothing is written

#### Scenario: Declining a confirmation by escaping
- **WHEN** a yes/no gate is focused and the user presses ESC
- **THEN** it is treated as choosing the negative answer
- **AND** it is not reported as a failure

#### Scenario: Navigation keys stay available
- **WHEN** a form is focused
- **THEN** enter still submits and advances a field
- **AND** tab still advances, so the abort key does not consume the keys fields need

