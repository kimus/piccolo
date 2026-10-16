package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/kimus/piccolo/internal/configdoc"
	"github.com/kimus/piccolo/internal/credential"
	"github.com/kimus/piccolo/internal/picoclaw"
)

// docWith returns a config.json carrying a model key and a discord token.
func docWith(t *testing.T) *configdoc.Doc {
	t.Helper()
	d, err := configdoc.Parse([]byte(`{"version":3,
	  "agents":{"defaults":{"model_name":"gpt","image_model":"gpt"}},
	  "model_list":[{"model_name":"gpt","provider":"openai","model":"gpt-4o","api_keys":["sk-MODEL"]}],
	  "channel_list":{"discord":{"enabled":true,"type":"discord","allow_from":["1"],"settings":{"token":"dt-TOKEN","proxy":"http://p"}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func sidecarIn(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "sidecar.yml")
}

// The rule: a credential lives in the overlay or in config.json, never both. The
// overlay wins on load, so a copy left in config.json is unreadable-in-principle
// by the gateway yet perfectly readable by anyone with the directory.
func TestMoveLeavesCredentialsOnlyInTheOverlay(t *testing.T) {
	key := encryptFixtureKey(t)
	scPath := sidecarIn(t)
	doc := docWith(t)

	res, err := EncryptWorkspace(doc, picoclaw.Secrets{}, key, "PASS", scPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Refused) != 0 {
		t.Fatalf("nothing should be refused: %+v", res.Refused)
	}

	// config.json: no credentials anywhere, but the entries themselves survive.
	out := string(doc.Marshal())
	for _, gone := range []string{"sk-MODEL", "dt-TOKEN"} {
		if strings.Contains(out, gone) {
			t.Errorf("%q left in config.json:\n%s", gone, out)
		}
	}
	if !strings.Contains(out, `"provider": "openai"`) || !strings.Contains(out, `"model_name": "gpt"`) {
		t.Errorf("model entry damaged by moving its key:\n%s", out)
	}
	if !strings.Contains(out, "http://p") {
		t.Errorf("non-secret settings.proxy lost while moving settings.token:\n%s", out)
	}
	// Both values are in the overlay, sealed. The caller persists SidecarBytes,
	// exactly as piccolo secrets encrypt does.
	if len(res.SidecarBytes) == 0 {
		t.Fatal("no overlay bytes produced")
	}
	if err := os.WriteFile(scPath, res.SidecarBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	scBytes := res.SidecarBytes
	sc := string(scBytes)
	if strings.Count(sc, credential.Scheme) != 2 {
		t.Errorf("expected 2 sealed values in the overlay:\n%s", sc)
	}
	if !strings.Contains(sc, "gpt:0:") {
		t.Errorf("overlay key must be the ordinal form PicoClaw's merge computes:\n%s", sc)
	}
	// And both decrypt back to what was there.
	var parsed struct {
		ModelList map[string]struct {
			APIKeys []string `yaml:"api_keys"`
		} `yaml:"model_list"`
		ChannelList map[string]struct {
			Settings map[string]string `yaml:"settings"`
		} `yaml:"channel_list"`
	}
	if err := yaml.Unmarshal(scBytes, &parsed); err != nil {
		t.Fatalf("overlay unreadable: %v\n%s", err, sc)
	}
	got := parsed.ModelList["gpt:0"].APIKeys[0]
	if plain, err := credential.Decrypt("PASS", key, got); err != nil || plain != "sk-MODEL" {
		t.Errorf("model key decrypt = %q, %v", plain, err)
	}
	tok := parsed.ChannelList["discord"].Settings["token"]
	if plain, err := credential.Decrypt("PASS", key, tok); err != nil || plain != "dt-TOKEN" {
		t.Errorf("channel token decrypt = %q, %v", plain, err)
	}
}

// The reporting rule: a move is a removal, not an encryption. A user who greps
// config.json for ciphertext and finds none must be told where it went.
func TestMoveIsReportedAsRemovalNotEncryption(t *testing.T) {
	key := encryptFixtureKey(t)
	doc := docWith(t)
	res, err := EncryptWorkspace(doc, picoclaw.Secrets{}, key, "PASS", sidecarIn(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Redundant) != 2 {
		t.Fatalf("Redundant = %v, want model and channel", res.Redundant)
	}
	sum := res.Summary()
	if !strings.Contains(sum, "duplicate") || strings.Contains(sum, "2 credential(s) encrypted") {
		t.Errorf("summary miscounts moved values: %s", sum)
	}
}

// Without somewhere to move them to, credentials must not be stripped: deleting
// config.json's copy with no overlay to receive it would destroy the credential.
func TestMoveOnlyDeletesWhatLanded(t *testing.T) {
	key := encryptFixtureKey(t)
	doc := docWith(t)
	dir := t.TempDir()
	scPath := filepath.Join(dir, "sidecar.yml")
	// A directory where the overlay belongs makes every seal fail.
	if err := os.MkdirAll(scPath, 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := EncryptWorkspace(doc, picoclaw.Secrets{}, key, "PASS", scPath)
	if err == nil {
		t.Fatal("expected the overlay write to fail against a directory")
	}
	// A failed seal must not have stripped config.json: the deletion happens only
	// for values the overlay accepted.
	out := string(doc.Marshal())
	if !strings.Contains(out, "sk-MODEL") || !strings.Contains(out, "dt-TOKEN") {
		t.Errorf("credentials removed although they never reached an overlay:\n%s", out)
	}
}

// No credentials to move and no existing overlay: the file must not appear.
// PicoClaw creates it on its own saves; Piccolo creating an empty one would be
// noise in a workspace that never asked for it.
func TestNoMoveCreatesNoOverlay(t *testing.T) {
	key := encryptFixtureKey(t)
	doc, err := configdoc.Parse([]byte(`{"version":3,"model_list":[{"model_name":"m","provider":"openai","model":"x"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	scPath := sidecarIn(t)
	if _, err := EncryptWorkspace(doc, picoclaw.Secrets{}, key, "PASS", scPath); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(scPath); err == nil {
		t.Error("an overlay was created with nothing to put in it")
	}
}

// Running again must be inert: the overlay already holds ciphertext and
// config.json holds nothing, so a second pass rewriting the file is churn that
// makes a workspace diff look like a change happened.
func TestMoveIsIdempotent(t *testing.T) {
	key := encryptFixtureKey(t)
	doc := docWith(t)
	scPath := sidecarIn(t)
	res, err := EncryptWorkspace(doc, picoclaw.Secrets{}, key, "PASS", scPath)
	if err != nil {
		t.Fatal(err)
	}
	firstDoc := string(doc.Marshal())
	firstSc := res.SidecarBytes
	if err := os.WriteFile(scPath, firstSc, 0o600); err != nil {
		t.Fatal(err)
	}
	// Re-read the overlay the way a later run would.
	cwd := t.TempDir()
	cfgPath := filepath.Join(cwd, "config.json")
	if err := os.WriteFile(cfgPath, doc.Marshal(), 0o600); err != nil {
		t.Fatal(err)
	}
	res2, err := EncryptWorkspace(doc, picoclaw.LoadSecrets(cfgPath), key, "PASS", scPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(doc.Marshal()) != firstDoc {
		t.Error("second pass altered config.json")
	}
	if len(res2.Changed) != 0 || len(res2.Pruned) != 0 {
		t.Errorf("second pass reported changes: %+v", res2)
	}
	if second := string(mustRead(t, scPath)); second != string(firstSc) {
		t.Errorf("second pass rewrote the overlay:\n%s", second)
	}
}

// An overlay key no config.json entry can reach holds a token nothing loads, and
// it survives every later rename — the state that leaves unused credentials in
// the secret file. It must be pruned by the same pass that moves values in.
func TestMovePrunesUnreachableOverlayKeys(t *testing.T) {
	key := encryptFixtureKey(t)
	dir := t.TempDir()
	scPath := filepath.Join(dir, "sidecar.yml")
	if err := os.WriteFile(scPath, []byte(
		"model_list:\n  gpt:0:\n    api_keys:\n      - \"enc://KEEPME\"\n  deleted-model:0:\n    api_keys:\n      - sk-UNUSED-TOKEN\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	doc := docWith(t)
	res, err := EncryptWorkspace(doc, picoclaw.LoadSecrets(filepath.Join(dir, "config.json")), key, "PASS", scPath)
	if err != nil {
		t.Fatal(err)
	}
	after := string(res.SidecarBytes)
	if strings.Contains(after, "deleted-model") || strings.Contains(after, "sk-UNUSED-TOKEN") {
		t.Errorf("unreachable overlay entry survived:\n%s", after)
	}
	if !strings.Contains(after, "gpt:0:") {
		t.Errorf("live model key was pruned:\n%s", after)
	}
	// The live entry's old value is replaced by the one moving in from
	// config.json — the overlay holds one copy per field, not a list of history.
	if strings.Contains(after, "KEEPME") {
		t.Errorf("the moved value did not replace the stale one:\n%s", after)
	}
	if len(res.Pruned) != 1 || res.Pruned[0] != "deleted-model:0" {
		t.Errorf("Pruned = %v, want the one unreachable key", res.Pruned)
	}
	if !strings.Contains(res.Summary(), "pruned") {
		t.Errorf("summary omits the pruning: %s", res.Summary())
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
