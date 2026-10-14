## MODIFIED Requirements

### Requirement: Gate menu entries on integration state

Piccolo SHALL fetch integration state facts before rendering a setup menu and SHALL prevent the user from selecting an action whose required facts are unmet, explaining the missing prerequisite instead. State facts SHALL gate which **actions** are selectable and SHALL never remove an action's **inputs** from the rendered form: an action that declares a field shows that field.

#### Scenario: Required fact is unmet

- **WHEN** an action requires a fact the integration reports as false
- **THEN** Piccolo does not invoke that action
- **AND** Piccolo shows which prerequisite is missing and the actions that could satisfy it

#### Scenario: State fetch fails

- **WHEN** the integration's state call fails or returns an invalid payload
- **THEN** Piccolo reports the failure and does not render a menu from assumed facts

#### Scenario: An input's own subject fact is already satisfied

- **WHEN** an action declares an input gated on a fact that is currently true, and that action is the only way to set the fact
- **THEN** Piccolo still renders the input, so the value can be reviewed and replaced
- **AND** selecting the action can never produce a form with fewer fields than declared

### Requirement: Render integration setup UI in Piccolo

Piccolo SHALL render integration-declared inputs using its own terminal form widgets, mapping a closed set of input types to widgets, and SHALL NOT require an integration to provide its own interactive UI. Declared inputs SHALL be presented with the value the integration currently holds wherever that value may be transmitted, so that leaving a field untouched and emptying it are distinguishable.

#### Scenario: Supported input types render

- **WHEN** an action declares inputs of type `text`, `secret`, `path`, `select`, `multiselect`, or `confirm`
- **THEN** Piccolo renders each with the corresponding form widget in the host terminal

#### Scenario: Unsupported input type

- **WHEN** an action declares an input type Piccolo does not support
- **THEN** Piccolo refuses to run that action and names the unsupported type

#### Scenario: Secrets are masked during entry

- **WHEN** an input is marked `secret` or `sensitive`
- **THEN** Piccolo masks the entry and never echoes the value back in output or logs

#### Scenario: A stored non-secret value is shown in its field

- **WHEN** integration state reports a current value for a non-sensitive input
- **THEN** Piccolo pre-fills that field with the stored value
- **AND** submitting without editing resends the same value

#### Scenario: A stored secret is shown as a state, not a value

- **WHEN** a sensitive input already has a stored value
- **THEN** Piccolo renders the field empty and states through the field description that a value is already set and that submitting untouched keeps it
- **AND** no secret value is received from the integration or written to the terminal

### Requirement: Invoke actions by id with answers on stdin

Piccolo SHALL invoke an integration action as `<binary> call <action-id>`, passing the collected answers as a single JSON document on stdin, and SHALL NOT place answer values in command arguments or container environment variables. The answers document SHALL distinguish a value to set, a value to clear, and a sensitive value deliberately left untouched.

#### Scenario: Answers are delivered on stdin

- **WHEN** the user submits a rendered form
- **THEN** Piccolo starts the action process and writes the answers document to its stdin
- **AND** no answer value appears in the process arguments

#### Scenario: Host terminal is not interactive

- **WHEN** Piccolo cannot attach a terminal for form rendering
- **THEN** Piccolo does not start an interactive action and tells the user how to run setup from an interactive shell

#### Scenario: An emptied optional field is sent as a clear

- **WHEN** the user deletes a pre-filled value from an optional non-sensitive input and submits
- **THEN** the answers document carries that key with an empty value, so the integration can unset it

#### Scenario: An untouched secret field is omitted

- **WHEN** the user submits a sensitive input without typing into it
- **THEN** the answers document omits that key, which the integration reads as keep-existing
- **AND** the stored secret is neither fetched nor resent

## ADDED Requirements

### Requirement: Required inputs cannot be skipped

Piccolo SHALL treat an input declared `required` as blocking: the user cannot advance to the next field or submit the form while it is empty. Absent that declaration, Piccolo SHALL treat the input as optional, so an older integration that declares no requiredness keeps its current behaviour.

#### Scenario: Advance blocked on an empty required field

- **WHEN** the user attempts to move on from a required input that is empty
- **THEN** Piccolo keeps the user on that field with a stated reason
- **AND** the action is not invoked

#### Scenario: Submit blocked while any required field is empty

- **WHEN** the form contains a required input that is empty at submit time
- **THEN** Piccolo does not start the action

#### Scenario: Integration still validates independently

- **WHEN** an action receives answers missing a value it needs
- **THEN** Piccolo surfaces the integration's error rather than assuming its own rendering prevented it
- **AND** the reported code and fields are shown as the integration provided them

### Requirement: Backing out of a form returns to the menu

Piccolo SHALL treat the user aborting a rendered form as a return to the menu: no action container is started, nothing is saved for that action, and the setup loop continues.

#### Scenario: Escape from a form

- **WHEN** the user aborts the form before submitting
- **THEN** Piccolo redisplays the menu with freshly-read state
- **AND** no answer values from the abandoned form are sent

#### Scenario: A patch earned by a completed action survives a later abort

- **WHEN** an action completed and proposed a configuration change, and the user then aborts a subsequent form
- **THEN** the earlier proposal remains pending and is still offered for confirmation at the end of the loop

#### Scenario: Aborting the menu itself still exits

- **WHEN** the user aborts from the menu rather than from a form
- **THEN** Piccolo exits without applying a pending proposal

### Requirement: Integration state carries current field values

Piccolo SHALL read current field values from the integration's state output alongside its facts, SHALL ignore values for fields it did not declare, and SHALL never treat a state value as a secret to display.

#### Scenario: Values arrive with facts

- **WHEN** integration state reports current values for declared inputs
- **THEN** Piccolo uses them to pre-fill the rendered form

#### Scenario: Older integration reports no values

- **WHEN** state carries only facts, as with a bundle released before this change
- **THEN** Piccolo renders the declared inputs unfilled and the flow still completes
- **AND** omitting an unknown value never causes an error

#### Scenario: Unknown keys remain tolerable

- **WHEN** an integration's state output includes fields Piccolo does not know
- **THEN** Piccolo parses the payload successfully, so no state schema version bump is required
