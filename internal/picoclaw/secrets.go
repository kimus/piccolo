package picoclaw

// Secrets is a read-only view of PicoClaw's `.security.yml` sidecar — the file
// PicoClaw writes next to config.json and merges over it on load.
//
// It exists because PicoClaw's own save path relocates every secure field
// (model_list api_keys, channel tokens) out of config.json and into that file,
// with no passphrase involved: one trip through the web console,
// `picoclaw model add`, or a config-version migration is enough, because
// `SecureString.MarshalJSON` emits a `[NOT_HERE]` sentinel for the field and
// `SecureStrings.MarshalJSON` omits it, so config.json goes on looking like a
// workspace with no keys at all. On load, `.security.yml` WINS — and it wins
// per entry, in both directions: a matched entry with a populated list replaces
// config.json's key, and a matched entry with `api_keys: []` leaves the model
// with none even though config.json still shows one.
//
// Every rule here was verified against sipeed/picoclaw@v0.3.1
// (pkg/config/security.go loadSecurityConfig, pkg/config/config_struct.go
// SecureModelList.UnmarshalYAML + toNameIndex) by driving the real loader.
//
// Piccolo never writes this file and never surfaces a value from it: the type
// reports presence and claim relationships, so neither doctor nor setup can
// print a secret by accident.

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// SecurityFileName is PicoClaw's sidecar, always beside config.json.
const SecurityFileName = ".security.yml"

// EncSchemePrefix is PicoClaw's encrypted-value marker. Declared here rather than
// imported from internal/credential so this package keeps no dependency on the
// crypto it only reports about.
const EncSchemePrefix = "enc://"

// FileSchemePrefix marks a credential value that PicoClaw reads from a sibling
// file instead of carrying inline. Like enc:// it is already indirect, so nothing
// that writes credentials should treat it as plaintext to be wrapped.
const FileSchemePrefix = "file://"

// notHereSentinel is the literal `SecureString.MarshalJSON` writes into
// config.json in place of a field PicoClaw keeps in the sidecar. Decoded from
// JSON it arrives as the bare string, brackets included.
const notHereSentinel = "[NOT_HERE]"

// SecretFieldName lists every field PicoClaw models as SecureString/SecureStrings,
// in the order a channel's settings should be probed (primary bot credential
// first). Verified mechanically against pkg/config of the pinned release: each
// name appears ONLY on a SecureString field, which is what makes "encrypt any
// scalar under this key" safe rather than a guess.
//
// `username` is deliberately absent. It is the one secure-looking name that is
// ambiguous upstream — one SecureString use and one plain string use — so
// rewriting it by name would turn a real username into ciphertext and PicoClaw
// would send the ciphertext as the username. TestSecureFieldNamesAreUnambiguous
// re-checks that claim against the module cache when it is present.
var SecretFieldName = []string{
	"token", "bot_token", "app_token", "client_secret", "app_secret",
	"api_key", "api_keys", "access_token", "channel_access_token", "channel_secret",
	"webhook_url", "auth_token", "verification_token", "encrypt_key",
	"password", "sasl_password", "nickserv_password", "secret",
}

// SidecarSentinelValue is what PicoClaw writes in place of a secure field it has
// moved to the sidecar. Exported because setup must not treat it as a credential
// to move.
const SidecarSentinelValue = notHereSentinel

// ModelOverlayKey is the overlay key PicoClaw's merge resolves for the model at a
// given duplicate ordinal, counted in config.json order. Exposed so a writer and
// the reader cannot disagree about where a model's keys live.
func ModelOverlayKey(alias string, occurrence int) string {
	return alias + ":" + strconv.Itoa(occurrence)
}

// IsSecretFieldName reports whether a mapping key names a field PicoClaw treats
// as a secure string.
func IsSecretFieldName(name string) bool {
	for _, n := range SecretFieldName {
		if n == name {
			return true
		}
	}
	return false
}

