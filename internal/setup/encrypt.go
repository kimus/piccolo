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
	res.SidecarPath = sidecarPath
	if passphrase == "" {
		return res, credential.ErrPassphraseRequired
	}

	// The overlay pass runs FIRST: whether a config.json field is "refused and left
	// plaintext" depends on whether the overlay sealed it a moment later, and
	// reporting a sealed value as still-readable is a false alarm the user would go
	// chasing.
	var sc SidecarResult
	if sidecarPath != "" {
		// Sealed in memory: this function runs before the caller's confirmation, and
		// writing here would mean a declined run had already rewritten the file that
		// overrides config.json.
		var serr error
		res.SidecarBytes, sc, serr = SealSidecarFile(sidecarPath, keyPath, passphrase)
		if serr != nil {
			return res, serr
		}
		for _, f := range sc.Encrypted {
			res.addChanged(fmt.Sprintf("picoclaw/%s %s", picoclaw.SecurityFileName, f))
		}
	}
	res.Sidecar = sc
	// A config.json value may only be DISCARDED when the overlay provably holds a
	// usable value for the same field: sealed by this run, or already ciphertext
	// there. If the overlay's own copy is still plaintext, deleting config.json's
	// would destroy the only readable form of the credential, so those fields are
	// refused instead.
	sealed := sealedTargets(append(append([]string(nil), sc.Encrypted...), secrets.EncryptedSecureValues()...))

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

		if shields[i].Matched && sealed["model:"+alias] {
			// The overlay replaces this entry's WHOLE list on load, so every value
			// here is dead whether plaintext or ciphertext. A dead plaintext value is
			// worse than dead: it is a readable copy of a credential that already
			// exists encrypted elsewhere. Drop the field, matching what PicoClaw's
			// own SaveConfig leaves behind (it omits secure fields entirely).
			entry.Delete("api_keys")
			res.addChanged(fmt.Sprintf("model %q api_keys (only copy now in picoclaw/%s)", alias, picoclaw.SecurityFileName))
			res.Redundant = append(res.Redundant, fmt.Sprintf("model %q api_keys", alias))
			continue
		}
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
				// Reached only when the overlay's copy is still readable plaintext, so
				// the config.json value is the only usable one and must stay.
				res.addRefused(name, fmt.Sprintf("picoclaw/.security.yml %s governs this model's api_keys and still holds it as plaintext; seal it first", shields[i].Key))
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
					if sealed["channel:"+channel+"."+field] {
						// The overlay owns this field; the copy here is a readable
						// duplicate of a credential that is already sealed there.
						settings.Delete(field)
						res.addChanged(fmt.Sprintf("channel %q settings.%s (only copy now in picoclaw/%s)", channel, field, picoclaw.SecurityFileName))
						res.Redundant = append(res.Redundant, name)
						continue
					}
					res.addRefused(name, "picoclaw/.security.yml sets this field and still holds it as plaintext; seal it first")
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
	// SidecarBytes is the sealed overlay, or nil when there was nothing to change.
	// The caller writes it after its own confirmation, next to the config.json
	// write, so a declined or failed run leaves the previous bytes in place.
	SidecarBytes []byte
	// SidecarPath is where SidecarBytes belongs, echoed back so the caller does not
	// have to remember which of its own paths to write.
	SidecarPath string
	// Redundant lists config.json values DELETED because the overlay owns them.
	// Reported separately: "encrypted" and "removed as a duplicate" are different
	// facts, and a user comparing config.json afterwards would otherwise find
	// nothing where they expected ciphertext and conclude the run failed.
	Redundant []string
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
	if r.NothingChanged() && len(r.Refused) == 0 {
		return "nothing to encrypt: every credential in config.json is already encrypted or empty"
	}
	// Changed counts every mutation, because callers decide whether to write on it.
	// The summary must still separate sealing from deleting a duplicate: reporting a
	// removal as "encrypted" implies ciphertext the user will not find in
	// config.json, which reads like a failed run.
	sealed := len(r.Changed) - len(r.Redundant)
	if sealed < 0 {
		sealed = 0
	}
	var parts []string
	if sealed > 0 {
		parts = append(parts, fmt.Sprintf("%d credential(s) encrypted", sealed))
	}
	if n := len(r.Redundant); n > 0 {
		parts = append(parts, fmt.Sprintf("%d duplicate(s) removed from config.json, picoclaw/%s holds the only copy", n, picoclaw.SecurityFileName))
	}
	if n := len(r.Refused); n > 0 {
		parts = append(parts, fmt.Sprintf("%d left in plaintext because picoclaw/%s governs them and its own copy is unsealed", n, picoclaw.SecurityFileName))
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

// sealedTargets indexes the overlay paths the seal pass encrypted into the same
// vocabulary the config.json refusals use, so "refused here" and "sealed there" can
// be told apart. Names come from SealSidecarFile's dotted paths, e.g.
// "model_list.gpt-4o:0.api_keys[0]" -> "model:gpt-4o" and
// "channel_list.discord.settings.token" -> "channel:discord.token".
func sealedTargets(paths []string) map[string]bool {
	out := map[string]bool{}
	for _, p := range paths {
		parts := strings.Split(p, ".")
		if len(parts) < 2 {
			continue
		}
		switch parts[0] {
		case "model_list":
			alias := strings.SplitN(parts[1], ":", 2)[0]
			out["model:"+alias] = true
		case "channel_list", "channels":
			if len(parts) >= 4 {
				field := strings.TrimSuffix(parts[3], "[0]")
				out["channel:"+parts[1]+"."+field] = true
			}
		}
	}
	return out
}
