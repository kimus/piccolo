## ADDED Requirements

### Requirement: Setup and init offer encrypted credential storage
Piccolo SHALL let a user choose encrypted storage when supplying a model API key or channel token, and SHALL keep plaintext as the default so that an untouched flow is unchanged. The help text on a credential field SHALL describe where the value will actually be stored rather than asserting plaintext unconditionally.

#### Scenario: Init offers the choice
- **WHEN** the init wizard collects a model API key
- **THEN** it offers encrypted storage of the workspace credentials
- **AND** declining leaves the generated `config.json` exactly as it is generated today

#### Scenario: Non-interactive init does not opt in
- **WHEN** `piccolo init` runs non-interactively without `--encrypt-keys`
- **THEN** credentials are written as plaintext
- **AND** no key file or manifest field is created

#### Scenario: Setup stores a key encrypted for an opted-in workspace
- **WHEN** a user enters a credential in a workspace that has opted into encryption
- **THEN** setup writes the `enc://` form rather than plaintext
- **AND** the confirmation names the storage form it wrote

#### Scenario: Help text matches the file that will hold the value
- **WHEN** setup shows a credential field
- **THEN** the field's description says where the value goes
- **AND** when the sidecar governs that field, the description still says the write is inert

### Requirement: Placeholder detection recognises upstream's example values
Piccolo warns when a credential is still an unfilled template. The detection SHALL treat PicoClaw's own example placeholder shape as a placeholder, not as a usable credential.

#### Scenario: An untouched example token warns
- **WHEN** `config.json` carries a value shaped like the upstream examples, such as `YOUR_DISCORD_BOT_TOKEN`
- **THEN** doctor treats the credential as unusable and warns
- **AND** the warning names the field so the user knows what to replace

#### Scenario: A real credential does not warn
- **WHEN** a credential is a normal key value
- **THEN** doctor reports it as usable

#### Scenario: Encrypted values count as usable
- **WHEN** a credential is stored as `enc://`
- **THEN** doctor treats it as usable, because Piccolo cannot resolve it without the passphrase and emptiness is the only claim it can make
