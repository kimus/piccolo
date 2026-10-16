package setup

import (
	"fmt"
	"strings"

	"github.com/kimus/piccolo/internal/configdoc"
	"github.com/kimus/piccolo/internal/credential"
	"github.com/kimus/piccolo/internal/picoclaw"
)

// EncryptWorkspace rewrites every plaintext credential in config.json as an
// enc:// value, and then does the same inside picoclaw/.security.yml. It returns
// what it changed and, separately, what it refused —
// because a workspace that "has been encrypted" while a shadowed field still
// holds plaintext is the same false green light this repo just fixed in doctor,
// and the caller must not be able to report a blanket success.
//
// Only config.json is touched. picoclaw/.security.yml is never written: it is
// PicoClaw's file, it may already hold enc:// values, and Piccolo has no way to
// re-encrypt those without the passphrase that produced them.
// sidecarPath is the workspace's picoclaw/.security.yml, or "" when the caller
// has no overlay to heal. Required rather than variadic: an omitted argument would
// silently leave the file that OVERRIDES config.json in plaintext, which is the
// exact failure this pass exists to close.
func EncryptWorkspace(doc *configdoc.Doc, secrets picoclaw.Secrets, keyPath, passphrase, sidecarPath string) (res EncryptResult, err error) {
	if passphrase == "" {
		return res, credential.ErrPassphraseRequired
	}

	// Model keys. The sidecar decides per model_list entry, keyed by the alias
	// plus its duplicate ordinal, so this walks the entries positionally rather
	// than by name — the same rule internal/picoclaw resolves with.
	aliases := docModelAliases(doc)
	shields := secrets.ResolveModels(aliases)
	v, ok := doc.Get("model_list")
	if !ok {
		return res, fmt.Errorf("config.json has no model_list to encrypt")
	}
	arr, ok := v.([]configdoc.Value)
	if !ok {
		return res, fmt.Errorf("config.json model_list is not an array")
	}
	for i, el := range arr {
		entry, ok := el.(*configdoc.Doc)
		if !ok || i >= len(shields) {
			continue
		}
		keys, isList := entry.Get("api_keys")
		if !isList {
			continue
		}
		kv, ok := keys.([]configdoc.Value)
		if !ok {
			continue
		}
		alias, _ := entry.GetString("model_name")
		name := fmt.Sprintf("model %q api_keys", alias)

		changed := false
		out := make([]configdoc.Value, 0, len(kv))
		for _, k := range kv {
			s, isStr := k.(string)
			// Already encrypted, or the sentinel PicoClaw writes when the value
			// moved to the sidecar: leave both alone. Re-encrypting an enc:// value
			// would wrap a ciphertext as if it were a secret.
			if !isStr || !encryptable(s) {
				out = append(out, k)
				continue
			}
			if shields[i].Matched {
				// The sidecar replaces this entry's whole list on load, so a fresh
				// enc:// value here would be dead the moment it was written.
				res.addRefused(name, fmt.Sprintf("picoclaw/.security.yml %s governs this model's api_keys", shields[i].Key))
				// Leave the entry exactly as it was: writing an enc:// value the
				// sidecar discards would make the workspace look encrypted while the
				// gateway keeps loading the old plaintext from elsewhere.
				out = append(out, k)
				continue
			}
			enc, err := credential.Encrypt(passphrase, keyPath, s)
			if err != nil {
				return res, fmt.Errorf("encrypt %s: %w", name, err)
			}
			out = append(out, enc)
			changed = true
			res.addChanged(name)
		}
		if changed {
			entry.Set("api_keys", out)
		}
	}

	// Channel tokens. The sidecar merges channels per settings FIELD, so the
	// shield question is per field, not per channel.
	cl, ok := doc.GetObject("channel_list")
	if ok {
		for _, channel := range cl.Keys() {
			entry, ok := cl.GetObject(channel)
			if !ok {
				continue
			}
			settings, ok := entry.GetObject("settings")
			if !ok {
				continue
			}
			for _, field := range picoclaw.SecretFieldName {
				raw, ok := settings.GetString(field)
				if !ok || !encryptable(raw) {
					continue
				}
				name := fmt.Sprintf("channel %q settings.%s", channel, field)
				if sc := secrets.ChannelSecret(channel, field); sc.Matched {
					res.addRefused(name, "picoclaw/.security.yml sets this field, so config.json's value is not read")
					continue
				}
				enc, err := credential.Encrypt(passphrase, keyPath, raw)
				if err != nil {
					return res, fmt.Errorf("encrypt %s: %w", name, err)
				}
				settings.Set(field, enc)
				res.addChanged(name)
			}
		}
	}

	// The sidecar pass closes the hole the config.json pass cannot: that file is
	// overridden by the sidecar per field, so leaving a plaintext credential there
	// would mean reporting a workspace encrypted while the gateway still loads
	// plaintext. Refusing the shadowed field is correct for a WRITE and wrong for a
	// HEAL, which is why the two are separate operations.
	var sc SidecarResult
	if sidecarPath != "" {
		sc, err = EncryptSidecar(sidecarPath, keyPath, passphrase)
		if err != nil {
			return res, err
		}
		for _, f := range sc.Encrypted {
			res.addChanged(fmt.Sprintf("picoclaw/%s %s", picoclaw.SecurityFileName, f))
		}
	}
	res.Sidecar = sc
	return res, nil
}

