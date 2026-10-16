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
func EncryptSidecar(sidecarPath, keyPath, passphrase string, live LiveOverlay, moves []CredentialMove) (SidecarResult, error) {
	out, res, err := SealSidecarFile(sidecarPath, keyPath, passphrase, live, moves)
	if err != nil || out == nil {
		return res, err
	}
	return res, writeSidecar(sidecarPath, out)
}

// SealSidecarFile returns the sealed bytes without touching disk. EncryptWorkspace
// uses this rather than writing, because it runs BEFORE the user's confirmation: a
// pass that writes here would leave a declined run having already rewritten the one
// file that overrides config.json, while printing "nothing was written".
func SealSidecarFile(sidecarPath, keyPath, passphrase string, live LiveOverlay, moves []CredentialMove) (out []byte, res SidecarResult, err error) {
	data, err := os.ReadFile(sidecarPath)
	switch {
	case err == nil:
	case os.IsNotExist(err):
		// Absence is normal: PicoClaw creates this file the first time something
		// saves through it, and Piccolo never needed it before. Credentials moving
		// into it need somewhere to land, so it is created from an empty document —
		// but ONLY when there is something to move, so an untouched workspace does
		// not gain a file it never asked for.
		if len(moves) == 0 {
			return nil, res, nil
		}
		data = nil
	default:
		return nil, res, fmt.Errorf("read %s: %w", filepath.Base(sidecarPath), err)
	}
	if len(data) > 0 && strings.TrimSpace(string(data)) == "" && len(moves) == 0 {
		return nil, res, nil
	}

	// Decoded as a document node so comments, styles and any node Piccolo does not
	// understand survive the rewrite; decoding into a typed struct would silently
	// drop everything the struct does not model.
	var doc yaml.Node
	if len(data) > 0 {
		if err := yaml.Unmarshal(data, &doc); err != nil {
			return nil, res, fmt.Errorf("%s does not parse: %w", filepath.Base(sidecarPath), err)
		}
	}
	if doc.Kind == 0 {
		doc = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{
			{Kind: yaml.MappingNode, Tag: "!!map"},
		}}
	}
	sealed := walkForSecrets(&doc, keyPath, passphrase, &res, "")
	moved := applyMoves(&doc, keyPath, passphrase, moves, &res)
	pruned := pruneDeadEntries(&doc, live, &res)
	if !sealed && !moved && !pruned {
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
	// Pruned lists overlay keys removed because no config.json entry can reach
	// them. Reported because a silently vanishing credential is exactly the kind
	// of change a user must be able to see after the fact.
	Pruned []string
	// Moved lists the locators whose values landed in the overlay. The caller
	// deletes the config.json copy only for what appears here, so a seal that failed
	// can never cost the user a credential.
	Moved []string
	// Skipped names values left alone because they were already indirect
	// (enc://, file://) or empty.
	Skipped int
}

// Changed reports whether the file needs rewriting.
func (r SidecarResult) Changed() bool {
	return len(r.Encrypted) > 0 || len(r.Moved) > 0 || len(r.Pruned) > 0
}

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

// CredentialMove is one secure value moving from config.json into the overlay.
//
// The destination is not a choice: PicoClaw's loader reads the overlay AFTER
// config.json and lets it win field by field, so a value kept in both files means
// the config.json copy is never loaded while staying readable on disk. Moving it
// leaves exactly one copy, in the file whose purpose is holding secrets.
//
// Value may be plaintext or already enc://. A ciphertext is copied verbatim rather
// than decrypted and re-sealed: re-encrypting changes the salt and nonce for no
// benefit, and a needless decrypt is a needless exposure.
type CredentialMove struct {
	Model   string // set for a model_list entry's api_keys value
	Ordinal int    // duplicate ordinal PicoClaw's merge computes for that entry
	Channel string // set for a channel settings field
	Field   string // the settings field name, with Channel
	Value   string // the config.json value being moved, plaintext or enc://
}

// Locator names the move's origin in config.json, so the caller can match a landed
// move back to the value it may now delete.
func (m CredentialMove) Locator() string {
	if m.Model != "" {
		return "model:" + m.Model
	}
	return "channel:" + m.Channel + "." + m.Field
}

// OverlayModelKey is the overlay key PicoClaw's merge resolves for a model at an
// ordinal. The "<name>:<count>" form is upstream's own toNameIndex output; writing
// anything else would leave the credential unreachable, which is worse than
// leaving it in config.json.
func OverlayModelKey(model string, ordinal int) string {
	return fmt.Sprintf("%s:%d", model, ordinal)
}

// LiveOverlay is what config.json can actually address in the overlay. An
// unreachable overlay key holds a token nothing loads, and it survives every
// later rename of the model it used to belong to.
type LiveOverlay struct {
	// ModelKeys holds overlay keys a config.json entry resolves to, including the
	// bare-alias fallback the merge uses when the ordinal key is absent.
	ModelKeys map[string]bool
	// Channels holds the channel names present in config.json's channel_list.
	Channels map[string]bool
	// HaveModels and HaveChannels guard the prune. If config.json could not be
	// read at all the live set is UNKNOWN, and treating unknown as empty would
	// delete every credential in the overlay on a parse hiccup.
	HaveModels, HaveChannels bool
}

// channelLive reports whether a channel entry's credentials are reachable.
func (l LiveOverlay) channelLive(name string) bool {
	if !l.HaveChannels {
		return true
	}
	return l.Channels[name]
}

