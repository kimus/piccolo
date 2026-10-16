## 1. Credential format implementation and its proof

- [x] 1.1 Create `internal/credential/` with `Encrypt`/`Decrypt` producing `enc://base64(salt[16] || nonce[12] || ciphertext)`, info string `picoclaw-credential-v1`, and `ikm = HMAC-SHA256(key = SHA256(sshKeyBytes), msg = passphrase)` per design D1
- [x] 1.2 Implement Ed25519 key generation writing the private key at 0600, its directory at 0700, and the public key beside it
- [x] 1.3 Generate compatibility fixtures offline from the pinned `sipeed/picoclaw@v0.3.1` `pkg/credential` (ciphertext, passphrase, key) and commit them as testdata
- [x] 1.4 Add the both-directions compatibility test from design D8: upstream ciphertext decrypts via Piccolo, Piccolo ciphertext decrypts via a locally built upstream decoder; confirm it fails when a constant is altered
- [x] 1.5 Test failure modes explicitly: wrong passphrase, wrong key, truncated payload, empty passphrase — each must return a distinguishable error, never a wrong plaintext
- [x] 1.6 Add `golang.org/x/crypto` as a direct dependency — keygen needs `ssh.MarshalPrivateKey`, while the encrypt/decrypt half is stdlib-only (Go 1.26 ships `crypto/hkdf`, so design D1's assumption that x/crypto was needed for derivation was wrong). Pinned at v0.31.0: the first attempt at v0.58.0 needlessly bumped `x/sync` and `x/text`, and at v0.31.0 nothing else in the graph moved

## 2. Workspace layout and manifest

- [x] 2.1 Add `secrets.passphrase_env` to `internal/manifest` as a non-secret variable *name*; confirm strict decoding accepts it and rejects unknown siblings, and that no passphrase value can be expressed in `piccolo.yaml`
- [x] 2.2 Document in the manifest type comment that a variable name is Piccolo-owned metadata while a passphrase value is never stored
- [x] 2.3 Add `picoclaw/keys/` to the required entries — done by collapsing three drifted copies of the list into one `generate.GitignoreEntries` read by init, doctor's check, and the migration. The copies had already diverged: the check looked at 5 entries while init wrote 8, so a workspace missing `picoclaw/.security.yml` (the relocated credentials) was reported `[OK]`. `TestGitignoreEntryListsAgree` pins writer against checker so they cannot drift again
- [x] 2.4 Add the key path to `internal/workspace` layout (`Layout.Keys()` host path plus the `/root/.picoclaw/keys/...` container path) so no caller hand-assembles it
- [x] 2.5 Update `cmd/piccolo/workspace.go`'s gitignore entry list so a migrated workspace gains the same entry

## 3. Write paths: opt in and store encrypted

- [x] 3.1 Implemented as one command, `piccolo secrets encrypt`, which generates the key when absent, records `secrets.passphrase_env`, appends the gitignore entry, and prints the export line. No separate `secrets init`: a key with nothing encrypted under it is a half state that makes `secrets check` report a workspace as encrypted when it is not. `secrets check` was added as the read-only companion
- [x] 3.2 Implement `piccolo secrets encrypt`: read `config.json`, encrypt each plaintext model key and channel token in place, write via the existing create-only-safe atomic path with `config.json.bak`
- [x] 3.3 Show the irreversible-loss warning before rewriting anything, including that `config.json.bak` holds the plaintext values (spec: "Warning precedes the rewrite")
- [x] 3.4 Reuse the sidecar check from `e0a0581` so a field governed by `picoclaw/.security.yml` is declined with its sidecar key named, and the summary does not claim a whole-workspace success when fields were skipped
- [x] 3.5 Accept the passphrase from the named host variable, else `term.ReadPassword`, in the existing `IsTerminal`-guarded style; never from a flag argument
- [x] 3.6 Verified no-opt-in byte-identity two ways: `TestEncryptWorkspaceNoCredentialsIsByteIdentical` (unit) and the existing init/doctor suites still passing unchanged. `TestSecretsRefusalLeavesWorkspaceConsistent` additionally pins that a refused run writes neither ciphertext nor a `secrets:` block

## 4. Generation and interactive setup

- [x] 4.1 Wizard question added after the credential input (where the consequence is visible), with the protects/does-not-hide/irreversible text inline; plus `--encrypt-keys` and `--passphrase-env` flags. Caught the real bug here: the `--yes` path never enters `wizard.Run`, so the opt-in was silently dropped and init reported success while writing plaintext — now asserted on the artifact
- [x] 4.2 Threaded as `Inputs.Encrypt func(string) (string, error)` rather than a key+passphrase, keeping `internal/generate` free of crypto and the filesystem so BuildPlan still works before the directory exists. Ordering: preview the plan, generate the key only after "Proceed?", rebuild the plan (BuildPlan is pure), then write — so an abort leaves no key and a completed run writes no plaintext byte
- [x] 4.3 Update the credential field help in `internal/setup/channels.go` and `modelForm` so "stored plaintext in config.json" is conditional on the workspace's posture
- [x] 4.4 `setupState.sealBeforeWrite` seals at `applyAndConfirm`, the single choke point both the model and channel forms pass through, so neither path can be forgotten. Posture = key file AND recorded route (either half alone cannot boot). Refuses the whole write when the passphrase is absent rather than storing plaintext in a workspace the user believes is protected. Required changing `setupState.out` from `*os.File` to `io.Writer` to reach these paths from tests
- [x] 4.5 Widen `picoclaw.HasUsableKey` to treat upstream's `YOUR_*` example shape as a placeholder (spec: setup-ux), and flip the known-gap assertion in `internal/picoclaw/secrets_test.go` deliberately rather than as a side effect
- [x] 4.6 Add a test that an untouched example token in `config.json` now warns, and that a normal key still passes

## 5. Delivery at serve time

- [x] 5.1 In `preflight`, when `config.json` contains `enc://`, require that `secrets.passphrase_env` resolves and the key file exists, before any container is created
- [x] 5.2 Compose `PICOCLAW_SSH_KEY_PATH` and `PICOCLAW_KEY_PASSPHRASE` into the container spec for encrypted workspaces, through the same path that already injects `PICOCLAW_GATEWAY_HOST`
- [x] 5.3 Verified live on real containers: running gateway + promotion to a different image + wrong passphrase -> candidate rejected, previous gateway restored and Up, exit 1. The same promotion with the correct passphrase exits 0 and adopts the new image. The launcher image is the interesting case because it stays up while refusing to auto-start its gateway child, so a liveness-only gate could have passed it
- [x] 5.4 Verified live: with the running gateway up and the variable unset, serve refuses in preflight (exit 1), names the variable, leaves the running container Up, and parks nothing. Covered in tests by driving preflight directly rather than executing serve, which would create real containers

## 6. Reporting

- [x] 6.1 Added beside the sidecar check. Warning, not failure, on purpose: the passphrase is exported by whoever runs serve and Piccolo never stores it, so failing would turn every `doctor` in a fresh shell into a red board. Missing key file is also a warning here and fatal at serve. A plaintext workspace gets no finding at all
- [x] 6.2 Phrase every encrypted-workspace message so the missing thing is named as the variable or key file, never as a malformed `config.json`
- [x] 6.3 `TestDoctorNeverPrintsCredentialValues` sweeps every result line across both delivery states with a plaintext secret, a sidecar secret, and the passphrase present; verified it fails when a value is deliberately leaked into the `[OK]` message
- [x] 6.4 Add doctor and `status` coverage for a workspace whose key file was deleted after encryption

## 7. Documentation and verification

- [ ] 7.1 Update README's credentials section: what `enc://` protects against, what it does not (host or Docker access, `docker inspect` env exposure), and the loss consequences
- [ ] 7.2 Record in AGENTS.md the verified facts: the two-input derivation, boot-failure behaviour of a bad passphrase, why `PICOCLAW_SSH_KEY_PATH` is required, and why `picoclaw onboard -E` is unusable
- [ ] 7.3 Add a real end-to-end verification: encrypt a workspace, `serve` it healthy, then unset the variable and confirm doctor plus preflight name the missing variable
- [ ] 7.4 Re-run the container matrix from design (no env / passphrase only / both / wrong passphrase) against the implemented injection to confirm no new failure mode was introduced
- [ ] 7.5 Confirm `go build ./...`, `go vet ./...`, `go test ./...`, and `openspec validate --specs` all pass