// Secrets is the parsed sidecar. The zero value means "no file", which is the
// state of every workspace `piccolo init` wrote and nothing has saved since,
// and every method is safe to call on it.
type Secrets struct {
	// models is keyed by the raw sidecar key (`"gpt-5.4:0"`, or a bare
	// `"gpt-5.4"`), valued by whether that entry lists a usable key.
	models map[string]bool
	// channels is channel name -> secure field -> value as written. A field
	// absent from a present channel contributes nothing: PicoClaw merges channels
	// per field, so an empty `settings: {}` does NOT clear a token config.json
	// still carries.
	channels map[string]map[string]string
	// plaintext and encrypted hold secure-field values by state, recorded during
	// the load below so no later pass has to re-walk the document. Counting both
	// lets a report say how many values each file protects rather than describing
	// only config.json, which understates a workspace whose overlay is also sealed.
	plaintext []string
	encrypted []string
	// Present reports whether the file existed.
	Present bool
	// Err is a parse failure. PicoClaw treats one as fatal — LoadConfig returns
	// "failed to load security config" and the gateway never starts — so it is
	// never a warning to Piccolo either.
	Err error
}

// LoadSecrets reads the sidecar beside config.json. A missing file is not an
// error. A file that does not parse comes back through Secrets.Err.
func LoadSecrets(configPath string) Secrets {
	path := filepath.Join(filepath.Dir(configPath), SecurityFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Secrets{}
		}
		return Secrets{Err: err}
	}
	var root map[string]any
	if err := yaml.Unmarshal(data, &root); err != nil {
		return Secrets{Present: true, Err: err}
	}
	s := Secrets{Present: true, models: map[string]bool{}, channels: map[string]map[string]string{}}
	s.loadModels(root)
	s.loadChannels(root)
	s.collectPlaintext(root, "")
	return s
}

// collectPlaintext records every secure-field value the file still holds as
// readable text. Names are dotted paths so a report can point at the exact entry
// without ever quoting the value itself.
func (s *Secrets) collectPlaintext(node any, path string) {
	switch v := node.(type) {
	case map[string]any:
		for key, child := range v {
			next := key
			if path != "" {
				next = path + "." + key
			}
			if IsSecretFieldName(key) {
				s.noteSecureValues(child, next)
			}
			s.collectPlaintext(child, next)
		}
	case []any:
		for i, item := range v {
			s.collectPlaintext(item, fmt.Sprintf("%s[%d]", path, i))
		}
	}
}

// noteSecureValues records one secure field's value(s) by state: readable text goes
// to plaintext, ciphertext to encrypted. Empty values are neither — they are no
// credential, not a protected one. A file:// reference counts as neither, since it
// is indirect by design rather than sealed.
func (s *Secrets) noteSecureValues(v any, path string) {
	switch t := v.(type) {
	case string:
		s.noteSecureValue(t, path)
	case []any:
		for i, item := range t {
			if str, ok := item.(string); ok {
				s.noteSecureValue(str, fmt.Sprintf("%s[%d]", path, i))
			}
		}
	}
}

func (s *Secrets) noteSecureValue(raw, path string) {
	v := strings.TrimSpace(raw)
	switch {
	case v == "":
		return
	case strings.HasPrefix(v, EncSchemePrefix):
		s.encrypted = append(s.encrypted, path)
	case strings.HasPrefix(v, FileSchemePrefix):
		// Indirect already, and not this package's business.
	default:
		s.plaintext = append(s.plaintext, path)
	}
}

func isReadableSecret(raw string) bool {
	v := strings.TrimSpace(raw)
	if v == "" || strings.HasPrefix(v, EncSchemePrefix) || strings.HasPrefix(v, FileSchemePrefix) {
		return false
	}
	return true
}

// ModelSecret is the sidecar's say over one model_list entry.
type ModelSecret struct {
	// Matched means the sidecar governs this entry: whatever config.json writes
	// for its api_keys is discarded, in both directions.
	Matched bool
	// Key is the sidecar key that matched, for the message that names it.
	Key string
	// HasKeys means the matched entry lists at least one usable key. Matched
	// without HasKeys is the cleared state: the model runs with no key.
	HasKeys bool
}

