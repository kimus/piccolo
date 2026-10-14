## ADDED Requirements

### Requirement: A reported action failure keeps the session open

Piccolo SHALL treat an action that emits a terminal event as having reported an outcome, whatever its exit code, and SHALL return to the menu after rendering that report. Only a Piccolo-side fault SHALL end the session.

#### Scenario: Diagnostic reports a real problem

- **WHEN** an action emits an `error` event and exits non-zero
- **THEN** Piccolo renders the message and remediation and redisplays the menu
- **AND** the command does not end with an action failure error

#### Scenario: Silence plus a bad exit is a crash

- **WHEN** an action exits non-zero without emitting a terminal event
- **THEN** Piccolo says the integration died without reporting
- **AND** it re-reads state, because partial effects cannot be ruled out

#### Scenario: Piccolo ended the attempt

- **WHEN** the user cancels or Piccolo's time budget expires
- **THEN** the message attributes the ending to the cancellation, not to the integration

### Requirement: Earned proposals survive later failures

Piccolo SHALL keep a configuration proposal returned by a completed action available for confirmation even after a later action fails, crashes, or is abandoned, and SHALL apply nothing if the user leaves from the menu.

#### Scenario: Failure after a successful save

- **WHEN** one action proposes a configuration change and a later action reports failure
- **THEN** the proposal is still offered once the user finishes

#### Scenario: Leaving from the menu

- **WHEN** the user aborts at the menu rather than choosing Done
- **THEN** no pending proposal is written and Piccolo says so

### Requirement: Unfinished work is summarised without failing the command

Piccolo SHALL print a closing summary of actions that did not complete and SHALL exit zero when the session itself ran correctly, so severity stays on screen rather than in an exit code a truthful diagnostic would also trigger.

#### Scenario: Mixed outcomes

- **WHEN** a session included a success, a reported failure, and a user cancel
- **THEN** the summary lists only the reported failure
- **AND** the command exits zero

#### Scenario: Nothing outstanding

- **WHEN** every action succeeded or was cancelled by the user
- **THEN** no failure summary is printed

### Requirement: Severity is shown and never colour-only

Piccolo SHALL render action severity with colour when writing to a terminal, following the status in the event data, and SHALL keep the text markers that carry the same meaning without colour.

#### Scenario: Reported failure versus Piccolo fault

- **WHEN** an action reports failure and another crashes without reporting
- **THEN** the first is shown as an integration report and the second as a Piccolo-side fault
- **AND** the two are visually distinguishable

#### Scenario: Colour disabled

- **WHEN** the output is not a terminal, or NO_COLOR is set
- **THEN** every line still carries its textual status marker
