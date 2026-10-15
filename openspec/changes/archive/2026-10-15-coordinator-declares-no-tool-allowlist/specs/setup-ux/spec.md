## ADDED Requirements

### Requirement: The generated coordinator declares no tool allowlist
Piccolo SHALL generate the default agent's `AGENT.md` without a `tools:` frontmatter key, and SHALL NOT add one during an AGENT.md allowlist sync, because PicoClaw reads a declared key as a strict exact-name allowlist that cannot contain the runtime-named `mcp_<server>_<tool>` entries an installed integration provides.

#### Scenario: Coordinator frontmatter after init
- **WHEN** `piccolo init` writes the default agent's `AGENT.md`
- **THEN** the frontmatter carries no `tools:` key and specialists keep their reduced `tools:` lists

#### Scenario: Syncing enabled tools
- **WHEN** `piccolo setup` syncs enabled tools into agent allowlists
- **THEN** the default agent's frontmatter is not edited