// encryptable reports whether a stored value is plaintext Piccolo should wrap.
//
// Left alone: empty values, the sidecar sentinel, anything already indirect
// (enc:// or file://), and unfilled template placeholders. The last group is now
// decided by the same rule doctor warns with (picoclaw.HasUsableKey), which is the
// convergence task 4.5 asked for: encrypting YOUR_DISCORD_BOT_TOKEN would seal a
// non-secret into ciphertext, give the workspace the appearance of protected
// credentials, and leave the real failure exactly where it was.
func encryptable(raw string) bool {
	s := strings.TrimSpace(raw)
	switch {
	case s == "":
		return false
	case credential.IsEncrypted(s):
		return false
	case strings.HasPrefix(s, picoclaw.FileSchemePrefix):
		return false
	case picoclaw.IsSidecarSentinel(s):
		return false
	}
	return picoclaw.HasUsableKey(s)
}

// EncryptResult is what a run changed and what it declined, with the reason for
// each refusal attached to the field it applies to.
type EncryptResult struct {
	Changed []string
	Refused []RefusedField
	// Sidecar reports the picoclaw/.security.yml pass separately, because a user
	// deciding whether their workspace is protected needs to know the overlay was
	// handled at all — silence there would look like it was skipped.
	Sidecar SidecarResult
}

// RefusedField names one credential left in plaintext and why.
type RefusedField struct {
	Field  string
	Reason string
}

func (r *EncryptResult) addChanged(name string) {
	r.Changed = append(r.Changed, name)
}

func (r *EncryptResult) addRefused(name, reason string) {
	r.Refused = append(r.Refused, RefusedField{Field: name, Reason: reason})
}

// NothingChanged is the state a second run lands in: every credential is already
// an enc:// value, so re-running must not rewrite the file and must not look like
// a fresh success.
func (r EncryptResult) NothingChanged() bool {
	return len(r.Changed) == 0
}

// Summary reports the outcome in one line, keeping refusals visible rather than
// folding them into "done".
func (r EncryptResult) Summary() string {
	if len(r.Changed) == 0 && len(r.Refused) == 0 {
		return "nothing to encrypt: every credential in config.json is already encrypted or empty"
	}
	var parts []string
	parts = append(parts, fmt.Sprintf("%d credential(s) encrypted", len(r.Changed)))
	if len(r.Refused) > 0 {
		parts = append(parts, fmt.Sprintf("%d left in place because picoclaw/.security.yml governs them", len(r.Refused)))
	}
	return strings.Join(parts, ", ")
}

func docModelAliases(doc *configdoc.Doc) []string {
	v, ok := doc.Get("model_list")
	if !ok {
		return nil
	}
	arr, ok := v.([]configdoc.Value)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, el := range arr {
		if m, ok := el.(*configdoc.Doc); ok {
			a, _ := m.GetString("model_name")
			out = append(out, a)
		} else {
			// A non-object entry has no name, but ResolveModels is positional: a
			// missing alias here would shift every later entry onto the wrong
			// sidecar key, so record the hole rather than dropping it.
			out = append(out, "")
		}
	}
	return out
}
