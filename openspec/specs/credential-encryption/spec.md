# credential-encryption Specification

## Purpose
Let a Piccolo workspace keep model API keys and channel tokens as PicoClaw `enc://`
ciphertext instead of plaintext, and make the consequences of that choice visible.

Scope is deliberate and narrow: Piccolo generates and validates encrypted
credentials and delivers the passphrase from `piccolo.yaml` at `serve`. It
never writes PicoClaw's `picoclaw/.security.yml`, never stores the passphrase in a
workspace file, and never prints a credential value. The threat model is at-rest
exposure — a copied, backed-up, committed, or pasted workspace — not a host or
Docker operator, who can read the container environment regardless.

## Requirements
### Requirement: Credentials SHALL be encryptable at rest
Piccolo SHALL store a model API key or channel token in `config.json` as a PicoClaw `enc://` credential by default, and SHALL encrypt the value as it writes it so that plaintext never lands in the workspace file. `--no-encrypt` opts out.

#### Scenario: Encrypted credential is written
- **WHEN** a credential is stored for a workspace that has opted into encryption
- **THEN** Piccolo writes the value as `enc://` followed by base64 of salt, nonce, and ciphertext
- **AND** the plaintext value does not appear anywhere in `config.json`

#### Scenario: Opting out is available and works
- **WHEN** a workspace runs `piccolo init --no-encrypt`
- **THEN** generated `config.json` contains plaintext credentials
- **AND** no key file and no `secrets` block are created

#### Scenario: Encryption is the default
- **WHEN** `piccolo init` runs with no encryption flag of any kind
- **THEN** the credential is written as `enc://`
- **AND** the key file and the `secrets.passphrase` entry exist

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

### Requirement: The passphrase SHALL be recorded in piccolo.yaml
Piccolo SHALL store the enc:// decryption passphrase in `piccolo.yaml` under `secrets.passphrase` and deliver it to the container from there, so an encrypted workspace needs no host setup to start. Because `piccolo.yaml` is the one workspace file intended to be committed and shared, Piccolo SHALL state plainly that this does not protect a copy of the workspace, and SHALL NOT describe the arrangement as at-rest protection.

#### Scenario: Passphrase recorded and delivered
- **WHEN** `piccolo serve` starts an encrypted workspace
- **THEN** the container receives `PICOCLAW_KEY_PASSPHRASE` with the value from `secrets.passphrase`
- **AND** no host environment setup is needed for the workspace to start

#### Scenario: No setup ritual
- **WHEN** a user runs `piccolo init` and supplies a passphrase
- **THEN** `piccolo serve` starts the encrypted gateway with no further configuration

#### Scenario: The limitation is stated where the choice is made
- **WHEN** a user enables encryption for a workspace, or it is enabled by default
- **THEN** the command states that the passphrase is stored in `piccolo.yaml` and that a copy of the workspace can therefore be decrypted
- **AND** help text, README and doctor use the same framing rather than implying at-rest protection

#### Scenario: A typo cannot be committed silently
- **WHEN** the passphrase is entered at a prompt
- **THEN** it is confirmed by a second entry and a mismatch is refused
- **AND** an empty or whitespace-only value is refused rather than stored

#### Scenario: An existing passphrase is not overwritten by an opt-in
- **WHEN** `piccolo.yaml` already records a passphrase and a later command supplies a different one
- **THEN** Piccolo leaves the recorded value in place and says so
- **AND** does not claim to have rotated anything, since re-encryption under a new passphrase is not performed

### Requirement: An encrypted workspace that cannot decrypt SHALL be reported before startup
PicoClaw aborts its whole configuration load when an `enc://` value cannot be resolved, so the gateway exits and the health gate fails with an error naming `config.json`. Piccolo SHALL detect that state from the workspace alone and report the missing piece.

#### Scenario: Missing key file detected before boot
- **WHEN** `config.json` contains an `enc://` value and the workspace key file is absent or unreadable
- **THEN** `piccolo doctor` warns that the gateway cannot decrypt this configuration, naming the expected path
- **AND** `piccolo serve` refuses to start for the same reason

