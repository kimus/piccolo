## ADDED Requirements

### Requirement: Gateway is always required, web console is optional

The PicoClaw gateway SHALL always run when a workspace is served, independent of any web-console choice. The web console SHALL be an optional add-on that the user enables or disables at setup time. Disabling the web console MUST NOT disable the gateway, `piccolo chat`, or the health endpoint.

#### Scenario: Web console disabled still serves the gateway
- **WHEN** a workspace was set up with the web console disabled and the user runs `piccolo serve`
- **THEN** the gateway container starts and `http://127.0.0.1:<gatewayPort>/health` answers
- **AND** `piccolo chat` and `piccolo chat -m <msg>` work against the running gateway

#### Scenario: Web console enabled serves gateway plus console
- **WHEN** a workspace was set up with the web console enabled and the user runs `piccolo serve`
- **THEN** the gateway is reachable on its health port
- **AND** the web console is reachable at `http://127.0.0.1:18800`

### Requirement: The 18800 binding is the single source of truth

Whether the web console is enabled SHALL be determined solely by the presence of a published `:18800` container-port binding in `piccolo.yaml`. The system SHALL NOT introduce a separate boolean field whose value can disagree with the published ports.

#### Scenario: Presence of the binding enables the console
- **WHEN** `piccolo.yaml` `container.ports` includes a binding whose container port is `18800`
- **THEN** `piccolo serve` treats the web console as enabled and prints its URL

#### Scenario: Absence of the binding disables the console
- **WHEN** `piccolo.yaml` `container.ports` contains no `:18800` binding
- **THEN** `piccolo serve` treats the web console as disabled and does not print a web console URL

### Requirement: No dead published port when the console is disabled

When the web console is disabled, `piccolo serve` SHALL NOT publish `:18800` and the container SHALL NOT run the web dashboard process. A published port with no listener behind it is not acceptable.

#### Scenario: Disabled console publishes only the gateway port
- **WHEN** the web console is disabled and `piccolo serve` composes the container spec
- **THEN** the published ports include the gateway/health port
- **AND** the published ports do not include `18800`

#### Scenario: Disabled console runs the gateway, not the launcher
- **WHEN** the web console is disabled and the container starts
- **THEN** the container runs the plain PicoClaw gateway entrypoint
- **AND** nothing inside the container listens on `18800`

### Requirement: Setup records the web-console choice without colliding with web-search

The setup flow SHALL let the user choose whether the web console is enabled, and SHALL record that choice as the presence or absence of the `:18800` binding in the generated `piccolo.yaml`. The web-console choice SHALL be exposed as a dedicated `--dashboard` flag (and a wizard confirmation), and SHALL be distinct from the web-search-tools setting, which is exposed as `--web-search` (renamed from the ambiguous `--web`). The two flags control unrelated things and MUST remain independent.

#### Scenario: Interactive setup asks about the web console
- **WHEN** a user runs interactive `piccolo init`
- **THEN** the wizard asks whether to expose the optional web console, worded so it is clear the gateway runs regardless

#### Scenario: Non-interactive setup selects the console explicitly
- **WHEN** a user runs `piccolo init --yes --dashboard`
- **THEN** the generated `piccolo.yaml` includes a `127.0.0.1:18800:18800` binding
- **AND** the generated `piccolo.yaml` uses the launcher image

#### Scenario: Web-search flag remains independent
- **WHEN** a user runs `piccolo init --yes --web-search` but omits `--dashboard`
- **THEN** `config.json` `tools.web.enabled` is true
- **AND** `piccolo.yaml` has no `:18800` binding

### Requirement: Status and doctor treat the console as optional

`piccolo status` and `piccolo doctor` SHALL report the web console only when it is enabled, and SHALL NOT warn or fail merely because the console is disabled. `doctor` SHALL flag a published `:18800` binding that does not correspond to a launcher-image container as an inconsistency.

#### Scenario: Status with console disabled
- **WHEN** the web console is disabled and the user runs `piccolo status`
- **THEN** the output reports gateway state and health without a web console line

#### Scenario: Status with console enabled
- **WHEN** the web console is enabled and the user runs `piccolo status`
- **THEN** the output reports the web console URL in addition to gateway state

#### Scenario: Doctor accepts a disabled console
- **WHEN** the web console is disabled and the user runs `piccolo doctor`
- **THEN** doctor reports no failure or warning attributable solely to the absent console