// ResolveModel reports what PicoClaw will actually use as one entry's keys.
// alias is the entry's model_name and occurrence is its index among entries
// sharing that name — the key format PicoClaw's toNameIndex builds,
// `"<name>:<count>"`, counted in document order.
func (s Secrets) ResolveModel(alias string, occurrence int) ModelSecret {
	indexed := alias + ":" + strconv.Itoa(occurrence)
	if has, ok := s.models[indexed]; ok {
		return ModelSecret{Matched: true, Key: indexed, HasKeys: has}
	}
	// The bare-name fallback is PicoClaw's, reached only when the indexed key is
	// absent. It is resolved per entry, so two entries sharing a name can both be
	// claimed from one bare key — report the claim, do not invent a rule.
	if has, ok := s.models[alias]; ok {
		return ModelSecret{Matched: true, Key: alias, HasKeys: has}
	}
	return ModelSecret{}
}

// ResolveModels resolves the sidecar's say over every model_list entry at once.
// aliases is config.json's model_name list in document order and the result is
// index-aligned with it. This is the one place the `"<name>:<count>"` counting
// rule lives, so doctor and setup cannot disagree about which entry a sidecar
// key covers.
func (s Secrets) ResolveModels(aliases []string) []ModelSecret {
	out := make([]ModelSecret, len(aliases))
	count := map[string]int{}
	for i, a := range aliases {
		out[i] = s.ResolveModel(a, count[a])
		count[a]++
	}
	return out
}