#### Scenario: Failure is not reported as a broken config file
- **WHEN** `secrets.passphrase` is empty for a workspace whose config.json holds `enc://` values
- **THEN** the message names the missing field or key file rather than reporting `config.json` as malformed

### Requirement: Encryption SHALL compose with the sidecar's precedence
Piccolo SHALL treat an `enc://` value written to a field governed by `picoclaw/.security.yml` as inert and SHALL refuse to report a field it could not genuinely protect as encrypted.

`picoclaw/.security.yml` overrides `config.json` per field, so an `enc://` value written to a governed field is dead in exactly the way a plaintext value is.

#### Scenario: Encrypting a shadowed field in config.json is refused
- **WHEN** a credential write targets a field governed by `picoclaw/.security.yml`
- **THEN** it is declined with the sidecar key named, rather than stored where it will never be read
- **AND** the workspace-wide pass seals the overlay's own value instead, so the plaintext does not survive the run
- **AND** a run that sealed some values but not others reports that, never a blanket success

#### Scenario: The sidecar is sealed, never created
- **WHEN** `piccolo secrets encrypt` runs on a workspace that has a `picoclaw/.security.yml`
- **THEN** plaintext credential values inside it are rewritten as `enc://`, because the overlay overrides `config.json` per field and would otherwise leave the workspace loading plaintext
- **AND** values already `enc://` or `file://`, empty values, and every field PicoClaw does not model as a secure string are left untouched
- **AND** unrelated nodes and comments survive the rewrite, and the file is written with owner-only permissions
- **WHEN** no `picoclaw/.security.yml` exists
- **THEN** Piccolo does not create one, since absence is the good state

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

#### Scenario: Turning encryption off does not rewrite existing ciphertext
- **WHEN** a user disables encryption on a workspace whose credentials are already `enc://`
- **THEN** Piccolo leaves those values encrypted and working
- **AND** states that later credentials would be stored as plaintext, rather than silently decrypting anything

### Requirement: Serve delivers both credential inputs and refuses without them
PicoClaw needs the passphrase AND the exact key file whose bytes feed its derivation, and aborts the config load without either. Piccolo serve SHALL inject both for an encrypted workspace, and SHALL refuse in preflight — before any container is created or any running gateway is parked — when either is missing.

#### Scenario: Both inputs reach the container
- **WHEN** `piccolo serve` starts an encrypted workspace
- **THEN** the container environment includes `PICOCLAW_KEY_PASSPHRASE` from `secrets.passphrase`
- **AND** `PICOCLAW_SSH_KEY_PATH` naming the key's path inside the mounted PicoClaw home

#### Scenario: A host-set value is not overwritten
- **WHEN** `piccolo.yaml` already sets either variable in `container.env`
- **THEN** Piccolo leaves that entry alone

#### Scenario: Preflight refuses an undeliverable credential
- **WHEN** `config.json` holds an `enc://` value and `secrets.passphrase` is empty, or the key file is unreadable
- **THEN** serve exits non-zero before creating a container, without contacting the Docker daemon first
- **AND** the error names the missing field or file rather than reporting `config.json` as malformed
- **AND** a running gateway keeps running and nothing is parked

#### Scenario: A rejected promotion restores service
- **WHEN** an encrypted workspace is promoted to a different image and the candidate cannot decrypt its config
- **THEN** the health gate fails, the candidate is removed, and the previous gateway is restarted
- **AND** serve exits non-zero

#### Scenario: The key is not left to PicoClaw's search
- **WHEN** credential environment is composed
- **THEN** the key path is given explicitly, because PicoClaw's default location is `~/.ssh`, which inside the container is image content that does not survive a recreate

### Requirement: Reporting states the missing input, not a broken config file
Doctor and status SHALL report an encrypted workspace's delivery state, and every message SHALL name the missing field or key file rather than describing `config.json` as malformed. Neither SHALL print a credential value in either form.

#### Scenario: Healthy encrypted workspace
- **WHEN** `config.json` holds `enc://` values, `secrets.passphrase` is set, and the key file is readable
- **THEN** doctor reports a passing line for it, and states that the passphrase sits in a file the workspace commits
- **AND** does not report the ciphertext as a placeholder or empty key

