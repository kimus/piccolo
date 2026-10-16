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
	var res SidecarResult
	data, err := os.ReadFile(sidecarPath)
	if err != nil {
		if os.IsNotExist(err) {
			return res, nil // no sidecar: nothing to protect, and not a problem
		}
		return res, fmt.Errorf("read %s: %w", filepath.Base(sidecarPath), err)
	}
	if strings.TrimSpace(string(data)) == "" {
		return res, nil
	}

	// A document node wraps the root; decode into it so comments and styles on the
	// top-level mapping are retained by the encoder.
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return res, fmt.Errorf("%s does not parse: %w", filepath.Base(sidecarPath), err)
	}
	if doc.Kind == 0 {
		return res, nil
	}

	if walkForSecrets(&doc, keyPath, passphrase, &res) {
		// Indent 2 and drop the document separator, matching what PicoClaw's own
		// SaveConfig writes. yaml.Marshal defaults to 4, which would reformat the
		// whole file: harmless to the parser, but it turns a three-value heal into a
		// diff that buries the change, in a file that is not Piccolo's to restyle.
		var buf bytes.Buffer
		enc := yaml.NewEncoder(&buf)
		enc.SetIndent(2)
		if err := enc.Encode(&doc); err != nil {
			return res, fmt.Errorf("encode %s: %w", filepath.Base(sidecarPath), err)
		}
		if err := enc.Close(); err != nil {
			return res, fmt.Errorf("close encoder for %s: %w", filepath.Base(sidecarPath), err)
		}
		out := buf.Bytes()
		// 0600, matching what PicoClaw itself writes this file with. A temp file
		// plus rename keeps a half-written sidecar from being what the gateway
		// loads after a crash.
		tmp := sidecarPath + ".piccolo-tmp"
		if err := os.WriteFile(tmp, out, 0o600); err != nil {
			return res, err
		}
		if err := os.Rename(tmp, sidecarPath); err != nil {
			return res, err
		}
	}
	return res, nil
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

func walkForSecrets(n *yaml.Node, keyPath, passphrase string, res *SidecarResult) bool {
	switch n.Kind {
	case yaml.DocumentNode:
		changed := false
		for _, c := range n.Content {
			if walkForSecrets(c, keyPath, passphrase, res) {
				changed = true
			}
		}
		return changed
	case yaml.MappingNode:
		changed := false
		// Content alternates key, value.
		for i := 0; i+1 < len(n.Content); i += 2 {
			key, val := n.Content[i], n.Content[i+1]
			if picoclaw.IsSecretFieldName(key.Value) {
				if encryptSecretNode(val, keyPath, passphrase, key.Value, res) {
					changed = true
				}
				continue
			}
			if walkForSecrets(val, keyPath, passphrase, res) {
				changed = true
			}
		}
		return changed
	case yaml.SequenceNode:
		changed := false
		for _, c := range n.Content {
			if walkForSecrets(c, keyPath, passphrase, res) {
				changed = true
			}
		}
		return changed
	}
	return false
}

// encryptSecretNode seals one secure value, or each item of a secure list
// (api_keys). Strings that are already indirect are counted as skipped, never
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
	return walkForSecrets(val, keyPath, passphrase, res)
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
