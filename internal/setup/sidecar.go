package setup

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/kimus/piccolo/internal/credential"
	"github.com/kimus/piccolo/internal/picoclaw"
)

// EncryptSidecar rewrites plaintext secure values inside picoclaw/.security.yml
// as enc://, in place.
//
// This is the only way the file can stop being the weak point: the sidecar
// OVERRIDES config.json per field, so a workspace with encrypted config.json and
// a plaintext sidecar is not encrypted — it merely looks encrypted, and the
// gateway loads the plaintext. Verified against a real container: config.json
// held enc://… while the sidecar's plaintext token won and the gateway ran.
//
// The file is optional, so nothing is created when it is absent; absence is the
// good state. When it exists, the walk goes through yaml.Node rather than a
// struct so that every node Piccolo does not understand survives verbatim —
// decoding into a typed struct and re-emitting would silently drop fields the
// way Manifest.Write silently drops comments.
//
// Only scalars whose own key names a field PicoClaw models as a SecureString are
// touched, and that list was verified field-by-field against the pinned release,
// with the one ambiguous name (username) excluded.
func EncryptSidecar(sidecarPath, keyPath, passphrase string) (SidecarResult, error) {
	out, res, err := SealSidecarFile(sidecarPath, keyPath, passphrase)
	if err != nil || out == nil {
		return res, err
	}
	return res, writeSidecar(sidecarPath, out)
}

// SealSidecarFile returns the sealed bytes without touching disk. EncryptWorkspace
// uses this rather than writing, because it runs BEFORE the user's confirmation: a
// pass that writes here would leave a declined run having already rewritten the one
// file that overrides config.json, while printing "nothing was written".
func SealSidecarFile(sidecarPath, keyPath, passphrase string) (out []byte, res SidecarResult, err error) {
	data, err := os.ReadFile(sidecarPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, res, nil // no sidecar: nothing to protect, and not a problem
		}
		return nil, res, fmt.Errorf("read %s: %w", filepath.Base(sidecarPath), err)
	}
	if strings.TrimSpace(string(data)) == "" {
		return nil, res, nil
	}

	// A document node wraps the root; decode into it so comments and styles on the
	// top-level mapping are retained by the encoder.
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, res, fmt.Errorf("%s does not parse: %w", filepath.Base(sidecarPath), err)
	}
	if doc.Kind == 0 {
		return nil, res, nil
	}
	if !walkForSecrets(&doc, keyPath, passphrase, &res, "") {
		return nil, res, nil
	}

	// Indent 2, matching what PicoClaw's own SaveConfig writes. yaml defaults to
	// 4, which turns a three-value heal into a whole-file reformat: harmless to the
	// parser, but it buries the change in a file that is not Piccolo's to restyle.
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return nil, res, fmt.Errorf("encode %s: %w", filepath.Base(sidecarPath), err)
	}
	if err := enc.Close(); err != nil {
		return nil, res, fmt.Errorf("close encoder for %s: %w", filepath.Base(sidecarPath), err)
	}
	return buf.Bytes(), res, nil
}

// writeSidecar replaces the file atomically at 0600, matching what PicoClaw writes
// it with. A temp file plus rename keeps a half-written sidecar from being the file
// the gateway loads after a crash.
func writeSidecar(sidecarPath string, out []byte) error {
	tmp := sidecarPath + ".piccolo-tmp"
	if err := os.WriteFile(tmp, out, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, sidecarPath)
}

// SidecarResult reports what the pass changed and what it declined.
type SidecarResult struct {
	Encrypted []string
	// Skipped names values left alone because they were already indirect
	// (enc://, file://) or empty.
	Skipped int
}

// Changed reports whether the file needs rewriting.
func (r SidecarResult) Changed() bool { return len(r.Encrypted) > 0 }

func walkForSecrets(n *yaml.Node, keyPath, passphrase string, res *SidecarResult, path string) bool {
	switch n.Kind {
	case yaml.DocumentNode:
		changed := false
		for _, c := range n.Content {
			if walkForSecrets(c, keyPath, passphrase, res, path) {
				changed = true
			}
		}
		return changed
	case yaml.MappingNode:
		changed := false
		// Content alternates key, value.
		for i := 0; i+1 < len(n.Content); i += 2 {
			key, val := n.Content[i], n.Content[i+1]
			child := key.Value
			if path != "" {
				child = path + "." + key.Value
			}
			if picoclaw.IsSecretFieldName(key.Value) {
				if encryptSecretNode(val, keyPath, passphrase, child, res) {
					changed = true
				}
				continue
			}
			if walkForSecrets(val, keyPath, passphrase, res, child) {
				changed = true
			}
		}
		return changed
	case yaml.SequenceNode:
		changed := false
		for _, c := range n.Content {
			if walkForSecrets(c, keyPath, passphrase, res, path) {
				changed = true
			}
		}
		return changed
	}
	return false
}

// encryptSecretNode seals one secure value, or each item of a secure list
// (api_keys), recording the dotted path so a caller can tell WHICH field was
// protected. Strings that are already indirect are counted as skipped, never
// re-wrapped: encrypting a ciphertext would produce a value that decrypts to
// base64 noise, and encrypting the empty string would turn "no credential" into
// a credential-shaped string.
func encryptSecretNode(val *yaml.Node, keyPath, passphrase, field string, res *SidecarResult) bool {
	switch {
	case val.Kind == yaml.ScalarNode:
		return encryptScalar(val, keyPath, passphrase, field, res)
	case val.Kind == yaml.SequenceNode:
		changed := false
		for _, item := range val.Content {
			if item.Kind == yaml.ScalarNode && encryptScalar(item, keyPath, passphrase, field, res) {
				changed = true
			}
		}
		return changed
	}
	// A mapping under a secure key is not a credential; walk it for nested ones.
	return walkForSecrets(val, keyPath, passphrase, res, field)
}

func encryptScalar(val *yaml.Node, keyPath, passphrase, field string, res *SidecarResult) bool {
	v := strings.TrimSpace(val.Value)
	if v == "" || credential.IsEncrypted(v) || strings.HasPrefix(v, picoclaw.FileSchemePrefix) {
		if v != "" {
			res.Skipped++
		}
		return false
	}
	enc, err := credential.Encrypt(passphrase, keyPath, val.Value)
	if err != nil {
		// Left as-is rather than half-written: an unreadable ciphertext in the file
		// PicoClaw loads would take the gateway down at boot.
		res.Skipped++
		return false
	}
	val.Value = enc
	// Force a plain quoted scalar so a ciphertext that begins with a YAML special
	// character still parses as the string it is.
	val.Style = yaml.DoubleQuotedStyle
	res.Encrypted = append(res.Encrypted, field)
	return true
}

// WriteSidecarBytes persists a sealed overlay produced by EncryptWorkspace. It is
// exported for the callers that own the commit point — piccolo secrets encrypt and
// the setup toggle — so the write happens exactly once, after confirmation.
func WriteSidecarBytes(path string, out []byte) error { return writeSidecar(path, out) }
