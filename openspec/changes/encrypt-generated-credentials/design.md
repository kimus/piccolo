## Context

Piccolo writes credentials as plaintext into `picoclaw/config.json`. PicoClaw v0.3.1 can decrypt `enc://<base64>` values at load, and a gateway booted with such a value was verified running and healthy against the pinned image.

Verified constraints that shape every decision below:

- **The scheme needs two inputs, not one.** `deriveKey` uses `HKDF-SHA256(ikm = HMAC-SHA256(key = SHA256(ssh_key_bytes), msg = passphrase), salt, "picoclaw-credential-v1", 32)`. Both the passphrase and the SSH private key are secrets; neither alone is sufficient.
- **Both failures are boot failures.** Missing passphrase → `credential: enc:// passphrase required`; wrong passphrase or key → `decryption failed … message authentication failed`. Each aborts `LoadConfig`, so the container exits 1 and the health gate never opens.
- **`~/.ssh` is not available to the container.** `DefaultSSHKeyPath()` returns `~/.ssh/picoclaw_ed25519.key`, and the image's `HOME=/root`. Piccolo deliberately never sets `PICOCLAW_HOME`, and the only mounts are `picoclaw/`, `integrations/`, and media — so `/root/.ssh` is image content, and a key generated there vanishes with the container.
- **`allowedSSHKeyPath` is an allowlist, not a heuristic.** A path passes only if it equals `$PICOCLAW_SSH_KEY_PATH`, is inside `$PICOCLAW_HOME`, or is inside `~/.ssh`. Since Piccolo controls the container env, `PICOCLAW_SSH_KEY_PATH` is the one branch that works without changing the mounting rules.
- **Piccolo has no Go dependency on PicoClaw today.** `pkg/credential`'s only non-stdlib import is `golang.org/x/crypto/ssh`; Go 1.26 provides `crypto/hkdf` in the standard library.
- **`picoclaw onboard -E` cannot be used.** It calls `createWorkspaceTemplates`, which unconditionally writes `AGENT.md`, `SOUL.md`, `USER.md` and `memory/` into `agents.defaults.workspace` — the user-owned `~/.picoclaw/agents/main`. That breaks the create-only rule for agent files.
- **`.security.yml` still governs.** The preceding commits (`881fa2c`, `e0a0581`) established that the sidecar overrides `config.json` per field. Encryption has to compose with that, not reintroduce a second source of truth.

## Goals / Non-Goals

**Goals**
- A new workspace can store every credential it writes as `enc://`, with the plaintext never landing on disk.
- The passphrase is never written into the workspace by Piccolo.
- A workspace that cannot decrypt its own config is reported before `serve`, in the file and with the remedy — not as a config-parse error naming the wrong file.
- Workspaces that never opt in are byte-for-byte unaffected.

**Non-Goals**
- No protection against a host operator who has both the workspace and the container env. This is at-rest encryption, and the threat model says so in user-facing text.
- No migration of an existing `config.json`. A workspace opts in by an explicit command.
- No `file://` support, no external secret manager, no per-field encryption choice.
- No writing, editing, or deleting of `picoclaw/.security.yml`.
- No change to the plaintext default.

## Decisions

### D1: Piccolo implements the scheme instead of importing PicoClaw

Add `internal/credential/` implementing HKDF-SHA256 + AES-256-GCM with the exact wire format: `enc://base64(salt[16] || nonce[12] || ciphertext)`, info `"picoclaw-credential-v1"`, and `ikm = HMAC-SHA256(SHA256(sshKeyBytes), passphrase)`.

*Alternative considered:* depend on `github.com/sipeed/picoclaw/pkg/credential` and call it directly. Rejected — the whole module and its `pkg/config` tree would enter Piccolo's graph for one function, and Piccolo's position has been that it does not link against PicoClaw. `x/crypto/ssh` is already adjacent to Piccolo's existing `x/term` dependency, so this adds one indirect module.

*The cost is real and accepted:* a wire-format drift in a future PicoClaw silently produces ciphertext Piccolo can write but the gateway cannot read. Mitigation is a compatibility test, not a guess — D8.

### D2: The SSH key lives in the mounted `picoclaw/` dir and is reached through `PICOCLAW_SSH_KEY_PATH`

Store it at `<workspace>/picoclaw/keys/picoclaw_ed25519.key` (0600, directory 0700), which is `/root/.picoclaw/keys/picoclaw_ed25519.key` in the container. `serve` injects `PICOCLAW_SSH_KEY_PATH` pointing at the container path.

This reuses the existing `picoclaw/` mount, so no new bind mount is added and the AGENTS.md mounting rules are untouched. It also satisfies `allowedSSHKeyPath` through its first branch, which is the only branch Piccolo can satisfy without setting `PICOCLAW_HOME` (explicitly forbidden) or mounting `/root/.ssh`.

*Alternative considered:* mount host `~/.ssh` into the container. Rejected — it exposes the user's personal SSH keys to the gateway and to every agent tool, which is a far larger blast radius than the Piccolo workspace.

