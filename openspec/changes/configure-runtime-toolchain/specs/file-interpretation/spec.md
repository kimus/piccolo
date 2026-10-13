## ADDED Requirements

### Requirement: File-handling skill reflects configured strong defaults
Piccolo SHALL update generated file-handling guidance to use the stronger default runtime toolchain directly from PATH.

#### Scenario: PDF workflow uses Poppler helpers
- **WHEN** the generated skill describes PDF handling
- **THEN** it recommends `pdfinfo` for metadata/page count
- **AND** `pdftotext` for text PDFs
- **AND** `pdftoppm` or `pdftocairo` plus `tesseract` for scanned/image PDFs

#### Scenario: Office workflow uses LibreOffice
- **WHEN** the generated skill describes PPTX/DOCX/XLSX or other office files
- **THEN** it recommends `soffice` or `libreoffice` headless conversion when rendered output is needed
- **AND** allows archive/XML inspection when text-only extraction is sufficient

#### Scenario: Image workflow uses ImageMagick
- **WHEN** the generated skill describes image normalization or inspection
- **THEN** it recommends `file` or `identify` for inspection
- **AND** `magick` or `convert` for resizing, format conversion, thresholding, or normalization before OCR/barcode decoding

#### Scenario: OpenCV is not first-line guidance
- **WHEN** the generated skill describes QR/barcode or OCR/image workflows
- **THEN** it prefers deterministic command-line tools such as `zbarimg`, `tesseract`, and ImageMagick
- **AND** it does not instruct agents to import Python `cv2` unless a user explicitly requests OpenCV-style image processing or simpler tools fail

### Requirement: File-handling skill teaches safe path discipline
Piccolo SHALL update generated file-handling guidance so agents avoid PicoClaw exec safety guard failures for common media workflows.

#### Scenario: Uploaded media input path
- **WHEN** an uploaded file is shown with an `[image:/tmp/picoclaw_media/...]` or `[file:/tmp/picoclaw_media/...]` tag
- **THEN** the skill tells the agent it may use that absolute media path as an input path

#### Scenario: Derived outputs stay in workspace
- **WHEN** a workflow creates converted files, rendered pages, OCR outputs, extracted archives, or other intermediates
- **THEN** the skill tells the agent to write those outputs under the current workspace, such as `./work`, `./converted`, or `./ocr`
- **AND** not to use generic `/tmp` output paths or `../` path traversal

#### Scenario: Safety guard failure recovery
- **WHEN** a command is blocked by the exec safety guard for path outside workspace or traversal
- **THEN** the skill tells the agent to retry with media paths only as inputs and workspace-relative directories for outputs
