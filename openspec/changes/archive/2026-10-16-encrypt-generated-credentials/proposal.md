## Why

Piccolo writes every model API key and channel token into `picoclaw/config.json` as plaintext. The file is git-ignored and doctor warns about placeholders, but the credential is still readable by anything that can read the workspace directory: backups, disk images, a `git add -A` that missed the ignore rule, or a config pasted into an issue for debugging.

PicoClaw v0.3.1 already supports `enc://<base64>` credentials (AES-256-GCM, HKDF-SHA256 over a passphrase and an SSH private key) and decrypts them at load. A gateway booted with `enc://` in `config.json` was verified running and healthy in this repo's own container, so the format works with the pinned image today. Piccolo just never produces it.

## What Changes

- `piccolo init` and `piccolo setup` offer to encrypt credentials instead of writing them plaintext: the wizard gains an encryption choice, and a credential written to `config.json` is stored as `enc://…`.
- New `piccolo secrets` command group: generate the encryption key, encrypt a single credential for pasting into a config, and rotate the passphrase over an existing workspace.
- The credential's decryption material is delivered at `serve` time through the manifest's existing `env:HOST_VAR` mechanism, so no passphrase is written into the workspace.
- `piccolo doctor` reports the encryption state: whether an `enc://` value present in `config.json` can actually be resolved by the gateway that is running, and whether the host variable the passphrase comes from is set. (Today an unresolvable `enc://` value makes the gateway exit 1 with a config-parse error naming `config.json`.)
- **BREAKING** for workspaces that opt in and then remove the passphrase: the gateway will not start without it. This is upstream's behaviour, not a Piccolo choice — it is verified that a missing or wrong passphrase aborts the whole config load. Workspaces that never opt in are unaffected, and no existing workspace is migrated automatically.

## Capabilities

### New Capabilities
- `credential-encryption`: generating, storing, delivering, and validating `enc://` credentials for a Piccolo workspace — key file location and ownership, the passphrase's route from host to container, what Piccolo writes versus what PicoClaw writes, and how the two failure modes are reported.

### Modified Capabilities
- `setup-ux`: the model and channel credential flows gain an encrypted storage choice, and the "stored plaintext in config.json" help text stops being universally true. Also closes the placeholder-detection gap where an untouched upstream example value (`YOUR_DISCORD_BOT_TOKEN`) currently reads as a usable key.
- `structured-workspace-layout`: the SSH private key file becomes workspace content that must be laid out, permissioned, and git-ignored like the other secret files.

## Impact

- `internal/picoclaw/secrets.go` — gains encryption alongside the read-only sidecar view added in `881fa2c`.
- `internal/credential/` (new) — key generation, HKDF + AES-GCM encrypt/decrypt. Piccolo has **no Go dependency on PicoClaw**, so this either adds `github.com/sipeed/picoclaw` as a module dependency or reimplements the verified scheme; decided in `design.md`.
- `internal/generate/` — `config.json` rendering and the `.gitignore` entry list.
- `cmd/piccolo/init.go`, `cmd/piccolo/setup.go`, `cmd/piccolo/secrets.go` (new), `cmd/piccolo/serve.go` — the credential write paths, key injection into the container spec, and the preflight check.
- `internal/doctor/doctor.go` — a new check next to the secret-sidecar check.
- Not touched: PicoClaw's own `SaveConfig`, which already re-encrypts plaintext `api_keys` when a passphrase is present. Piccolo never writes `picoclaw/.security.yml`.
- Risk to flag: running `picoclaw onboard -E` to do this work is **not** viable — it rewrites `AGENT.md`, `SOUL.md`, `USER.md` and `memory/` into `agents.defaults.workspace`, which for a Piccolo workspace is the user-owned `~/.picoclaw/agents/main`.
