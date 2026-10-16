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

### Requirement: Init carries the encryption choice on every path
`piccolo init` SHALL offer the credential-storage choice in the interactive wizard and SHALL accept it non-interactively, and a choice made on either path SHALL reach the generated workspace. The key SHALL be generated only after the plan is confirmed, and the plan SHALL be rebuilt from the encrypted value before anything is written.

#### Scenario: Non-interactive opt-in is not dropped
- **WHEN** `piccolo init --yes --encrypt-keys` runs with the passphrase variable set
- **THEN** `config.json` is written with the credential as `enc://`
- **AND** the plaintext appears in no file in the workspace
- **AND** `piccolo.yaml` records the variable name and `picoclaw/keys/` holds the key

#### Scenario: Declining keeps today's behaviour
- **WHEN** `init` runs without the encryption choice
- **THEN** `config.json` is written as it is today
- **AND** no key directory and no `secrets:` block are created

#### Scenario: Aborted init creates nothing
- **WHEN** the user declines the generation plan
- **THEN** no key file exists and no `piccolo.yaml` secrets block was written

#### Scenario: Opt-in without a reachable passphrase stops early
- **WHEN** encryption is requested but the named host variable is unset and no terminal can supply it
- **THEN** init fails before writing `config.json`
- **AND** the error names the variable to export and offers `piccolo secrets encrypt` as the later route

### Requirement: Setup seals credentials at one choke point
`piccolo setup` SHALL encrypt a credential it is about to write whenever the workspace posture is encrypted, at the single write path every form shares, and SHALL treat that posture as requiring both a key file and a recorded passphrase route.

#### Scenario: Both forms seal through the same path
- **WHEN** a model key or a channel token is submitted in an encrypted workspace
- **THEN** the value written is `enc://`
- **AND** neither form can bypass sealing by taking its own branch

#### Scenario: Missing passphrase refuses the write
- **WHEN** an encrypted workspace cannot resolve its passphrase variable at write time
- **THEN** setup does not write `config.json`
- **AND** the previously stored ciphertext is left intact

#### Scenario: Half-configured posture is not treated as encrypted
- **WHEN** a key file exists but no passphrase route is recorded, or the reverse
- **THEN** setup does not behave as though credentials can be sealed