### D3: The passphrase reaches the container only as a resolved env reference

`piccolo.yaml` gains `secrets.passphrase_env: <HOST_VAR>` (a name, never a value). `serve` resolves it through the existing `ResolveEnv`/`env:HOST_VAR` machinery and injects `PICOCLAW_KEY_PASSPHRASE` into the container. The name goes into the manifest; the value stays in the host environment and in the container's process env.

*Alternatives considered and rejected:*
- Prompt at `serve` time. Rejected — `serve` is detached, and `restart`/`reload`/CI call it without a terminal.
- Store the passphrase in `piccolo.yaml`. Rejected — that is plaintext in the workspace, the exact thing this change is fixing, only moved between two files.
- macOS Keychain. Rejected for v1 — it makes the workspace non-portable and unusable under Linux CI; revisit as its own change.

**Trade-off stated plainly:** the passphrase is visible in `docker inspect`. Anyone with docker access to the host can already read the container's filesystem and env, so this is not a regression, but it means `enc://` defends against copies, backups, commits, and a pasted config — not against a host operator. `doctor` and README must say this in those words rather than implying secrets are unconditionally safe.

### D4: One explicit opt-in command; `init` offers but never requires

`piccolo secrets encrypt` walks `config.json`, encrypts every plaintext credential in place, writes the key file, records `secrets.passphrase_env`, and prints the export line for the host variable. `init` gains a wizard question and a non-interactive `--encrypt-keys` flag; the default stays plaintext so nothing changes for existing users or scripted init.

Encryption happens on the host at write time, so plaintext is never written to disk first. Because the passphrase must exist to encrypt, `secrets encrypt` accepts it from the host variable named in the manifest or from a `term.ReadPassword` prompt, matching the existing wizard's password handling.

### D5: The sidecar rule is unchanged, and encryption inherits it

If the sidecar governs a field, an `enc://` value written to `config.json` for that field is inert — exactly like a plaintext value. The `setup` refusal added in `e0a0581` already blocks the write; it stays indifferent to whether the incoming value is plaintext or `enc://`.

`piccolo secrets encrypt` must therefore check the sidecar before rewriting a field and refuse the same way, because a silent no-op here would leave a workspace that looks encrypted and is not.

### D6: Doctor reports the encrypted state as its own check

New check, placed next to the secret-sidecar check:
- `[OK]` when `config.json` carries no `enc://` value (nothing to deliver).
- `[OK]` when it does, `secrets.passphrase_env` names a set host variable, and the key file exists.
- `[WARN]` when it names an unset host variable — `serve` will fail at `ResolveEnv`.
- `[WARN]` when the key file is missing or unreadable.
- `[WARN]` when `container.image`'s PicoClaw predates `enc://` support. Not implementable as a version test today, so the check verifies against the running gateway instead and warns only when it can name the mismatch — never inventing a finding.

`HasUsableKey` already treats `enc://…` as usable, so the existing model and channel checks keep passing on an encrypted workspace.

### D7: Preflight fails fast

`serve`'s critical subset gains: if `config.json` contains `enc://`, require that `secrets.passphrase_env` resolves and that the key file exists, before any container is created. The alternative is the observed behaviour — the container exits, the gate times out, and the error text names `config.json` while the actual missing thing is a host variable.

### D8: A compatibility test pins the wire format

`internal/credential` carries a test vector: a ciphertext produced by PicoClaw v0.3.1's own `pkg/credential` (generated offline, committed as a fixture with its passphrase and key), which Piccolo must decrypt, plus the reverse — Piccolo's output decrypted by upstream. This is what makes D1's reimplementation safe rather than hopeful, and it fails loudly on a format drift instead of shipping a workspace the gateway cannot boot.

## Risks / Trade-offs

- **Passphrase lost ⇒ credentials unrecoverable.** No recovery path exists, by design. Mitigation: `secrets encrypt` prints a written warning before it rewrites anything, and `doctor` names the host variable so a user can find where it comes from. The pre-change `config.json` is kept as `config.json.bak` by the existing write path, and that backup holds plaintext — so the command states that the backup must be deleted or re-encrypted.
- **Format drift between Piccolo's implementation and PicoClaw's.** Mitigated by D8's fixture test, which is regenerated when the pinned version changes.
- **False security in user-facing text.** Mitigated by D3's explicit wording in doctor output and README.
- **A workspace encrypted on one host will not boot on another** without the key file and the host variable. Both travel with the workspace except the env var; documented in the opt-in warning.
- **The `docker inspect` exposure.** Accepted and documented, not hidden.

## Open Questions

- Should `piccolo secrets encrypt` also cover integration credentials under `integrations/`, or stay scoped to `config.json`? Left out of v1 scope pending the answer.
- Should `--encrypt-keys` become the default for `init` once the path is exercised, or stay opt-in permanently? Deferred to after this lands.
