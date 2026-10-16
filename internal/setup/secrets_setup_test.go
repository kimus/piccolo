package setup

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kimus/piccolo/internal/configdoc"
	"github.com/kimus/piccolo/internal/picoclaw"
)

// secretsFrom writes a sidecar body in a temp dir beside a config.json and
// returns the parsed Secrets, so these tests read the same bytes upstream writes
// rather than a struct built by hand.
func secretsFrom(t *testing.T, body string) picoclaw.Secrets {
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

// Models must report the KEY STATE THE GATEWAY WILL HAVE, not what config.json
// happens to display. Each case is a different way for those two to disagree.
func TestModelsKeyStateWithSidecar(t *testing.T) {
	sidecarWith := secretsFrom(t, "model_list:\n  gpt-5.4:0:\n    api_keys:\n      - sk-sidecar\n")
	sidecarEmpty := secretsFrom(t, "model_list:\n  gpt-5.4:0:\n    api_keys: []\n")
	sidecarNull := secretsFrom(t, "model_list:\n  gpt-5.4:0:\n")

	// userConfig carries api_keys ["sk-early"] for gpt-5.4.
	if m := Models(parse(t), picoclaw.Secrets{})[0]; !m.KeySet || m.Shield.Matched || !m.EffectiveKeySet() {
		t.Errorf("no sidecar: %+v", m)
	}
	// config.json has a key AND the sidecar has one: the model does have a key, so
	// EffectiveKeySet stays true — what changed is WHERE it is decided.
	m := Models(parse(t), sidecarWith)[0]
	if !m.Shield.Matched || !m.EffectiveKeySet() {
		t.Errorf("sidecar supplies the key: %+v", m)
	}
	// The trap: config.json displays a key, the sidecar clears the field, and the
	// gateway runs with none.
	m = Models(parse(t), sidecarEmpty)[0]
	if !m.KeySet {
		t.Fatal("fixture lost its config.json key")
	}
	if m.EffectiveKeySet() {
		t.Errorf("cleared model must report as keyless: %+v", m)
	}
	// A null sidecar entry is skipped upstream, so config.json's key really applies.
	m = Models(parse(t), sidecarNull)[0]
	if m.Shield.Matched || !m.EffectiveKeySet() {
		t.Errorf("null entry treated as a shadow: %+v", m)
	}
}

// An untouched key field (`"[NOT_HERE]"`) is not a usable key: it is the marker
// PicoClaw writes when the value moved to the sidecar.
func TestModelsSentinelIsNotKeySet(t *testing.T) {
	d, err := configdoc.Parse([]byte(`{
      "version": 3,
      "model_list": [
        {"model_name": "gpt-5.4", "model": "gpt-5.4", "api_keys": ["[NOT_HERE]"]}
      ]}`))
	if err != nil {
		t.Fatal(err)
	}
	m := Models(d, picoclaw.Secrets{})[0]
	if m.KeySet {
		t.Error("[NOT_HERE] counted as a key")
	}
	if m.EffectiveKeySet() {
		t.Error("sentinel-only model reported as keyed with no sidecar behind it")
	}
	// With the sidecar behind it, the same entry is correctly keyed.
	m = Models(d, secretsFrom(t, "model_list:\n  gpt-5.4:0:\n    api_keys:\n      - sk-real\n"))[0]
	if !m.EffectiveKeySet() {
		t.Errorf("sidecar-backed sentinel reported keyless: %+v", m)
	}
}

// ModelShield finds the entry a menu offers to edit; ShieldAfterWrite answers for
// the alias the entry will HAVE after the write, which is the question a rename
// has to ask.
func TestShieldAfterWriteRenameTargets(t *testing.T) {
	d := parse(t)
	sidecar := secretsFrom(t, "model_list:\n  spare:0:\n    api_keys:\n      - sk-spare\n  gpt-5.4:0:\n    api_keys:\n      - sk-old\n")

	// Editing in place: the old alias is the one under the existing key.
	if s := ModelShield(d, sidecar, "gpt-5.4"); !s.Matched || s.Key != "gpt-5.4:0" {
		t.Errorf("ModelShield: %+v", s)
	}
	// Renaming onto a name the sidecar already covers: the write lands shadowed,
	// and a check against the OLD name would have missed it entirely.
	if s := ShieldAfterWrite(d, sidecar, "gpt-5.4", "spare"); !s.Matched || s.Key != "spare:0" {
		t.Errorf("rename onto covered alias: %+v", s)
	}
	// Renaming to a fresh name: nothing covers it, so a config.json key WILL apply.
	if s := ShieldAfterWrite(d, sidecar, "gpt-5.4", "brand-new"); s.Matched {
		t.Errorf("fresh alias reported as shadowed: %+v", s)
	}
	// Adding a model whose name the sidecar already guards.
	if s := ShieldAfterWrite(d, sidecar, "", "spare"); !s.Matched {
		t.Errorf("added alias: %+v", s)
	}
	// Unknown alias resolves clean rather than panicking.
	if s := ModelShield(d, sidecar, "nope"); s.Matched {
		t.Errorf("unknown alias: %+v", s)
	}
}

// Duplicate aliases are occurrence-counted, so a shield must attach to the right
// position — the second entry's key is not the first's.
func TestModelsDuplicateAliasOccurrence(t *testing.T) {
	d, err := configdoc.Parse([]byte(`{
      "version": 3,
      "model_list": [
        {"model_name": "dup", "model": "a", "api_keys": ["sk-one"]},
        {"model_name": "dup", "model": "b"}
      ]}`))
	if err != nil {
		t.Fatal(err)
	}
	sidecar := secretsFrom(t, "model_list:\n  dup:1:\n    api_keys:\n      - sk-second\n")
	models := Models(d, sidecar)
	if models[0].Shield.Matched {
		t.Errorf("first entry claimed by the :1 key: %+v", models[0].Shield)
	}
	if !models[1].Shield.Matched || !models[1].EffectiveKeySet() {
		t.Errorf("second entry not resolved to its own key: %+v", models[1].Shield)
	}
}
