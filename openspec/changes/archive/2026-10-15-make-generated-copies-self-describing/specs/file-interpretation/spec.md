## MODIFIED Requirements

### Requirement: Generated workspaces include a file-handling skill
Piccolo SHALL include file and image interpretation guidance as a generated PicoClaw skill so agents can choose appropriate tools for common file-reading, decoding, extraction, parsing, and inspection tasks. The tools the skill advertises SHALL be the runtime image's own configured command list, never a separately maintained copy of it.

#### Scenario: New workspace contains skill
- **WHEN** `piccolo init` generates a workspace
- **THEN** the workspace contains `skills/file-handling/SKILL.md`
- **AND** generated agent `AGENT.md` frontmatter includes `skills: [file-handling]`
- **AND** the skill guidance distinguishes visual understanding from exact extraction

#### Scenario: Exact extraction prefers deterministic tools
- **WHEN** the guidance covers a task whose exact result matters
- **THEN** it directs the agent to a deterministic tool rather than visual inference

#### Scenario: Advertised tools are the configured commands
- **WHEN** the skill lists the tools available on PATH
- **THEN** the list is derived from the configured runtime toolchain command list
- **AND** adding or removing a configured command changes the skill without editing its prose

#### Scenario: No unprovided tool is advertised
- **WHEN** the skill is rendered for the default toolchain
- **THEN** no command outside the configured list appears in it
- **AND** a command no default package provides is absent from both the configuration and the skill

#### Scenario: Listing is stable
- **WHEN** the skill is rendered twice from the same configuration
- **THEN** the tool list appears in the same order
