## ADDED Requirements

### Requirement: Credentials SHALL be encryptable at rest
Piccolo SHALL be able to store a model API key or channel token in `config.json` as a PicoClaw `enc://` credential instead of plaintext, and SHALL encrypt the value as it writes it so that plaintext never lands in the workspace file.

#### Scenario: Encrypted credential is written
- **WHEN** a credential is stored for a workspace that has opted into encryption
- **THEN** Piccolo writes the value as `enc://` followed by base64 of salt, nonce, and ciphertext
- **AND** the plaintext value does not appear anywhere in `config.json`

#### Scenario: Opt-in is explicit
- **WHEN** a workspace has not opted into encryption
- **THEN** Piccolo writes credentials exactly as it does today
- **AND** no key file, manifest field, or container environment variable is created for it

#### Scenario: The default does not change
- **WHEN** `piccolo init` runs without the encryption choice and without `--encrypt-keys`
- **THEN** generated `config.json` contains plaintext credentials as before

### Requirement: The encryption key SHALL live in the mounted PicoClaw home and be named by environment
Because the container's `~/.ssh` is image content and Piccolo never sets `PICOCLAW_HOME`, Piccolo SHALL store the SSH private key under `picoclaw/keys/` in the workspace and SHALL inject `PICOCLAW_SSH_KEY_PATH` naming its container path, which is the key-location rule PicoClaw accepts for a path outside `~/.ssh`.

#### Scenario: Key file placement
- **WHEN** an encryption key is generated for a workspace
- **THEN** the private key is written to `picoclaw/keys/picoclaw_ed25519.key` with owner-only permissions
- **AND** the containing directory is created owner-only

#### Scenario: The container is told where the key is
- **WHEN** `piccolo serve` composes the container specification for an encrypted workspace
- **THEN** the container environment includes `PICOCLAW_SSH_KEY_PATH` set to `/root/.picoclaw/keys/picoclaw_ed25519.key`
- **AND** no additional bind mount is introduced beyond the existing `picoclaw/`, integrations, and media mounts

#### Scenario: The key file is not committed
- **WHEN** a workspace opts into encryption
- **THEN** `.gitignore` covers the key directory alongside the existing credential entries

### Requirement: The passphrase SHALL reach the container from the host environment only
Piccolo SHALL keep the decryption passphrase out of every file it writes. It SHALL name a host environment variable in `piccolo.yaml` and resolve that variable when starting the container.

#### Scenario: Host variable is the source
- **WHEN** `piccolo serve` starts an encrypted workspace
- **THEN** Piccolo resolves the configured host variable and passes its value to the container as `PICOCLAW_KEY_PASSPHRASE`
- **AND** the value is not written to `piccolo.yaml`, `config.json`, or any other workspace file

#### Scenario: Host variable is unset
- **WHEN** the configured host variable is not set
- **THEN** `piccolo serve` fails before creating a container, naming the variable
- **AND** an existing running gateway is not stopped to replace it with one that cannot decrypt its config

#### Scenario: The exposure is stated honestly
- **WHEN** a user opts into encryption
- **THEN** Piccolo states that this protects credentials at rest against copies, backups, commits, and pasted configuration, and does not hide them from someone with host or Docker access

### Requirement: An encrypted workspace that cannot decrypt SHALL be reported before startup
PicoClaw aborts its whole configuration load when an `enc://` value cannot be resolved, so the gateway exits and the health gate fails with an error naming `config.json`. Piccolo SHALL detect that state from the workspace alone and report the missing piece.

#### Scenario: Missing key file detected before boot
- **WHEN** `config.json` contains an `enc://` value and the workspace key file is absent or unreadable
- **THEN** `piccolo doctor` warns that the gateway cannot decrypt this configuration, naming the expected path
- **AND** `piccolo serve` refuses to start for the same reason

#### Scenario: Failure is not reported as a broken config file
- **WHEN** the passphrase host variable is unset for an encrypted workspace
- **THEN** the message names the host variable as the missing thing rather than reporting `config.json` as malformed

