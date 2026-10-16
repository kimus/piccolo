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

	res, err := EncryptWorkspace(doc, picoclaw.Secrets{}, key, "PASS", "")
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
// A field the overlay governs is no longer a refusal: its value moves into the
// overlay, gets sealed there, and the config.json copy is dropped. The invariant
// this pins is that nothing readable survives anywhere, and that the summary says
// what actually happened rather than claiming an encryption that produced none.
func TestShadowedFieldsEndUpSealedAndDuplicatedNowhere(t *testing.T) {
	sidecar := secretsFromSidecar(t, "model_list:\n  gpt-5.4:0:\n    api_keys:\n      - sk-sidecar-wins\nchannel_list:\n  discord:\n    settings:\n      token: dt-sidecar-wins\n")
	key := encryptFixtureKey(t)
	dir := t.TempDir()
	scPath := filepath.Join(dir, "sidecar.yml")
	if err := os.WriteFile(scPath, []byte("model_list:\n  gpt-5.4:0:\n    api_keys:\n      - sk-sidecar-wins\nchannel_list:\n  discord:\n    settings:\n      token: dt-sidecar-wins\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	doc, err := configdoc.Parse([]byte(`{"version":3,"model_list":[{"model_name":"gpt-5.4","api_keys":["sk-CONFIG"]}],"channel_list":{"discord":{"settings":{"token":"dt-CONFIG"}},"slack":{"settings":{"bot_token":"xoxb-plain"}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	res, err := EncryptWorkspace(doc, sidecar, key, "PASS", scPath)
	if err != nil {
		t.Fatal(err)
	}
	cfgOut := string(doc.Marshal())
	for _, gone := range []string{"sk-CONFIG", "dt-CONFIG"} {
		if strings.Contains(cfgOut, gone) {
			t.Errorf("%q survived in config.json:\n%s", gone, cfgOut)
		}
	}
	// A field the overlay never mentions is still sealed, in place, in config.json.
	if strings.Contains(cfgOut, "xoxb-plain") {
		t.Error("unshadowed slack bot_token was not sealed")
	}
	scOut := string(res.SidecarBytes)
	if strings.Contains(scOut, "sk-sidecar-wins") || strings.Contains(scOut, "dt-sidecar-wins") {
		t.Errorf("plaintext survived in the overlay:\n%s", scOut)
	}
	if n := strings.Count(scOut, credential.Scheme); n < 2 {
		t.Errorf("overlay should hold sealed copies of both shadowed values:\n%s", scOut)
	}
	if len(res.Refused) != 0 {
		t.Errorf("nothing should be refused: %+v", res.Refused)
	}
	// The summary must not read as "N encrypted" when the work was duplication
	// removal plus sealing in two different files.
	if sum := res.Summary(); !strings.Contains(sum, "duplicate") {
		t.Errorf("summary omits the removals: %s", sum)
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
	res, err := EncryptWorkspace(doc, sidecar, key, "PASS", "")
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
	if _, err := EncryptWorkspace(doc, picoclaw.Secrets{}, key, "", ""); err == nil {
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
	res, err := EncryptWorkspace(doc, picoclaw.Secrets{}, key, "PASS", "")
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
	res, err := EncryptWorkspace(doc, picoclaw.Secrets{}, key, "PASS", "")
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
	res, err := EncryptWorkspace(doc, picoclaw.Secrets{}, key, "pw", "")
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
		res, err := EncryptWorkspace(doc, picoclaw.Secrets{}, key, "PASS", "")
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

// The refusal and the seal must not contradict each other. Sealing the overlay
// means a shadowed config.json field is protected, so reporting it as left in
// plaintext is a false alarm — and the summary's refusal count is what a user
// reads as "do I still have a problem?".
func TestRefusedButSealedInTheOverlayIsNotReportedAsPlaintext(t *testing.T) {
	key := encryptFixtureKey(t)
	sidecar := secretsFromSidecar(t, "model_list:\n  m1:0:\n    api_keys:\n      - sk-sidecar-plain\n")
	doc, err := configdoc.Parse([]byte(`{"version":3,"model_list":[{"model_name":"m1","api_keys":["sk-in-config"]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	scPath := filepath.Join(dir, "sidecar.yml")
	if err := os.WriteFile(scPath, []byte("model_list:\n  m1:0:\n    api_keys:\n      - sk-sidecar-plain\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	res, err := EncryptWorkspace(doc, sidecar, key, "PASS", scPath)
	if err != nil {
		t.Fatal(err)
	}
	// The overlay value was sealed...
	if len(res.Sidecar.Encrypted) == 0 {
		t.Fatal("overlay was not sealed")
	}
	// ...so nothing may be reported as left in plaintext.
	for _, r := range res.Refused {
		t.Errorf("reported %q as left in place, though the overlay sealed it: %s", r.Field, r.Reason)
	}
	if strings.Contains(res.Summary(), "left in place") {
		t.Errorf("summary still claims a plaintext remainder: %s", res.Summary())
	}
	// And config.json must not have gained a bogus empty credential.
	if strings.Contains(string(doc.Marshal()), `""`) {
		t.Errorf("config.json gained an empty credential:\n%s", doc.Marshal())
	}
}

// A credential must live in exactly one place. When the overlay carries it, the
// config.json copy is dead on load AND readable on disk, so sealing the overlay
// has to remove the duplicate rather than leave it behind.
func TestSealedOverlayRemovesTheConfigJSONDuplicate(t *testing.T) {
	key := encryptFixtureKey(t)
	dir := t.TempDir()
	scPath := filepath.Join(dir, "sidecar.yml")
	body := "model_list:\n  gpt:0:\n    api_keys:\n      - sk-MODEL\nchannel_list:\n  discord:\n    settings:\n      token: dt-TOKEN\n"
	if err := os.WriteFile(scPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	sidecar := secretsFromSidecar(t, body)
	doc, err := configdoc.Parse([]byte(`{"version":3,"model_list":[{"model_name":"gpt","provider":"openai","model":"gpt-4o","api_keys":["sk-MODEL"]}],"channel_list":{"discord":{"enabled":true,"type":"discord","settings":{"token":"dt-TOKEN","proxy":"http://p"}}}}`))
	if err != nil {
		t.Fatal(err)
	}

	res, err := EncryptWorkspace(doc, sidecar, key, "PASS", scPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Refused) != 0 {
		t.Errorf("nothing should be refused when the overlay can be sealed: %+v", res.Refused)
	}
	out := string(doc.Marshal())
	if strings.Contains(out, "sk-MODEL") || strings.Contains(out, "dt-TOKEN") {
		t.Errorf("plaintext duplicates survived in config.json:\n%s", out)
	}
	// The whole api_keys field goes, the way PicoClaw's own writer leaves it.
	if strings.Contains(out, `"api_keys"`) {
		t.Errorf("config.json kept an api_keys field the overlay owns:\n%s", out)
	}
	// Deleting the channel token must not take the non-secret neighbour with it.
	if !strings.Contains(out, "http://p") {
		t.Errorf("settings.proxy lost while deleting settings.token:\n%s", out)
	}
	// The model entry itself must survive — that is where provider/model live.
	if !strings.Contains(out, `"model_name": "gpt"`) || !strings.Contains(out, `"provider": "openai"`) {
		t.Errorf("model entry damaged by removing its keys:\n%s", out)
	}
	if len(res.Redundant) != 2 {
		t.Errorf("Redundant = %v, want the model list and the channel token", res.Redundant)
	}
	if !strings.Contains(res.Summary(), "duplicate") {
		t.Errorf("summary hides the removals: %s", res.Summary())
	}
}

// The opposite case, and the dangerous one: if the overlay's own copy is still
// plaintext, the config.json value is the only usable one. Deleting it would
// destroy the credential outright.
// A plaintext value already sitting in the overlay is sealed too, and the
// config.json copy goes with it. Refusing here would be the old behavior and
// would leave a readable secret in the very file meant to hold them.
func TestUnsealedOverlayCopyIsSealedNotRefused(t *testing.T) {
	key := encryptFixtureKey(t)
	dir := t.TempDir()
	scPath := filepath.Join(dir, "sidecar.yml")
	body := "model_list:\n  gpt:0:\n    api_keys:\n      - sk-OVERLAY\n"
	if err := os.WriteFile(scPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	doc, err := configdoc.Parse([]byte(`{"version":3,"model_list":[{"model_name":"gpt","api_keys":["sk-JSON"]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	res, err := EncryptWorkspace(doc, secretsFromSidecar(t, body), key, "PASS", scPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Refused) != 0 {
		t.Errorf("nothing should be refused when the overlay can be sealed: %+v", res.Refused)
	}
	if !strings.Contains(string(res.SidecarBytes), credential.Scheme) {
		t.Errorf("the overlay value was not sealed:\n%s", res.SidecarBytes)
	}
	if strings.Contains(string(res.SidecarBytes), "sk-OVERLAY") {
		t.Errorf("plaintext left in the overlay:\n%s", res.SidecarBytes)
	}
	if strings.Contains(string(doc.Marshal()), "sk-JSON") || strings.Contains(string(doc.Marshal()), "sk-OVERLAY") {
		t.Errorf("a plaintext copy survived in config.json:\n%s", doc.Marshal())
	}
}
