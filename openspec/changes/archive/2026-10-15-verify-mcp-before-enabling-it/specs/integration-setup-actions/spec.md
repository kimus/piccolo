## MODIFIED Requirements

### Requirement: Apply integration-proposed configuration through a fixed allowlist
Piccolo SHALL be the only writer of the PicoClaw configuration file, applying a proposed patch only when every path is inside the fixed allowlist covering that integration's own MCP server entry. Because that allowlist keeps an integration from touching tool toggles, Piccolo SHALL own `tools.mcp.enabled` and SHALL set it only after the registered server completes an MCP `initialize` handshake.

#### Scenario: A verified handshake opens the gate
- **WHEN** Piccolo has written a server entry and that server answers `initialize` with a JSON-RPC result
- **THEN** the same confirmed write sets `tools.mcp.enabled` to true, and the diff names the answering server and the image verified

#### Scenario: A failed handshake registers without enabling
- **WHEN** the server does not answer, is refused, or answers outside MCP's newline-delimited JSON
- **THEN** Piccolo writes the server entry, leaves the toggle untouched, and states the reason

#### Scenario: Verification is impossible
- **WHEN** no container engine is reachable or the entry declares no spawnable stdio command
- **THEN** Piccolo registers, reports the check as unverifiable, and does not enable the tool
