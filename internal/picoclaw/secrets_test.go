package picoclaw

import (
	"os"
	"path/filepath"
	"testing"
)

// sidecarFixture writes a .security.yml beside a config.json in a temp dir and
// returns the config.json path LoadSecrets takes.
func sidecarFixture(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.json")
	if err := os.WriteFile(cfg, []byte(`{"version":3}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if body != "" {
		if err := os.WriteFile(filepath.Join(dir, SecurityFileName), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return cfg
}

// The bytes upstream actually writes, produced by driving v0.3.1's SaveConfig
// over a model_list with a duplicated alias — asserted here so the reader is
// pinned to real output rather than to a guess about its shape.
const realSidecar = `channel_list:
  dingtalk:
    settings: {}
  discord:
    settings:
      token: dt-real
  telegram:
    settings: {}
model_list:
  gpt-5.4:0:
    api_keys:
      - sk-real-one
  gpt-5.4:1:
    api_keys:
      - sk-real-two
  llama:0:
    api_keys:
      - dummy
web:
  brave: {}
skills:
  registries: {}
`

func TestLoadSecretsRealUpstreamBytes(t *testing.T) {
	s := LoadSecrets(sidecarFixture(t, realSidecar))
	if s.Err != nil {
		t.Fatalf("parse: %v", s.Err)
	}
	if !s.Present {
		t.Fatal("Present false for an existing file")
	}
	// Colon-bearing keys must survive YAML parsing, and duplicates must resolve to
	// their own occurrence — the loader's whole keying scheme.
	for _, tc := range []struct {
		alias string
		occ   int
		want  ModelSecret
	}{
		{"gpt-5.4", 0, ModelSecret{Matched: true, Key: "gpt-5.4:0", HasKeys: true}},
		{"gpt-5.4", 1, ModelSecret{Matched: true, Key: "gpt-5.4:1", HasKeys: true}},
		{"llama", 0, ModelSecret{Matched: true, Key: "llama:0", HasKeys: true}},
		{"other", 0, ModelSecret{}},
	} {
		if got := s.ResolveModel(tc.alias, tc.occ); got != tc.want {
			t.Errorf("ResolveModel(%s,%d) = %+v, want %+v", tc.alias, tc.occ, got, tc.want)
		}
	}
	// `settings: {}` contributes nothing — PicoClaw merges channels per field, so
	// an empty map must not be read as a shadow.
	if sc := s.ChannelSecret("telegram", "token"); sc.Matched {
		t.Error("empty settings must not claim the field")
	}
	sc := s.ChannelSecret("discord", "token")
	if !sc.Matched || sc.Value != "dt-real" {
		t.Errorf("discord token: %+v", sc)
	}
}

func TestLoadSecretsAbsentsAndErrors(t *testing.T) {
	// No file: not an error, and nothing is claimed.
	s := LoadSecrets(sidecarFixture(t, ""))
	if s.Present || s.Err != nil {
		t.Errorf("absent sidecar = %+v", s)
	}
	if s.ResolveModel("m1", 0).Matched {
		t.Error("absent sidecar claimed a model")
	}
	// Unparseable: reported, because PicoClaw refuses to load config at all.
	bad := LoadSecrets(sidecarFixture(t, "model_list: [this is: not: valid\n"))
	if !bad.Present || bad.Err == nil {
		t.Errorf("malformed sidecar = Present:%v Err:%v", bad.Present, bad.Err)
	}
	// The zero value must be queryable: callers pass it when there is nothing to
	// consult, and a panic there would surface inside doctor or a setup screen.
	var zero Secrets
	if zero.Present || zero.Err != nil || zero.ResolveModel("m1", 0).Matched {
		t.Error("zero value must read as no sidecar")
	}
	if got := zero.ResolveModels([]string{"a", "b"}); len(got) != 2 || got[0].Matched || got[1].Matched {
		t.Errorf("zero-value ResolveModels = %+v", got)
	}
	if n := len(zero.OrphanKeys([]string{"a"})); n != 0 {
		t.Errorf("zero-value OrphanKeys = %d, want 0", n)
	}
	if sc := zero.ChannelSecret("discord", "token"); sc.Matched {
		t.Error("zero value claimed a channel field")
	}
}

func TestSecretsResolveModelsAlignsWithAliases(t *testing.T) {
	s := LoadSecrets(sidecarFixture(t, realSidecar))
	got := s.ResolveModels([]string{"gpt-5.4", "llama", "gpt-5.4", "newone"})
	want := []ModelSecret{
		{Matched: true, Key: "gpt-5.4:0", HasKeys: true},
		{Matched: true, Key: "llama:0", HasKeys: true},
		{Matched: true, Key: "gpt-5.4:1", HasKeys: true},
		ModelSecret{},
	}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("index %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestSecretsOrphanKeys(t *testing.T) {
	s := LoadSecrets(sidecarFixture(t, realSidecar))
	// A rename in config.json: gpt-5.4 became gpt-5.5, so both of its occurrence
	// keys are stranded and the new name is uncovered.
	orphans := s.OrphanKeys([]string{"gpt-5.5", "llama"})
	want := []string{"gpt-5.4:0", "gpt-5.4:1"}
	if len(orphans) != len(want) || orphans[0] != want[0] || orphans[1] != want[1] {
		t.Fatalf("orphans = %v, want %v", orphans, want)
	}
	// Nothing orphaned when every key is claimed, and the bare-name fallback counts.
	if n := len(s.OrphanKeys([]string{"gpt-5.4", "gpt-5.4", "llama"})); n != 0 {
		t.Errorf("expected no orphans, got %d", n)
	}
	bare := LoadSecrets(sidecarFixture(t, "model_list:\n  m1:\n    api_keys:\n      - sk-x\n"))
	if n := len(bare.OrphanKeys([]string{"m1"})); n != 0 {
		t.Errorf("bare key claimed by first occurrence: orphans = %v", bare.OrphanKeys([]string{"m1"}))
	}
}

// A matched entry with an empty or absent api_keys list is the CLEARED state:
// PicoClaw replaces config.json's list with nothing, so the model runs keyless
// even though config.json still displays a key.
func TestSecretsMatchedButEmptyIsCleared(t *testing.T) {
	for _, body := range []string{
		"model_list:\n  m1:0:\n    api_keys: []\n",
		"model_list:\n  m1:0:\n    api_keys:\n",
		"model_list:\n  m1:0: {}\n",
	} {
		s := LoadSecrets(sidecarFixture(t, body))
		got := s.ResolveModel("m1", 0)
		if !got.Matched || got.HasKeys {
			t.Errorf("body %q = %+v, want Matched && !HasKeys", body, got)
		}
	}
	// A null entry (`"m1:0:"` with no body at all) decodes to a nil *ModelConfig,
	// which upstream's merge skips: config.json keeps its own keys. Piccolo models
	// that as unclaimed.
	s := LoadSecrets(sidecarFixture(t, "model_list:\n  m1:0:\n"))
	if got := s.ResolveModel("m1", 0); got.Matched {
		t.Errorf("null entry claimed the model: %+v", got)
	}
}

func TestHasUsableKey(t *testing.T) {
	// The rule as Piccolo has always written it: bare empty, the `sk-your`/`your-`
	// prefixes, and three marker substrings.
	for _, bad := range []string{"", "   ", notHereSentinel, `[not_here]`, "sk-your-openai-key", "your-key", "placeholder", "changeme", "sk-xxx"} {
		if HasUsableKey(bad) {
			t.Errorf("%q must not count as usable", bad)
		}
	}
	// Upstream's own example shape (config/config.example.json uses
	// YOUR_DISCORD_BOT_TOKEN) reads as an unfilled template, not a credential. This
	// replaced a known gap where only the hyphenated `your-` prefix was caught and an
	// untouched example token was reported as usable.
	for _, example := range []string{"YOUR_DISCORD_BOT_TOKEN", "YOUR_BRAVE_API_KEY", "YOUR_CLIENT_SECRET", "MY_KEY_123"} {
		if HasUsableKey(example) {
			t.Errorf("%q is the shape of an unfilled example and must not read as usable", example)
		}
	}
	// Narrowness is the point: real tokens carry a lowercase letter, a hyphen or a
	// colon, so they must NOT be caught. A false positive here is a warning on a
	// working credential, which is the outcome this rule has to keep avoiding.
	for _, real := range []string{
		"sk-proj-abc123", "sk-ant-api03-abc", "xoxb-123-456-abc", "xapp-1-abc",
		"123456:ABC-DEF1234", "ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZabcdef123",
		"AKIAIOSFODNN7EXAMPLE", "hf_AbCdEfGhIjKlMnOpQrStUvWxYz1234", "github_pat_11ABC0xyz_abcdef",
	} {
		if !HasUsableKey(real) {
			t.Errorf("%q is a real credential shape and was flagged as a template", real)
		}
	}

	// Reference forms are values the gateway can resolve; emptiness is the only
	// question Piccolo can answer about them without the passphrase.
	for _, good := range []string{"sk-abcdef", "file://credentials/openai.key", "enc://AAAA", "dt-1234"} {
		if !HasUsableKey(good) {
			t.Errorf("%q must count as usable", good)
		}
	}
	if !IsSidecarSentinel(notHereSentinel) || IsSidecarSentinel("sk-real") {
		t.Error("sentinel detection")
	}
}

// The reader must classify sidecar values by state, since "the workspace is
// encrypted" is only true if the file that overrides config.json is sealed too.
func TestSecretsClassifyValuesByState(t *testing.T) {
	s := LoadSecrets(sidecarFixture(t, `model_list:
  gpt-4o:0:
    api_keys:
      - "enc://AAAA"
      - sk-readable
  other:0:
    api_keys: []
channel_list:
  discord:
    settings:
      token: dt-readable
      proxy: http://not-a-secret
  slack:
    settings:
      bot_token: "file://keys/slack.token"
`))
	if s.Err != nil {
		t.Fatal(s.Err)
	}
	gotPlain := map[string]bool{}
	for _, p := range s.PlaintextSecureValues() {
		gotPlain[p] = true
	}
	for _, want := range []string{"model_list.gpt-4o:0.api_keys[1]", "channel_list.discord.settings.token"} {
		if !gotPlain[want] {
			t.Errorf("%q not reported as plaintext: %v", want, gotPlain)
		}
	}
	// Non-secret fields, empty lists, already-sealed and indirect values stay out.
	for _, notWant := range []string{"model_list.gpt-4o:0.api_keys[0]", "channel_list.discord.settings.proxy", "channel_list.slack.settings.bot_token"} {
		if gotPlain[notWant] {
			t.Errorf("%q wrongly reported as plaintext", notWant)
		}
	}
	if enc := s.EncryptedSecureValues(); len(enc) != 1 || enc[0] != "model_list.gpt-4o:0.api_keys[0]" {
		t.Errorf("encrypted paths = %v", enc)
	}
}

// The ambiguous name must stay out of the list, or the sidecar walk would rewrite
// a real username into ciphertext and PicoClaw would send that as the username.
func TestSecretFieldNameExcludesAmbiguousNames(t *testing.T) {
	if IsSecretFieldName("username") {
		t.Error("username is a SecureString in one struct and a plain string in another; rewriting by name would corrupt the plain one")
	}
	for _, n := range []string{"token", "api_keys", "nickserv_password", "webhook_url"} {
		if !IsSecretFieldName(n) {
			t.Errorf("%q must be treated as a credential", n)
		}
	}
}