// OrphanKeys lists sidecar model keys no config.json entry claims, sorted. The
// common way to make one is renaming a model alias: the key keeps guarding a
// model that no longer exists, and the renamed model loses the key that used to
// cover it.
func (s Secrets) OrphanKeys(aliases []string) []string {
	claimed := map[string]bool{}
	for _, m := range s.ResolveModels(aliases) {
		if m.Matched {
			claimed[m.Key] = true
		}
	}
	var out []string
	for k := range s.models {
		if !claimed[k] {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// ChannelSecret reports the sidecar's value for one channel setting field.
// PlaintextSecureValues lists the secure-field values the sidecar still holds as
// readable text, as dotted paths. It is the check that catches the state encryption
// cannot be assumed to have fixed: because the overlay overrides config.json per
// field, a workspace with an encrypted config.json and a plaintext overlay is not
// encrypted — it only looks encrypted, which is the failure shape this whole area
// keeps having to hunt down.
func (s Secrets) PlaintextSecureValues() []string {
	return append([]string(nil), s.plaintext...)
}

// EncryptedSecureValues lists the secure-field paths the sidecar holds as enc://
// ciphertext, proving the overlay is sealed rather than merely present.
func (s Secrets) EncryptedSecureValues() []string {
	return append([]string(nil), s.encrypted...)
}

func (s Secrets) ChannelSecret(channel, field string) ChannelSecret {
	v, ok := s.channels[channel][field]
	if !ok {
		return ChannelSecret{}
	}
	return ChannelSecret{Matched: true, Value: v}
}

// ChannelSecret is one secure channel field as the sidecar writes it.
type ChannelSecret struct {
	Matched bool
	// Value is secret material. Compare it, never print it, never send it.
	Value string
}

// HasUsableKey reports whether a raw api_key/token string carries a real value:
// non-empty, not an unfilled template, and not the sentinel PicoClaw writes when
// the secret lives in the sidecar instead.
func HasUsableKey(raw string) bool {
	k := strings.TrimSpace(raw)
	switch {
	case k == "":
		return false
	case strings.EqualFold(k, notHereSentinel):
		return false
	}
	lower := strings.ToLower(k)
	switch {
	case strings.HasPrefix(lower, "sk-your"), strings.HasPrefix(lower, "your-"):
		return false
	case strings.Contains(lower, "placeholder"), strings.Contains(lower, "changeme"), strings.Contains(lower, "xxx"):
		return false
	}
	// Upstream's own example values are ALL_CAPS_WITH_UNDERSCORES
	// (config/config.example.json: YOUR_DISCORD_BOT_TOKEN, YOUR_BRAVE_API_KEY).
	// The `your-` prefix rule above cannot see them, so an untouched example read
	// as a usable credential and doctor stayed silent on a workspace that had never
	// been given a real key — a warning the user needed, suppressed by a hyphen.
	//
	// The shape test stays narrow on purpose. Real tokens almost always carry a
	// lowercase letter, a hyphen, or a colon (sk-…, xoxb-…, 123456:ABC…), so
	// requiring an all-uppercase string that also contains an underscore leaves
	// them alone; AKIAIOSFODNN7EXAMPLE and ghp_AbC… both pass through. A genuine
	// credential shaped exactly like a template is possible but rare, and the cost
	// is a warning naming a field that is in fact fine — not a silent failure.
	if isTemplateShape(k) {
		return false
	}
	return true
}

// isTemplateShape reports the unfilled-example form PicoClaw's own template uses:
// only upper-case letters, digits and underscores, and at least one underscore.
func isTemplateShape(s string) bool {
	if !strings.Contains(s, "_") {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
		default:
			return false
		}
	}
	return true
}

// IsSidecarSentinel reports whether config.json carries the `[NOT_HERE]`
// placeholder, which is PicoClaw saying "the value lives in .security.yml".
func IsSidecarSentinel(raw string) bool {
	return strings.EqualFold(strings.TrimSpace(raw), notHereSentinel)
}

// ModelAliases is config.json's model_name list in document order, which is what
// decides both the sidecar key format and which sidecar keys are orphans.
func (c Config) ModelAliases() []string {
	out := make([]string, 0, len(c.ModelList))
	for _, m := range c.ModelList {
		out = append(out, m.ModelName)
	}
	return out
}

func (s *Secrets) loadModels(root map[string]any) {
	ml, ok := root["model_list"]
	if !ok {
		return
	}
	mm, ok := ml.(map[string]any)
	if !ok {
		// PicoClaw decodes this as map[string]*ModelConfig; a non-map is a load
		// failure it reports, not something Piccolo should guess a state for.
		return
	}
	for key, v := range mm {
		entry, ok := v.(map[string]any)
		if !ok {
			// `"gpt-5.4:0":` with nothing under it decodes to a nil *ModelConfig,
			// and upstream's merge skips a nil sec — config.json keeps its own keys.
			// Recording it as a match would report a cleared model that is fine, and
			// refusing a setup write that would in fact land: an empty list and a null
			// entry look alike in YAML and mean opposite things.
			continue
		}
		s.models[key] = entryHasUsableKey(entry["api_keys"])
	}
}

func (s *Secrets) loadChannels(root map[string]any) {
	// loadSecurityConfig accepts either root key.
	cl, ok := root["channel_list"]
	if !ok {
		cl, ok = root["channels"]
	}
	if !ok {
		return
	}
	mm, ok := cl.(map[string]any)
	if !ok {
		return
	}
	for name, v := range mm {
		ch, ok := v.(map[string]any)
		if !ok {
			continue
		}
		settings, ok := ch["settings"].(map[string]any)
		if !ok {
			continue
		}
		fields := map[string]string{}
		for k, raw := range settings {
			str, ok := raw.(string)
			if !ok {
				continue
			}
			fields[k] = str
		}
		if len(fields) > 0 {
			s.channels[name] = fields
		}
	}
}

func entryHasUsableKey(v any) bool {
	list, ok := v.([]any)
	if !ok {
		return false
	}
	for _, item := range list {
		if str, ok := item.(string); ok && HasUsableKey(str) {
			return true
		}
	}
	return false
}
