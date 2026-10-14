## 1. Generated Agent Guidance

- [x] 1.1 Add a reusable PicoClaw `file-handling` skill and generated agent frontmatter references.
- [x] 1.2 Include the exact QR/barcode workflow using `zbarimg --quiet --raw <image-path>`, on-demand `zbar-tools` install, retry, exact return, and failure reporting.
- [x] 1.3 Include common mappings for OCR, PDF, DOCX/office documents, CSV/TSV, JSON/YAML/XML, SVG, archives, image metadata, media, SQLite, logs, source code, and unknown files.
- [x] 1.4 Keep generated instructions concise and avoid implying that vision can decode exact hidden payloads.

## 2. Exec and Remote Exec Configuration

- [x] 2.1 Extend PicoClaw config modeling or config patch helpers to preserve and set `tools.exec.allow_remote` without dropping unknown config fields.
- [x] 2.2 Decide and implement whether generated default config emits `allow_remote: false` explicitly or leaves it absent while setup can set it explicitly.
- [x] 2.3 If exposed in setup, add an explicit remote-exec toggle with warning text that it enables broad shell execution from remote chats.
- [x] 2.4 Ensure tool sync adds/removes `exec` in relevant `AGENT.md` allowlists when shell execution is changed.

## 3. Doctor, Docs, and Safety Messaging

- [x] 3.1 Add documentation explaining file interpretation, deterministic extraction, QR decoding, and on-demand package installation in the disposable container.
- [x] 3.2 Document the required Discord/remote-chat configuration: exec enabled, remote exec allowed, load_image enabled, and `exec`/`load_image` in agent allowlists.
- [x] 3.3 Add generic doctor/setup warnings for `tools.exec.allow_remote=true`, making clear remote chats can request shell commands inside the container.
- [x] 3.4 Ensure existing workspaces are not silently rewritten; provide manual or explicit setup/sync opt-in guidance.

## 4. Tests

- [x] 4.1 Add generation tests asserting `AGENT.md` references the file-handling skill and `skills/file-handling/SKILL.md` contains QR-specific `zbarimg` instructions.
- [x] 4.2 Add config/patch tests for preserving and setting `tools.exec.allow_remote` alongside `tools.exec.enabled`.
- [x] 4.3 Add setup tests for any remote-exec toggle and warning behavior.
- [x] 4.4 Add doctor/docs-related tests where applicable for remote-exec warning output.
- [x] 4.5 Run `go test ./...`.