#### Scenario: Passphrase variable unset
- **WHEN** `secrets.passphrase` is empty
- **THEN** doctor warns and names the field, as a warning and never a failure
- **AND** `piccolo status` shows the same gap, because the symptom a user notices is chat failing rather than an image or config error

#### Scenario: Key file gone after encryption
- **WHEN** the workspace key file has been deleted or is unreadable
- **THEN** doctor warns that no credential can be decrypted, and explains the file feeds the key derivation

#### Scenario: A plaintext workspace says nothing
- **WHEN** `config.json` holds no `enc://` value
- **THEN** neither doctor nor status reports a credential-encryption finding

#### Scenario: Values stay out of output
- **WHEN** any credential-encryption line is produced, in any state
- **THEN** no plaintext token, `enc://` payload, or passphrase appears in the message

### Requirement: The passphrase SHALL be recorded before any credential is sealed
Piccolo SHALL write `secrets.passphrase` to `piccolo.yaml` before rewriting credentials as `enc://`, and SHALL stop the operation if that record cannot be written.

#### Scenario: A failed config write leaves a bootable workspace
- **WHEN** the passphrase is recorded and sealing `config.json` then fails
- **THEN** `config.json` keeps its plaintext credentials and the workspace still starts
- **AND** the next successful write seals them

#### Scenario: A failed record stops the sealing
- **WHEN** `piccolo.yaml` cannot be written
- **THEN** no credential is sealed, because ciphertext with no recorded passphrase cannot be opened

#### Scenario: A declined confirmation writes nothing at all
- **WHEN** the user declines the confirmation, or no terminal is available to answer it
- **THEN** `config.json` and `picoclaw/.security.yml` are both unchanged

### Requirement: Closing guidance SHALL not offer reload where it cannot apply
When a session has just given a workspace its passphrase, Piccolo SHALL tell the user that `piccolo restart` is required and that `piccolo reload` will not work, rather than listing both as options.

#### Scenario: Encryption turned on with a running gateway
- **WHEN** the gateway container was started before the workspace had a passphrase
- **THEN** the guidance names `piccolo restart` as required
- **AND** states that reload reports success while the gateway fails to reload its config

#### Scenario: Ordinary config changes are unaffected
- **WHEN** a session changed config without introducing a passphrase
- **THEN** both `piccolo reload` and `piccolo restart` are offered as before

### Requirement: A credential SHALL exist in one place
When `picoclaw/.security.yml` carries a usable value for a field, Piccolo SHALL NOT also keep a value for that field in `config.json`: the overlay replaces it on load, so the second copy is never read while remaining readable on disk.

#### Scenario: Sealing the overlay drops the config.json copy
- **WHEN** `piccolo secrets encrypt` seals a value the overlay also governs
- **THEN** the `config.json` field is removed rather than left as a plaintext duplicate
- **AND** the model or channel entry itself is preserved, since it carries provider, model and non-secret settings
- **AND** the run reports the removal as a removal, not as an encryption, so a reader comparing files afterwards finds the credential in one place

#### Scenario: An unsealed overlay copy never justifies a deletion
- **WHEN** the overlay governs a field but its own value is still plaintext
- **THEN** the `config.json` value is kept, because deleting it would destroy the only usable copy
- **AND** the field is reported as refused with that reason

#### Scenario: An overlay-only credential is not reported as missing
- **WHEN** `config.json` carries no value for a field the overlay supplies
- **THEN** doctor reports the credential as present and names the overlay, rather than warning about a missing or placeholder key

### Requirement: The overlay SHALL be created when it has to hold a credential
Piccolo SHALL create `picoclaw/.security.yml` when a credential must move there and no such file exists, and SHALL NOT create it when there is nothing to move.

#### Scenario: First move creates the file
- **WHEN** an encrypted workspace with no overlay seals its first credential
- **THEN** the overlay is created with the sealed value at the key PicoClaw's merge resolves
- **AND** `config.json` retains the model and channel entries, minus the credential fields

#### Scenario: Nothing to move leaves no new file
- **WHEN** a workspace has no overlay and no credential to move
- **THEN** no `picoclaw/.security.yml` is created

