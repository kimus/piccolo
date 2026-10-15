## Why

An installed, authenticated Google Workspace integration exposed no tools: PicoClaw turns
a DECLARED `tools:` key into a strict exact-name allowlist
(pkg/agent/tool_allowlist.go:143-164 → pkg/tools/registry.go:171-184) while MCP tools are
named `mcp_<server>_<tool>` only at runtime (pkg/tools/integration/mcp_tool.go:143-152).
The generated coordinator listed six tools, so every integration tool was filtered out and
the assistant told the user no mailbox was configured.

## What Changes

- generate omits `tools:` for the default agent (nil allowlist = every enabled tool);
  specialists keep their reduced lists.
- setup's AGENT.md sync skips the coordinator, so a sync cannot re-declare one.
- Doctor warns when the default agent declares a list while MCP servers are registered.

## Capabilities
### Modified Capabilities
- `setup-ux`, `file-interpretation`