### Requirement: Encryption SHALL compose with the sidecar's precedence
Piccolo SHALL treat an `enc://` value written to a field governed by `picoclaw/.security.yml` as inert and SHALL refuse to report a field it could not genuinely protect as encrypted.

`picoclaw/.security.yml` overrides `config.json` per field, so an `enc://` value written to a governed field is dead in exactly the way a plaintext value is.

#### Scenario: Encrypting a shadowed field is refused
- **WHEN** `piccolo secrets encrypt` encounters a credential whose field is governed by `picoclaw/.security.yml`
- **THEN** it declines that field, names the sidecar key that decides it, and continues with the fields it can genuinely protect
- **AND** it does not report the workspace as encrypted when a governed field was skipped

#### Scenario: No sidecar write
- **WHEN** any encryption flow runs
- **THEN** Piccolo does not create, edit, or delete `picoclaw/.security.yml`

### Requirement: The credential format SHALL be verified against the pinned PicoClaw release
Piccolo SHALL verify its credential format against the pinned PicoClaw release in both directions, and that verification SHALL fail the build on any mismatch.

Because Piccolo implements the key derivation and wire format itself rather than linking against PicoClaw, a silent format difference would produce ciphertext the gateway cannot read.

#### Scenario: Compatibility fixture
- **WHEN** the test suite runs
- **THEN** a ciphertext produced by the pinned PicoClaw release's own credential package decrypts successfully through Piccolo's implementation
- **AND** a ciphertext produced by Piccolo decrypts successfully through the pinned upstream implementation

#### Scenario: Drift fails the build
- **WHEN** either direction of the fixture stops matching
- **THEN** the test fails rather than shipping a writer that produces unreadable credentials

### Requirement: Opting in SHALL warn about irreversible loss
Piccolo SHALL show the irreversible-loss warning before it rewrites any credential, and SHALL NOT encrypt anything a user did not explicitly request.

Losing the passphrase makes the credentials unrecoverable, and the pre-encryption backup of `config.json` contains the plaintext.

#### Scenario: Warning precedes the rewrite
- **WHEN** the user opts an existing workspace into encryption
- **THEN** Piccolo warns that a lost passphrase cannot be recovered and that the written backup holds the plaintext values
- **AND** the rewrite happens only after that warning is shown

#### Scenario: Nothing is encrypted without a request
- **WHEN** no encryption command or flag is used
- **THEN** Piccolo does not encrypt anything, does not generate a key, and does not change `piccolo.yaml`

### Requirement: Serve delivers both credential inputs and refuses without them
PicoClaw needs the passphrase AND the exact key file whose bytes feed its derivation, and aborts the config load without either. Piccolo serve SHALL inject both for an encrypted workspace, and SHALL refuse in preflight — before any container is created or any running gateway is parked — when either is missing.

#### Scenario: Both variables reach the container
- **WHEN** `piccolo serve` starts an encrypted workspace
- **THEN** the container environment includes `PICOCLAW_KEY_PASSPHRASE` with the resolved host value
- **AND** `PICOCLAW_SSH_KEY_PATH` naming the key's path inside the mounted PicoClaw home

#### Scenario: A host-set value is not overwritten
- **WHEN** `piccolo.yaml` already sets either variable in `container.env`
- **THEN** Piccolo leaves that entry alone

#### Scenario: Preflight refuses an undeliverable credential
- **WHEN** `config.json` holds an `enc://` value and the named host variable is unset, empty, or the key file is unreadable
- **THEN** serve exits non-zero before creating a container
- **AND** the error names the missing variable or file rather than reporting `config.json` as malformed
- **AND** a running gateway keeps running and nothing is parked

#### Scenario: A rejected promotion restores service
- **WHEN** an encrypted workspace is promoted to a different image and the candidate cannot decrypt its config
- **THEN** the health gate fails, the candidate is removed, and the previous gateway is restarted
- **AND** serve exits non-zero

#### Scenario: The key is not left to PicoClaw's search
- **WHEN** credential environment is composed
- **THEN** the key path is given explicitly, because PicoClaw's default location is `~/.ssh`, which inside the container is image content that does not survive a recreate
