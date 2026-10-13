## ADDED Requirements

### Requirement: Generated workspaces include a file-handling skill
Piccolo SHALL include file and image interpretation guidance as a generated PicoClaw skill so agents can choose appropriate tools for common file-reading, decoding, extraction, parsing, and inspection tasks.

#### Scenario: New workspace contains skill
- **WHEN** `piccolo init` generates a workspace
- **THEN** the workspace contains `skills/file-handling/SKILL.md`
- **AND** generated agent `AGENT.md` frontmatter includes `skills: [file-handling]`
- **AND** the skill guidance distinguishes visual understanding from exact extraction

#### Scenario: Exact extraction prefers deterministic tools
- **WHEN** the generated skill discusses exact payloads, URLs, identifiers, numbers, tables, metadata, or structured data
- **THEN** it tells the agent to prefer deterministic command-line tools over guessing from vision or prose inspection

### Requirement: Missing file-handling tools are installed on demand
Piccolo SHALL teach generated agents that routine missing decoder, parser, extractor, OCR, metadata, and inspection tools can be installed on demand when shell execution is available.

#### Scenario: Routine file tool missing
- **WHEN** a requested file-handling task requires a deterministic command-line tool
- **AND** the command is missing
- **AND** shell execution is available
- **THEN** the generated file-handling skill tells the agent to install the relevant routine package with the system package manager
- **AND** it tells the agent to retry the task without asking the user first

#### Scenario: Installation requires user input
- **WHEN** shell execution is unavailable, installation fails, the package is unusually invasive or heavy, the operation could alter user data, or the request is ambiguous
- **THEN** the generated file-handling skill tells the agent to report the issue or ask the user for guidance

### Requirement: QR and barcode decoding guidance
Piccolo SHALL teach generated agents to decode QR codes and barcodes using deterministic decoder tooling when shell execution is available.

#### Scenario: QR code request
- **WHEN** the user asks an agent to read, scan, decode, or extract the contents of a QR code from an uploaded image
- **THEN** the generated file-handling skill tells the agent to run `zbarimg --quiet --raw <image-path>` using the uploaded image's actual file path
- **AND** it tells the agent to return the decoded text exactly

#### Scenario: Decoder missing
- **WHEN** the agent needs `zbarimg` and the command is missing
- **THEN** the generated file-handling skill maps the missing command to package `zbar-tools`
- **AND** the generic missing-tool guidance tells the agent to install it and retry when shell execution is available
- **AND** it tells the agent to report installation or decoding failure clearly if the retry cannot succeed

### Requirement: Common file type mapping
Piccolo SHALL provide generated guidance for common file categories and their preferred interpretation tools.

#### Scenario: Common file map is present
- **WHEN** `piccolo init` generates the file-handling skill
- **THEN** the skill includes mappings for QR/barcode images, screenshot OCR, text PDFs, scanned PDFs, DOCX or office documents, CSV/TSV, JSON, YAML, XML, SVG, archives, image metadata, media files, SQLite databases, log files, source code, and unknown files

#### Scenario: Unknown file fallback
- **WHEN** the generated skill discusses unknown file types
- **THEN** it tells the agent to identify the file type first using a deterministic command such as `file <path>`

#### Scenario: Archives are handled cautiously
- **WHEN** the generated skill discusses archive files
- **THEN** it tells the agent to list archive contents before extraction
- **AND** it tells the agent not to blindly extract unsafe or untrusted archive contents

### Requirement: Remote exec configuration is explicit when supported by Piccolo setup
If Piccolo provides a setup flow for remote-chat file decoding, the flow SHALL configure PicoClaw's remote exec setting explicitly and warn about its scope.

#### Scenario: Remote exec is enabled through setup
- **WHEN** a Piccolo setup flow enables shell execution for remote channels
- **THEN** `config.json` contains `tools.exec.enabled` set to `true`
- **AND** `config.json` contains `tools.exec.allow_remote` set to `true`
- **AND** matching agent `AGENT.md` tool allowlists include `exec`

#### Scenario: Remote exec warning
- **WHEN** Piccolo presents or applies a setting that enables `tools.exec.allow_remote`
- **THEN** it warns that remote chats can request shell commands inside the container subject to PicoClaw safety checks
- **AND** it tells the user to enable remote exec only for trusted channels/users
- **AND** generic remote-exec warnings do not mention QR-specific behavior

### Requirement: Existing user-owned files are not silently rewritten
Piccolo SHALL NOT silently overwrite existing user-owned `config.json`, agent instruction files, or skill files to add file interpretation behavior.

#### Scenario: Existing workspace opt-in
- **WHEN** an existing Piccolo workspace lacks the file-handling skill or agent skill reference
- **THEN** Piccolo does not silently modify the workspace during unrelated commands
- **AND** any command that installs or updates the skill requires an explicit setup/sync action or documented manual edit