#### Scenario: An unreachable overlay key is dropped
- **WHEN** the overlay holds a credential for a model no longer in `config.json`
- **THEN** that key is removed during the seal pass
- **AND** the removal is reported, because a credential vanishing from the secret file must be visible after the fact

#### Scenario: A missing key file is not a silent downgrade
- **WHEN** a workspace records a passphrase but its key file is absent
- **THEN** a credential write is refused with that reason rather than storing plaintext
- **AND** doctor and `secrets check` report the same state

### Requirement: piccolo.yaml SHALL NOT be readable beyond its owner
Because `piccolo.yaml` carries the enc:// passphrase, Piccolo SHALL create it with owner-only permissions, SHALL tighten a wider existing mode, and SHALL report a passphrase-carrying file that remains group- or world-readable. Tightening SHALL only remove permission bits.

#### Scenario: Created private
- **WHEN** init writes a new piccolo.yaml
- **THEN** its mode grants access to the owner only

#### Scenario: An existing wider mode is tightened
- **WHEN** a workspace created before the rule holds a 0644 piccolo.yaml with a passphrase
- **THEN** doctor warns with the observed mode and the remedy
- **AND** a subsequent `piccolo secrets encrypt` tightens it and says it did

#### Scenario: Read-only stays read-only
- **WHEN** tightening is requested for a file the owner made read-only
- **THEN** group and other bits are removed
- **AND** no write permission the owner had withheld is granted

#### Scenario: No passphrase, no claim to enforce
- **WHEN** piccolo.yaml holds no passphrase
- **THEN** its permissions are not reported as an exposure

### Requirement: credential-bearing files SHALL NOT be readable beyond their owner
Piccolo SHALL create `piccolo.yaml`, `picoclaw/config.json`, its `.bak` backup, and `picoclaw/.security.yml` with owner-only permissions, SHALL tighten a wider existing mode on write, and SHALL report any of them that remains group- or world-readable while holding a passphrase or a usable plaintext credential. The permission rule SHALL exist in one package read by every writer.

#### Scenario: A backup of a plaintext credential is private
- **WHEN** sealing writes a `config.json.bak` that still holds a pre-encryption plaintext key
- **THEN** the backup is created owner-only whatever mode `config.json` had

#### Scenario: A rewritten file is tightened, not merely requested
- **WHEN** Piccolo rewrites an existing `0644` credential-bearing file
- **THEN** its mode ends owner-only, because the write's mode argument would otherwise apply only to files created here

#### Scenario: A read-only file is not made writable
- **WHEN** a credential-bearing file the owner made read-only is rewritten or tightened
- **THEN** group and other bits are removed and no write bit the owner withheld is granted

#### Scenario: An encrypted config.json is not reported
- **WHEN** `picoclaw/config.json` is group-readable but holds no usable plaintext credential
- **THEN** no permission warning is raised for it

#### Scenario: An unfilled example is not a credential
- **WHEN** a readable file holds only an upstream placeholder value
- **THEN** it is not reported as leaking a credential

### Requirement: piccolo.yaml rewrites SHALL preserve comments
Piccolo SHALL render `piccolo.yaml` from its own configuration structure and transplant the replaced file's comments onto that render, matched by location. It SHALL NOT build the file by merging the old document, and SHALL NOT report an error when the old content cannot be parsed — an unparseable predecessor yields the plain render.

#### Scenario: A note on a pinned image survives
- **WHEN** a workspace whose piccolo.yaml carries user comments is rewritten by a Piccolo command
- **THEN** each comment still stands above the field it annotates
- **AND** the field's value reflects the new state rather than the commented one

#### Scenario: Piccolo's own header survives
- **WHEN** the init-written header block is present in the file being replaced
- **THEN** it is present again after the rewrite

#### Scenario: A field Piccolo stops writing takes its comment with it
- **WHEN** the rendered state omits a field the old file carried with a comment
- **THEN** the field is absent
- **AND** it is not re-added merely to host the comment

#### Scenario: Unparseable predecessor does not block the write
- **WHEN** the existing piccolo.yaml cannot be parsed as YAML
- **THEN** the rewrite succeeds with the plain render and valid loadable content
