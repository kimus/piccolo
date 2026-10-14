## ADDED Requirements

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

Piccolo SHALL fetch integration state facts before rendering a setup menu and SHALL prevent the user from selecting an action whose required facts are unmet, explaining the missing prerequisite instead.

#### Scenario: Required fact is unmet
- **WHEN** an action requires a fact the integration reports as false
- **THEN** Piccolo does not invoke that action
- **AND** Piccolo shows which prerequisite is missing and the actions that could satisfy it

#### Scenario: State fetch fails
- **WHEN** the integration's state call fails or returns an invalid payload
- **THEN** Piccolo reports the failure and does not render a menu from assumed facts

### Requirement: Render integration setup UI in Piccolo

Piccolo SHALL render integration-declared inputs using its own terminal form widgets, mapping a closed set of input types to widgets, and SHALL NOT require an integration to provide its own interactive UI.

#### Scenario: Supported input types render
- **WHEN** an action declares inputs of type `text`, `secret`, `path`, `select`, `multiselect`, or `confirm`
- **THEN** Piccolo renders each with the corresponding form widget in the host terminal

#### Scenario: Unsupported input type
- **WHEN** an action declares an input type Piccolo does not support
- **THEN** Piccolo refuses to run that action and names the unsupported type

#### Scenario: Secrets are masked during entry
- **WHEN** an input is marked `secret` or `sensitive`
- **THEN** Piccolo masks the entry and never echoes the value back in output or logs

### Requirement: Invoke actions by id with answers on stdin

Piccolo SHALL invoke an integration action as `<binary> call <action-id>`, passing the collected answers as a single JSON document on stdin, and SHALL NOT place answer values in command arguments or container environment variables.

#### Scenario: Answers are delivered on stdin
- **WHEN** the user submits a rendered form
- **THEN** Piccolo starts the action process and writes the answers document to its stdin
- **AND** no answer value appears in the process arguments

#### Scenario: Host terminal is not interactive
- **WHEN** Piccolo cannot attach a terminal for form rendering
- **THEN** Piccolo does not start an interactive action and tells the user how to run setup from an interactive shell

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
