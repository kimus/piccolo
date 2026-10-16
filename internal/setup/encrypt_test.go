package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kimus/piccolo/internal/configdoc"
	"github.com/kimus/piccolo/internal/credential"
	"github.com/kimus/piccolo/internal/picoclaw"
)

func encryptFixtureKey(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path, err := credential.Generate(filepath.Join(dir, "keys"))
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func secretsFromSidecar(t *testing.T, body string) picoclaw.Secrets {
	t.Helper()
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.json")
	if err := os.WriteFile(cfg, []byte(`{"version":3}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if body != "" {
		if err := os.WriteFile(filepath.Join(dir, picoclaw.SecurityFileName), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return picoclaw.LoadSecrets(cfg)
}

const encryptDoc = `{
  "version": 3,
  "model_list": [
    { "model_name": "gpt", "provider": "openai", "model": "gpt-4o", "api_keys": ["sk-plain-one"] },
    { "model_name": "local", "provider": "ollama", "model": "llama3" }
  ],
  "channel_list": {
    "discord": { "enabled": true, "type": "discord", "settings": { "token": "dt-plain", "proxy": "http://p" } },
    "slack":   { "enabled": false, "type": "slack", "settings": { "bot_token": "xoxb-plain", "app_token": "xapp-plain" } },
    "empty":   { "enabled": false, "type": "discord", "settings": { "token": "" } }
  },
  "custom_key": { "kept": true }
}`

func TestEncryptWorkspaceCoversModelsAndChannels(t *testing.T) {
	key := encryptFixtureKey(t)
	doc, err := configdoc.Parse([]byte(encryptDoc))
	if err != nil {
		t.Fatal(err)
	}

	res, err := EncryptWorkspace(doc, picoclaw.Secrets{}, key, "PASS")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Changed) != 4 {
		t.Fatalf("changed = %v, want the model key plus 3 channel tokens", res.Changed)
	}

	out := string(doc.Marshal())
	// No plaintext may survive anywhere in the rewritten file.
	for _, plain := range []string{"sk-plain-one", "dt-plain", "xoxb-plain", "xapp-plain"} {
		if strings.Contains(out, plain) {
			t.Errorf("plaintext %q survived encryption", plain)
		}
	}
	for _, want := range []string{`"model_name": "gpt"`, `"proxy": "http://p"`, `"custom_key"`} {
		if !strings.Contains(out, want) {
			t.Errorf("encryption lost %s", want)
		}
	}
	// An empty token stays empty rather than becoming a ciphertext of nothing.
	if !strings.Contains(out, `"token": ""`) {
		t.Errorf("empty channel token was altered:\n%s", out)
	}
	// A model with no api_keys gains one, which would break PicoClaw's enablement
	// inference and add a field the user never had.
	if strings.Contains(out, `"model_name": "local"`) == false {
		t.Fatal("second model vanished")
	}

	// Everything written must actually decrypt back to one of the known plaintexts.
	// Marshal puts one array element per line, so scan quoted values generically.
	want := map[string]string{
		"sk-plain-one": "model gpt api_keys",
		"dt-plain":     "channel discord settings.token",
		"xoxb-plain":   "channel slack settings.bot_token",
		"xapp-plain":   "channel slack settings.app_token",
	}
	seen := map[string]bool{}
	// Every enc:// token in the document, wherever it sits: alone on an array line
	// or as the value of a "token": "enc://…" field.
	for _, part := range strings.Split(out, credential.Scheme)[1:] {
		val := credential.Scheme + part
		if i := strings.IndexAny(val, `",`); i >= 0 {
			val = val[:i]
		}
		got, err := credential.Decrypt("PASS", key, val)
		if err != nil {
			t.Errorf("written ciphertext does not decrypt (%s…): %v", val[:24], err)
			continue
		}
		if _, ok := want[got]; !ok {
			t.Errorf("decrypt yielded %q, which is not one of the known plaintexts", got)
			continue
		}
		seen[got] = true
	}
	if n := len(strings.Split(out, credential.Scheme)) - 1; n != 4 {
		t.Errorf("found %d enc:// values, want 4:\n%s", n, out)
	}
	for plain, label := range want {
		if !seen[plain] {
			t.Errorf("%s (%q) was never verified by decryption", label, plain)
		}
	}

}

// Re-running must be a no-op: an enc:// value is already indirect, and wrapping a
// ciphertext as if it were a secret would produce a credential that decrypts to
// base64 noise.
func TestEncryptWorkspaceIsIdempotent(t *testing.T) {
	key := encryptFixtureKey(t)
	doc, err := configdoc.Parse([]byte(encryptDoc))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := EncryptWorkspace(doc, picoclaw.Secrets{}, key, "PASS"); err != nil {
		t.Fatal(err)
	}
	first := string(doc.Marshal())

	res, err := EncryptWorkspace(doc, picoclaw.Secrets{}, key, "PASS")
	if err != nil {
		t.Fatal(err)
	}
	if !res.NothingChanged() {
		t.Fatalf("second run changed things: %v", res.Changed)
	}
	if string(doc.Marshal()) != first {
		t.Error("second run rewrote the file, so a workspace diff shows a change that did not happen")
	}
}

// The core of task 3.4: a field the sidecar governs must be refused, not silently
// rewritten into a value the gateway will never read.
func TestEncryptWorkspaceRefusesShadowedFields(t *testing.T) {
	sidecar := secretsFromSidecar(t, "model_list:\n  gpt:0:\n    api_keys:\n      - sk-from-sidecar\nchannel_list:\n  discord:\n    settings:\n      token: dt-from-sidecar\n")
	key := encryptFixtureKey(t)
	doc, err := configdoc.Parse([]byte(encryptDoc))
	if err != nil {
		t.Fatal(err)
	}

	res, err := EncryptWorkspace(doc, sidecar, key, "PASS")
	if err != nil {
		t.Fatal(err)
	}

	// Both shadowed fields refused, with the sidecar named.
	names := map[string]string{}
	for _, r := range res.Refused {
		names[r.Field] = r.Reason
	}
	for _, want := range []string{`model "gpt" api_keys`, `channel "discord" settings.token`} {
		if _, ok := names[want]; !ok {
			t.Errorf("shadowed field %q was not refused: %v", want, names)
		}
	}
	for f, reason := range names {
		if !strings.Contains(reason, ".security.yml") {
			t.Errorf("refusal of %s does not name the sidecar: %q", f, reason)
		}
	}

	// The unshadowed ones still encrypt, so a partial job is not a failed one.
	if len(res.Changed) == 0 {
		t.Fatal("nothing was encrypted despite unshadowed fields being present")
	}
	out := string(doc.Marshal())
	if strings.Contains(out, "xoxb-plain") {
		t.Error("slack bot_token was not encrypted")
	}
	// And the refused ones keep the value they had, untouched — the summary must
	// not be able to claim a workspace that is fully encrypted.
	if !strings.Contains(out, "sk-plain-one") {
		t.Error("shadowed model key was rewritten even though it was refused")
	}
	if !strings.Contains(out, "dt-plain") {
		t.Error("shadowed channel token was rewritten even though it was refused")
	}
	if sum := res.Summary(); !strings.Contains(sum, "left in place") {
		t.Errorf("summary hides the refusals: %s", sum)
	}
}

// A null sidecar entry is skipped by PicoClaw's merge, so config.json's value IS
// in force and encrypting it is correct. Refusing here would leave a protectable
// credential in plaintext forever.
func TestEncryptWorkspaceEncryptsNullSidecarEntry(t *testing.T) {
	sidecar := secretsFromSidecar(t, "model_list:\n  gpt:0:\n")
	key := encryptFixtureKey(t)
	doc, err := configdoc.Parse([]byte(encryptDoc))
	if err != nil {
		t.Fatal(err)
	}
	res, err := EncryptWorkspace(doc, sidecar, key, "PASS")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range res.Refused {
		if strings.Contains(r.Field, "gpt") {
			t.Errorf("null sidecar entry blocked encryption of %s", r.Field)
		}
	}
	if strings.Contains(string(doc.Marshal()), "sk-plain-one") {
		t.Error("model key left plaintext behind a null sidecar entry")
	}
}

// The passphrase is required to encrypt anything: without it Piccolo cannot
// produce a value the gateway can read, and writing plaintext while reporting
// success is the failure this whole change exists to avoid.
func TestEncryptWorkspaceRequiresPassphrase(t *testing.T) {
	key := encryptFixtureKey(t)
	doc, err := configdoc.Parse([]byte(encryptDoc))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := EncryptWorkspace(doc, picoclaw.Secrets{}, key, ""); err == nil {
		t.Fatal("empty passphrase accepted")
	}
	// And the document was not half-modified.
	if strings.Contains(string(doc.Marshal()), "enc://") {
		t.Error("partial encryption happened despite the refused passphrase")
	}
}

func TestEncryptableGuardsIndirectValues(t *testing.T) {
	for _, raw := range []string{
		"", "   ", "sk-your-key", "YOUR_DISCORD_BOT_TOKEN", "[NOT_HERE]",
		"enc://AAAA", "  enc://AAAA  ", "file://keys/openai.key",
	} {
		if encryptable(raw) {
			t.Errorf("encryptable(%q) = true; it must be left alone", raw)
		}
	}
	for _, raw := range []string{"sk-real", "dt-1234", " xoxb-real "} {
		if !encryptable(raw) {
			t.Errorf("encryptable(%q) = false; real plaintext must be encrypted", raw)
		}
	}
}

// A workspace with no credentials to protect must produce byte-identical output
// (task 3.6's concern, asserted at the unit the command writes through).
func TestEncryptWorkspaceNoCredentialsIsByteIdentical(t *testing.T) {
	key := encryptFixtureKey(t)
	body := `{"version":3,"model_list":[{"model_name":"m","provider":"openai","model":"x","api_keys":["[NOT_HERE]"]}]}`
	doc, err := configdoc.Parse([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	before := string(doc.Marshal())
	res, err := EncryptWorkspace(doc, picoclaw.Secrets{}, key, "PASS")
	if err != nil {
		t.Fatal(err)
	}
	if !res.NothingChanged() {
		t.Errorf("nothing was encryptable yet the run reported changes: %v", res.Changed)
	}
	if string(doc.Marshal()) != before {
		t.Error("file rewritten with no actual change")
	}
}

// The two rules now agree (task 4.5 widened doctor's warning), and this pins that
// they cannot drift apart again: a value doctor calls an unfilled template is a
// value encryption refuses to seal. Sealing one would give a workspace the look of
// protected credentials while leaving the actual problem untouched.
func TestPlaceholderIsRefusedEvenThoughDoctorWarnsDifferently(t *testing.T) {
	key := encryptFixtureKey(t)
	doc, err := configdoc.Parse([]byte(`{"version":3,"model_list":[{"model_name":"m","api_keys":["YOUR_DISCORD_BOT_TOKEN"]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	res, err := EncryptWorkspace(doc, picoclaw.Secrets{}, key, "PASS")
	if err != nil {
		t.Fatal(err)
	}
	if !res.NothingChanged() {
		t.Errorf("an example placeholder was encrypted: %v", res.Changed)
	}
	if strings.Contains(string(doc.Marshal()), credential.Scheme) {
		t.Error("placeholder sealed into ciphertext")
	}
	// Agreement, asserted on both sides of the same call.
	if picoclaw.HasUsableKey("YOUR_DISCORD_BOT_TOKEN") {
		t.Error("doctor's rule changed without changing encryption's")
	}
	if encryptable("YOUR_DISCORD_BOT_TOKEN") {
		t.Error("encryption treats a template value as a secret")
	}
}

// Encryption must touch only the fields that actually hold credentials. A
// value-shaped-like-a-token in `proxy`, an agent `workspace`, or any user-owned
// key must pass through byte-identical: rewriting a non-credential field would
// corrupt settings nobody asked to protect, and PicoClaw does not model those
// fields as secrets at all.
func TestEncryptWorkspaceTouchesOnlyCredentialFields(t *testing.T) {
	key := encryptFixtureKey(t)
	doc, err := configdoc.Parse([]byte(`{"version":3,
      "model_list":[{"model_name":"m","api_keys":["sk-real"]}],
      "channel_list":{"discord":{"enabled":true,"type":"discord","settings":{"token":"dt-real","proxy":"MY_PROXY_VALUE"}}},
      "agents":{"defaults":{"workspace":"MY_WORKSPACE_PATH"}},
      "untouched":{"note":"ALL_CAPS_TOKEN","list":["sk-looking-but-not-a-credential"]}}`))
	if err != nil {
		t.Fatal(err)
	}
	res, err := EncryptWorkspace(doc, picoclaw.Secrets{}, key, "pw")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Changed) != 2 {
		t.Fatalf("changed = %v, want exactly the model key and the channel token", res.Changed)
	}
	out := string(doc.Marshal())
	for _, mustSurvive := range []string{"MY_PROXY_VALUE", "MY_WORKSPACE_PATH", "ALL_CAPS_TOKEN", "sk-looking-but-not-a-credential"} {
		if !strings.Contains(out, mustSurvive) {
			t.Errorf("non-credential value %q was altered:\n%s", mustSurvive, out)
		}
	}
}

// Every field in picoclaw.SecretFieldName must be sealed by encryption. The
// credential list is shared with doctor's channel check, so a field missing here
// is a credential left in plaintext that nothing warns about — the
// silent-under-protection shape, one layer down from the detection test in
// internal/doctor. Asserted against the list itself rather than a hardcoded copy,
// so adding a field in one place and not the other fails.
func TestEncryptCoversEveryListedSecretField(t *testing.T) {
	key := encryptFixtureKey(t)
	for _, field := range picoclaw.SecretFieldName {
		doc, err := configdoc.Parse([]byte(`{"version":3,"model_list":[{"model_name":"m","api_keys":["sk-model"]}],"channel_list":{"line":{"enabled":true,"type":"line","settings":{` +
			`"` + field + `":"a-real-value"}}}}`))
		if err != nil {
			t.Fatalf("%s: %v", field, err)
		}
		res, err := EncryptWorkspace(doc, picoclaw.Secrets{}, key, "PASS")
		if err != nil {
			t.Fatalf("%s: %v", field, err)
		}
		// Two changes: the model key and this channel field. Anything less means
		// the field was skipped.
		if len(res.Changed) != 2 {
			t.Errorf("field %q: changed = %v, want the model key and this field", field, res.Changed)
			continue
		}
		if strings.Contains(string(doc.Marshal()), "a-real-value") {
			t.Errorf("field %q left in plaintext", field)
		}
	}
}
