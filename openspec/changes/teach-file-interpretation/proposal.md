## Why

Piccolo-generated agents can already inspect uploaded images, but they may stop at visual recognition when a task requires exact extraction. A QR-code image is the clearest example: the agent can say "this is a QR code" while failing to decode the exact URL or text payload.

Users need agents to understand when to use deterministic file-processing tools instead of guessing from vision or prose inspection, especially in Docker-based PicoClaw environments where missing tools can be installed on demand.

## What Changes

- Add a generated PicoClaw `file-handling` skill that teaches common file and image interpretation strategies.
- Add `skills: [file-handling]` to generated agent frontmatter so agents load the skill by default.
- Teach agents to prefer deterministic command-line tools for exact decoding, extraction, metadata, and structured parsing.
- Include common mappings for QR/barcodes, OCR, PDFs, DOCX/office documents, CSV/TSV, JSON/YAML/XML, SVG, archives, media files, SQLite databases, logs, and unknown files.
- Add QR-specific guidance: use `zbarimg --quiet --raw <image-path>`, install `zbar-tools` in the disposable container if missing and exec is available, retry, then return the decoded payload exactly.
- Expose or document the required config shape for remote-chat decoding: `tools.exec.enabled=true`, `tools.exec.allow_remote=true`, `tools.load_image.enabled=true`, and agent `AGENT.md` tool allowlists including `exec` and `load_image`.
- Add generic safety language making clear that remote exec is broad shell access from Discord/Telegram/etc. and should only be enabled for trusted channels/users.

## Capabilities

### New Capabilities
- `file-interpretation`: Agent behavior for selecting appropriate tools and commands when asked to read, decode, extract, parse, or interpret common file types and image contents.

### Modified Capabilities

## Impact

- Generated `AGENT.md` templates and `skills/file-handling/SKILL.md` in `internal/generate`.
- Setup/tool-sync behavior for installing/updating the file-handling skill, adding `skills: [file-handling]`, and editing remote exec through `piccolo setup tools`.
- PicoClaw config modeling/patching for preserving and setting `tools.exec.allow_remote`.
- Doctor/setup/readme guidance for image interpretation, QR decoding, and remote-exec risk.
- Tests covering generated instructions, config preservation, and any new setup or doctor behavior.
