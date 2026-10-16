# Credential compatibility fixtures

Everything here is a **test fixture, not a secret**, and it is committed on
purpose.

`internal/credential` reproduces PicoClaw's `enc://` format rather than importing
it, because Piccolo has no Go dependency on PicoClaw (design decision D1). A
reproduction is only safe if it is checked against the real thing, so these files
prove that Piccolo and the pinned `github.com/sipeed/picoclaw` release agree on
every byte of the wire format, in both directions.

| File | What it is |
|---|---|
| `upstream-ciphertext.txt` | An `enc://` value produced by the pinned upstream `credential.Encrypt`, never by Piccolo. |
| `upstream-plaintext.txt` | The plaintext inside it. |
| `upstream-passphrase.txt` | The passphrase used to produce it. |
| `compat/picoclaw_ed25519.key` | The SSH private key whose raw bytes are the second derivation factor. |
| `compat/picoclaw_ed25519.key.pub` | Its public half. |

## Why a private key and a passphrase are allowed in git

They protect nothing. The key exists solely to derive an AES key for one fixture
ciphertext whose plaintext is also in this directory. There is no production
credential anywhere in here, no workspace, and no endpoint these values unlock.
Deleting them would make the compatibility test unrunnable, and generating them at
test time would do the same: the point is a ciphertext written by *upstream*, which
can only be produced once, against the pinned module.

The real key for a real workspace lives at `picoclaw/keys/picoclaw_ed25519.key`,
which is git-ignored (see the `structured-workspace-layout` spec), and whose
passphrase Piccolo never writes to any file at all.

## Regenerating

Fixtures must be rebuilt whenever the pinned PicoClaw version changes. Do not hand
edit them: a mistyped ciphertext tests nothing and reports a format mismatch that
is only a typo.

Generation has to happen in a scratch module outside this repository, because it is
the one place that must import PicoClaw and Piccolo's module graph must not:

```sh
# Requires the pinned module in the local cache:
#   go mod download github.com/sipeed/picoclaw@v0.3.1
mkdir -p /tmp/genfix && cd /tmp/genfix
cat > go.mod <<'EOF'
module genfix
go 1.26
require github.com/sipeed/picoclaw v0.3.1
EOF
cp "$(go env GOMODCACHE)/github.com/sipeed/picoclaw@v0.3.1/go.sum" . && chmod u+w go.sum
# main.go: credential.GenerateSSHKey(<compat key path>), then
#          credential.Encrypt(passphrase, keyPath, plaintext)
# with PICOCLAW_SSH_KEY_PATH set and GOPROXY pointed at $(go env GOMODCACHE)/cache/download.
```

Write the results to the three `upstream-*.txt` files and re-run the group:

```sh
go test ./internal/credential/
```

Both directions must pass. `TestConstantDriftIsDetected` exists so a wrong derived
key fails loudly instead of silently shipping a writer the gateway cannot read — if
that test ever stops failing when you break a constant, it is the test that is
broken.

## `upstreamcheck/`

A **separate Go module**, and that is the whole trick: Go skips any directory
holding its own `go.mod` when resolving the parent module's packages, so this is the
only place in the repository that references PicoClaw while Piccolo's `go.mod`
stays free of it. Deleting `upstreamcheck/go.mod` would pull PicoClaw into
Piccolo's dependency graph and silently undo D1.

It is invoked by `TestUpstreamDecryptsPiccoloOutput` and skips when the pinned
module is absent from the local cache, so a fresh clone without it still builds.
