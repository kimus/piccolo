## MODIFIED Requirements

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
