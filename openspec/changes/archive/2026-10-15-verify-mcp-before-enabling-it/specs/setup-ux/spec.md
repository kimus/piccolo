## ADDED Requirements

### Requirement: Setup toggles the MCP tool
Piccolo setup SHALL show and edit `tools.mcp.enabled` from the Tools screen and SHALL verify the handshake before turning it on, because a manual enable can break the gateway as completely as an unverified automatic one.

#### Scenario: The running container is verified too
- **WHEN** the container serving traffic was created from a different image than `piccolo.yaml` names
- **THEN** Piccolo probes both images, and a failure in the running image is reported as a failure

### Requirement: Doctor reports MCP reachability and registration
Piccolo doctor SHALL say whether registered MCP servers can reach the coordinator, SHALL warn when an integration with an `mcp_server_key` was never registered, and SHALL compare the running container's image with the manifest's.

#### Scenario: Nothing installed
- **WHEN** no MCP server entry is registered
- **THEN** doctor reports no MCP finding
