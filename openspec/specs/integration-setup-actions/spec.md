# integration-setup-actions Specification

## Purpose
TBD - created by archiving change add-declarative-integration-setup. Update Purpose after archive.
## Requirements
### Requirement: Discover integration setup actions from the descriptor

Piccolo SHALL read an optional list of setup actions from the integration descriptor, each with a stable opaque id, a role, a human title, and optional typed inputs, and SHALL dispatch on the id without interpreting it.

#### Scenario: Descriptor declares actions
- **WHEN** Piccolo reads a descriptor containing `actions`
- **THEN** Piccolo records each action's id, role, title, inputs, and required state facts
- **AND** an unknown role is ignored rather than treated as a descriptor error

#### Scenario: Descriptor declares no actions
- **WHEN** Piccolo reads a descriptor without `actions`
- **THEN** Piccolo runs the integration's legacy declared setup command unchanged
- **AND** no declarative path is attempted

#### Scenario: Unknown descriptor fields are tolerated
- **WHEN** a descriptor contains fields Piccolo does not know
- **THEN** Piccolo parses the descriptor successfully and ignores the unknown fields
- **AND** the descriptor schema identifier is unchanged from the version already in use

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

### Requirement: Consume streaming action events

Piccolo SHALL parse newline-delimited JSON events from an action's stdout while the action runs, SHALL render step and message events as progress, and SHALL treat the action's stderr as human-readable log output passed through to the user.

#### Scenario: Progress events render in order
- **WHEN** an action emits multiple step events
- **THEN** Piccolo displays each step's status and message as it arrives, before the action exits

#### Scenario: Malformed event line
- **WHEN** an action emits a stdout line that is not a valid event object
- **THEN** Piccolo surfaces the line as log output without aborting the action

#### Scenario: Action reports failure
- **WHEN** an action emits an error event or exits non-zero without a result event
- **THEN** Piccolo shows the error code, message, and any remediation text or link
- **AND** Piccolo does not apply any configuration change

### Requirement: Surface integration links for user action

Piccolo SHALL render `open_url` events with their label and URL, and SHALL treat the URL as opaque data supplied by the integration.

#### Scenario: Link is shown for a required user step
- **WHEN** an action emits an `open_url` event
- **THEN** Piccolo prints the label and the URL so the user can open it

#### Scenario: Headless or remote session
- **WHEN** Piccolo detects it is not attached to a local graphical session
- **THEN** Piccolo prints the URL rather than attempting to launch a browser

### Requirement: Courier browser callbacks into the container

When an action emits a `wait_redirect` event, Piccolo SHALL listen on the loopback port derived from the event URL's redirect target, capture exactly one incoming request, and forward the raw request path and query into the still-running action container's own loopback.

#### Scenario: Callback is delivered
- **WHEN** the user completes authorization in a browser and the redirect reaches Piccolo's listener
- **THEN** Piccolo forwards the raw request line into the action container without interpreting or persisting the credential material
- **AND** Piccolo closes the listener after that single request

#### Scenario: Port derives only from the event URL
- **WHEN** an action requests a callback
- **THEN** Piccolo binds only a loopback address and only the port found in the event URL's redirect target
- **AND** Piccolo ignores any separately supplied port or non-loopback address

#### Scenario: Port is unavailable
- **WHEN** the derived host port cannot be bound
- **THEN** Piccolo reports the conflict and offers to retry the action rather than choosing a different port silently

#### Scenario: Callback never arrives
- **WHEN** no request reaches the listener within the action's wait window
- **THEN** Piccolo reports that authorization did not complete, closes the listener, and leaves the action's own failure reporting intact

### Requirement: Broker host files into integration state

Piccolo SHALL support a `path` input by letting the user select a file on the host, copying it to a destination the action declared relative to the integration state directory with owner-only permissions, and passing the container-side path to the action.

#### Scenario: Selected file is staged
- **WHEN** the user selects a host file for a `path` input
- **THEN** Piccolo copies it to the declared destination under the integration state directory with mode `0600`
- **AND** Piccolo passes the container-side path, not the host path, to the action

#### Scenario: Overwriting existing state
- **WHEN** the destination file already exists
- **THEN** Piccolo asks for confirmation before replacing it

#### Scenario: Source file is unusable
- **WHEN** the selected path is missing, is a directory, or cannot be read
- **THEN** Piccolo rejects it with an explanation and does not stage anything

### Requirement: Apply integration-proposed configuration through a fixed allowlist

Piccolo SHALL be the only writer of the PicoClaw configuration file. An action may propose a configuration patch in its result, and Piccolo SHALL apply it only if every affected path is inside a fixed, code-defined allowlist covering the integration's own MCP server entry, using the existing diff preview, confirmation, backup, and atomic write behavior.

#### Scenario: Allowed patch is applied through the normal path
- **WHEN** a successful result contains a patch touching only the integration's own MCP server entry
- **THEN** Piccolo shows a diff, asks for confirmation, writes the previous file to the documented single backup name, and writes the new configuration atomically

#### Scenario: Out-of-allowlist patch is refused
- **WHEN** a proposed patch would modify any other configuration path
- **THEN** Piccolo refuses the patch, states which paths were rejected, and leaves the configuration file untouched

#### Scenario: No patch proposed
- **WHEN** a successful result contains no configuration patch
- **THEN** Piccolo does not modify the configuration file

#### Scenario: Integration state files stay integration-owned
- **WHEN** an action writes its own credentials or provider state under the integration state directory
- **THEN** Piccolo does not read, parse, or modify those files

### Requirement: Show integration links in status remediation

Piccolo SHALL accept an optional label and URL alongside an existing textual remediation on a status check and SHALL render the link when present.

#### Scenario: Remediation includes a link
- **WHEN** a failing status check supplies a remediation label and URL
- **THEN** Piccolo renders both the remediation text and the link

#### Scenario: Remediation is text only
- **WHEN** a status check supplies only textual remediation
- **THEN** Piccolo renders it exactly as before

### Requirement: Preserve one-shot guarantees during interactive actions

Piccolo SHALL keep an action's one-shot container named deterministically, removed on interrupt, and reclaimed when abandoned by an earlier run, for the full duration of an interactive or long-waiting action.

#### Scenario: User interrupts a waiting action
- **WHEN** the user sends SIGINT while an action awaits a browser callback
- **THEN** Piccolo stops and removes the action container and restores the host terminal state

#### Scenario: Abandoned action container exists
- **WHEN** a previous action container for the same integration is still present
- **THEN** Piccolo removes it before starting the new action and reports what it removed

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

### Requirement: The closing report reflects the latest attempt

Piccolo SHALL record one outcome per action, replaced by each new attempt, so that a successful retry removes an earlier failure from the closing summary.

#### Scenario: Failure then success

- **WHEN** an action reports failure and the user runs it again successfully
- **THEN** the closing summary does not list that action
- **AND** the command still lists any other action left unfinished

#### Scenario: Success then failure

- **WHEN** an action succeeds and a later attempt at the same action reports failure
- **THEN** the summary lists the action as unfinished, once