// applyMoves writes each moved value into the overlay, encrypting it on the way in.
func applyMoves(root *yaml.Node, keyPath, passphrase string, moves []CredentialMove, res *SidecarResult) bool {
	if len(moves) == 0 {
		return false
	}
	body := root
	if body.Kind == yaml.DocumentNode && len(body.Content) > 0 {
		body = body.Content[0]
	}
	if body.Kind != yaml.MappingNode {
		return false
	}
	done := false
	for _, mv := range moves {
		sealed := mv.Value
		if v := strings.TrimSpace(mv.Value); v != "" && !credential.IsEncrypted(v) && !strings.HasPrefix(v, picoclaw.FileSchemePrefix) {
			enc, err := credential.Encrypt(passphrase, keyPath, mv.Value)
			if err != nil {
				// Not moved: the caller keeps the config.json copy.
				res.Skipped++
				continue
			}
			sealed = enc
		}
		if mv.Model != "" {
			keys := ensureMap(ensureMap(body, "model_list"), OverlayModelKey(mv.Model, mv.Ordinal))
			setSeq(keys, "api_keys", []string{sealed})
		} else {
			settings := ensureMap(ensureMap(ensureMap(body, "channel_list"), mv.Channel), "settings")
			setScalar(settings, mv.Field, sealed)
		}
		res.Moved = append(res.Moved, mv.Locator())
		done = true
	}
	return done
}

// pruneDeadEntries removes overlay credentials no config.json entry can reach.
func pruneDeadEntries(root *yaml.Node, live LiveOverlay, res *SidecarResult) bool {
	body := root
	if body.Kind == yaml.DocumentNode && len(body.Content) > 0 {
		body = body.Content[0]
	}
	if body.Kind != yaml.MappingNode {
		return false
	}
	changed := false
	for i := 0; i+1 < len(body.Content); i += 2 {
		switch body.Content[i].Value {
		case "model_list":
			if live.HaveModels {
				changed = pruneModelList(body.Content[i+1], live.ModelKeys, res) || changed
			}
		case "channel_list", "channels":
			changed = pruneChannelEntries(body.Content[i+1], live) || changed
		}
	}
	return changed
}

func pruneModelList(node *yaml.Node, liveKeys map[string]bool, res *SidecarResult) bool {
	if node.Kind != yaml.MappingNode {
		return false
	}
	kept := make([]*yaml.Node, 0, len(node.Content))
	changed := false
	for i := 0; i+1 < len(node.Content); i += 2 {
		if liveKeys[node.Content[i].Value] {
			kept = append(kept, node.Content[i], node.Content[i+1])
			continue
		}
		res.Pruned = append(res.Pruned, node.Content[i].Value)
		changed = true
	}
	node.Content = kept
	return changed
}

// pruneChannelEntries drops only the credential VALUES of an unreachable channel.
// The channel node stays: PicoClaw writes an entry for every channel type whether
// or not it is configured, so deleting scaffolding would rewrite far more of
// PicoClaw's file than the dead token warrants.
func pruneChannelEntries(node *yaml.Node, live LiveOverlay) bool {
	if node.Kind != yaml.MappingNode {
		return false
	}
	changed := false
	for i := 0; i+1 < len(node.Content); i += 2 {
		name := node.Content[i].Value
		if live.channelLive(name) {
			continue
		}
		settings := findMapValue(node.Content[i+1], "settings")
		if settings == nil || settings.Kind != yaml.MappingNode {
			continue
		}
		kept := make([]*yaml.Node, 0, len(settings.Content))
		for j := 0; j+1 < len(settings.Content); j += 2 {
			k, v := settings.Content[j], settings.Content[j+1]
			if picoclaw.IsSecretFieldName(k.Value) && strings.TrimSpace(v.Value) != "" {
				changed = true
				continue
			}
			kept = append(kept, k, v)
		}
		settings.Content = kept
	}
	return changed
}

func findMapValue(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}

// ensureMap returns the mapping at key, creating it when absent.
func ensureMap(parent *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(parent.Content); i += 2 {
		if parent.Content[i].Value != key {
			continue
		}
		if parent.Content[i+1].Kind == yaml.MappingNode {
			return parent.Content[i+1]
		}
		node := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		parent.Content[i+1] = node
		return node
	}
	node := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	parent.Content = append(parent.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Value: key, Tag: "!!str"}, node)
	return node
}

func setScalar(parent *yaml.Node, key, value string) {
	node := &yaml.Node{Kind: yaml.ScalarNode, Value: value, Tag: "!!str", Style: yaml.DoubleQuotedStyle}
	for i := 0; i+1 < len(parent.Content); i += 2 {
		if parent.Content[i].Value == key {
			parent.Content[i+1] = node
			return
		}
	}
	parent.Content = append(parent.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: key, Tag: "!!str"}, node)
}

func setSeq(parent *yaml.Node, key string, values []string) {
	seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	for _, v := range values {
		seq.Content = append(seq.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: v, Tag: "!!str", Style: yaml.DoubleQuotedStyle})
	}
	for i := 0; i+1 < len(parent.Content); i += 2 {
		if parent.Content[i].Value == key {
			parent.Content[i+1] = seq
			return
		}
	}
	parent.Content = append(parent.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: key, Tag: "!!str"}, seq)
}
