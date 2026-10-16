# generated-artifact-provenance Specification

## Purpose
TBD - created by archiving change make-generated-copies-self-describing. Update Purpose after archive.
## Requirements
### Requirement: Workspace records what generation wrote
Piccolo SHALL record, for each generated workspace file it writes, the digest of the bytes it wrote and the template that produced them, in a deterministic file at the workspace root that contains no secrets.

#### Scenario: Fresh init records its output
- **WHEN** `piccolo init` writes generated files into a new workspace
- **THEN** the record lists each written generated file with its digest and source template
- **AND** the recorded name of the Piccolo version that wrote it

#### Scenario: A skipped file keeps its existing entry
- **WHEN** generation runs against a workspace where a generated file already exists and was left alone
- **THEN** the record's entry for that file is unchanged
- **AND** Piccolo records no digest for bytes it did not write

#### Scenario: Record is byte-stable
- **WHEN** the same workspace is regenerated with the same inputs
- **THEN** the record is byte-identical apart from content that genuinely changed
- **AND** path entries are ordered deterministically

### Requirement: Piccolo distinguishes customised copies from stale ones
Piccolo SHALL report, for recorded generated files, whether the file on disk was edited by the user or is untouched-but-older-than-the-current-template, because those two states demand opposite responses.

#### Scenario: Customised file is not offered as stale
- **WHEN** a recorded file's bytes differ from the digest Piccolo wrote
- **THEN** Piccolo reports it as customised and user-owned
- **AND** it does not recommend refreshing it

#### Scenario: Stale file names its source
- **WHEN** a recorded file's bytes match the digest Piccolo wrote and the current template renders different bytes
- **THEN** Piccolo reports the file as predating the current template
- **AND** the report names the template to change, not the copy
- **AND** the report is a warning, never a failure

#### Scenario: Untouched and current
- **WHEN** a recorded file matches both the record and the current template
- **THEN** Piccolo reports it as generated content with nothing to do

#### Scenario: No record exists
- **WHEN** a workspace predates provenance recording
- **THEN** Piccolo reports the absence of a record as an ordinary state
- **AND** the command still exits successfully

#### Scenario: Recorded file is gone
- **WHEN** a file in the record no longer exists in the workspace
- **THEN** Piccolo warns that the generated file is missing and names it

#### Scenario: Record cannot be read
- **WHEN** the record exists but cannot be parsed
- **THEN** Piccolo warns about the unreadable record
- **AND** it does not treat the workspace as having no record

### Requirement: Currency is claimed only where the template can be re-rendered
Piccolo SHALL report staleness only for generated content it can render today without user input, and SHALL stay silent about the currency of content that depends on wizard answers.

#### Scenario: Input-free template is checked
- **WHEN** the current template for a recorded file can be rendered without user input
- **THEN** Piccolo compares the rendered bytes against the record to decide currency

#### Scenario: Input-dependent template is not judged
- **WHEN** a recorded file's template consumes answers Piccolo did not retain
- **THEN** Piccolo reports only whether the file was edited
- **AND** it makes no claim that the file matches or predates the current template

### Requirement: Generation and refresh write the same record
Piccolo SHALL update the record whenever it itself writes generated content, so a Piccolo-authored rewrite is never later classified as a user edit, and SHALL NOT record content it merged into a user file.

#### Scenario: Setup refresh updates the record
- **WHEN** `piccolo setup` rewrites a rendered generated file
- **THEN** the record's entry for that file matches the bytes just written

#### Scenario: Merged edits are not attributed to generation
- **WHEN** a command patches a user-owned file rather than rendering it from a template
- **THEN** the record gains no entry claiming that file is generation output

### Requirement: Provenance maps each copy back to its source
The record SHALL associate each generated copy with the template that produced it, so a reader can find where a fix belongs without reading the generator.

#### Scenario: Source is discoverable from the record
- **WHEN** a reader asks where a generated file's content comes from
- **THEN** the record names the template path and function for that path
- **AND** a staleness report repeats that source so the fix is applied in code rather than in the copy


### Requirement: The record lives at a fixed name and survives a rename
Piccolo SHALL keep the record at `piccolo.lock` in the workspace root. If the record is renamed again, writing it SHALL adopt the entries stored under the previous name rather than starting empty, and SHALL remove the superseded file.

#### Scenario: Adopted on the first write after a rename
- **WHEN** a workspace has a record under the previous name and none at the current one
- **THEN** the next write carries the old entries into the new record
- **AND** the superseded file is removed, so no two records can disagree

#### Scenario: An unusable predecessor is discarded, not fatal
- **WHEN** the record under the previous name cannot be parsed
- **THEN** it is discarded and generation proceeds
- **AND** the workspace is not left unable to write its record

#### Scenario: Report names what it found
- **WHEN** doctor finds a record only under the previous name
- **THEN** it says so and names that file, rather than reporting that the workspace never had a record

#### Scenario: One definition of the predecessor's name
- **WHEN** the record's previous name is referenced by more than one package
- **THEN** it comes from a single exported constant, not a duplicated string
