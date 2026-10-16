package doctor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kimus/piccolo/internal/picoclaw"
)

// sidecar writes picoclaw/.security.yml next into a workspace.
func writeSidecar(t *testing.T, dir, body string) {
	t.Helper()
	path := filepath.Join(dir, "picoclaw", picoclaw.SecurityFileName)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// stripModelKeys reproduces what upstream's SaveConfig does to config.json: the
// api_keys field disappears entirely, because SecureStrings.MarshalJSON is not
// allowed to emit the value. This is the state a Piccolo workspace reaches the
// first time anything saves through PicoClaw itself, and doctor used to read it
// as "no API key".
func stripModelKeys(t *testing.T, dir string) {
	t.Helper()
	patchConfig(t, dir, func(cfg map[string]any) {
		for _, e := range cfg["model_list"].([]any) {
			delete(e.(map[string]any), "api_keys")
		}
	})
}

// The exact false warning: a working gateway whose keys live in the sidecar.
func TestDoctorSidecarKeyIsNotAPlaceholderWarning(t *testing.T) {
	dir := initWorkspace(t)
	stripModelKeys(t, dir)
	writeSidecar(t, dir, "model_list:\n  gpt-5.4:0:\n    api_keys:\n      - sk-from-sidecar\n")

	results := Run(context.Background(), Options{Workspace: dir, Engine: fakeEngine{imageExists: true}})
	if Failed(results) {
		t.Fatalf("unexpected failure: %v", results)
	}
	if r := find(results, "placeholder or empty API key"); r != nil {
		t.Errorf("sidecar-held key warned anyway: %s", r.Message)
	}
	r := find(results, "model: gpt-5.4")
	if r == nil || r.Status != OK {
		t.Fatalf("want OK naming the sidecar, got %v", results)
	}
	if !strings.Contains(r.Message, ".security.yml") {
		t.Errorf("OK line does not say which file holds the key: %s", r.Message)
	}
	// And the sidecar itself is reported, so a user knows the second file exists.
	if find(results, "secret sidecar") == nil {
		t.Errorf("no sidecar line: %v", results)
	}
}

// The dangerous direction: config.json still DISPLAYS a key while the sidecar
// carries a different one, so the gateway runs the sidecar's and a setup edit to
// config.json silently does nothing.
func TestDoctorWarnsWhenSidecarShadowsConfigJSONKey(t *testing.T) {
	dir := initWorkspace(t) // config.json keeps its generated api_keys
	writeSidecar(t, dir, "model_list:\n  gpt-5.4:0:\n    api_keys:\n      - sk-sidecar-wins\n")

	results := Run(context.Background(), Options{Workspace: dir, Engine: fakeEngine{imageExists: true}})
	if Failed(results) {
		t.Fatalf("shadowing must warn, not fail: %v", results)
	}
	r := find(results, "config.json api_keys is ignored")
	if r == nil || r.Status != WARN {
		t.Fatalf("want shadow WARN, got %v", results)
	}
	for _, want := range []string{"gpt-5.4:0", "config.json api_keys is ignored"} {
		if !strings.Contains(r.Message, want) {
			t.Errorf("message %q missing %q", r.Message, want)
		}
	}
	// The warning must never repeat the secret itself.
	if strings.Contains(r.Message, "sk-sidecar-wins") {
		t.Errorf("warning leaked a secret value: %s", r.Message)
	}
}

// A sidecar entry with an empty list strips a key config.json still shows: the
// model looks configured and authenticates with nothing.
// Same shadow as a model_list key: config.json displays a token, the sidecar
// loads a different one, and a setup edit to config.json does nothing. Warned by
// field name only — an output line must never carry either token.
func TestDoctorWarnsWhenSidecarShadowsChannelToken(t *testing.T) {
	dir := initWorkspace(t)
	patchConfig(t, dir, func(cfg map[string]any) {
		cfg["channel_list"] = map[string]any{
			"discord": map[string]any{
				"enabled": true, "type": "discord",
				"allow_from": []any{"12345"},
				"settings":   map[string]any{"token": "dt-IN-CONFIG"},
			},
		}
	})
	writeSidecar(t, dir, "channel_list:\n  discord:\n    settings:\n      token: dt-SIDECAR-WINS\n")

	results := Run(context.Background(), Options{Workspace: dir, Engine: fakeEngine{imageExists: true}})
	r := find(results, "config.json \"token\" is ignored")
	if r == nil || r.Status != WARN {
		t.Fatalf("want channel shadow WARN, got %v", results)
	}
	for _, secret := range []string{"dt-SIDECAR-WINS", "dt-IN-CONFIG"} {
		if strings.Contains(r.Message, secret) {
			t.Errorf("shadow warning leaked %q: %s", secret, r.Message)
		}
	}
}

// The inverse of the shadow, verified against upstream's loader: a sidecar that
// names the field with an EMPTY string replaces config.json's real token, so the
// channel is enabled, looks keyed, and starts with no credential at all.
func TestDoctorSidecarEmptyTokenOverridesConfigJSON(t *testing.T) {
	dir := initWorkspace(t)
	patchConfig(t, dir, func(cfg map[string]any) {
		cfg["channel_list"] = map[string]any{
			"discord": map[string]any{
				"enabled": true, "type": "discord",
				"allow_from": []any{"12345"},
				"settings":   map[string]any{"token": "dt-LOOKS-FINE"},
			},
		}
	})
	writeSidecar(t, dir, "channel_list:\n  discord:\n    settings:\n      token: \"\"\n")

	results := Run(context.Background(), Options{Workspace: dir, Engine: fakeEngine{imageExists: true}})
	r := find(results, "sets \"token\" empty")
	if r == nil || r.Status != WARN {
		t.Fatalf("want empty-override WARN, got %v", results)
	}
	if !strings.Contains(r.Message, "no token") {
		t.Errorf("warning must state the consequence: %s", r.Message)
	}
}

// Two tokens that agree are not worth a warning: the same value in both files is
// a redundant workspace, not a broken one.
func TestDoctorIdenticalSidecarAndConfigTokenDoesNotWarn(t *testing.T) {
	dir := initWorkspace(t)
	patchConfig(t, dir, func(cfg map[string]any) {
		cfg["channel_list"] = map[string]any{
			"discord": map[string]any{
				"enabled": true, "type": "discord",
				"allow_from": []any{"12345"},
				"settings":   map[string]any{"token": "dt-same"},
			},
		}
	})
	writeSidecar(t, dir, "channel_list:\n  discord:\n    settings:\n      token: dt-same\n")

	results := Run(context.Background(), Options{Workspace: dir, Engine: fakeEngine{imageExists: true}})
	if r := find(results, "is ignored"); r != nil {
		t.Errorf("matching tokens warned anyway: %s", r.Message)
	}
	if r := find(results, "channel: discord"); r == nil || r.Status != OK {
		t.Fatalf("want OK, got %v", results)
	}
}

// A sidecar entry with an empty list strips a key config.json still shows: the
// model looks configured and authenticates with nothing.
func TestDoctorWarnsWhenSidecarClearsAKey(t *testing.T) {
	for _, body := range []string{
		"model_list:\n  gpt-5.4:0:\n    api_keys: []\n",
		"model_list:\n  gpt-5.4:0: {}\n",
	} {
		dir := initWorkspace(t)
		writeSidecar(t, dir, body)
		results := Run(context.Background(), Options{Workspace: dir, Engine: fakeEngine{imageExists: true}})
		r := find(results, "lists none")
		if r == nil || r.Status != WARN {
			t.Fatalf("body %q: want cleared WARN, got %v", body, results)
		}
		if strings.Contains(r.Message, "sk-real-key") {
			t.Errorf("message leaked the config.json key: %s", r.Message)
		}
	}
}

// A null sidecar entry is skipped by upstream's merge, so config.json's own key
// really does apply. Warning about shadowing here would be a false alarm.
func TestDoctorNullSidecarEntryDoesNotShadow(t *testing.T) {
	dir := initWorkspace(t)
	writeSidecar(t, dir, "model_list:\n  gpt-5.4:0:\n")
	results := Run(context.Background(), Options{Workspace: dir, Engine: fakeEngine{imageExists: true}})
	if Failed(results) {
		t.Fatalf("unexpected failure: %v", results)
	}
	for _, r := range results {
		if strings.Contains(r.Message, "api_keys is ignored") || strings.Contains(r.Message, "lists none") {
			t.Errorf("null entry read as a shadow: %s", r.Message)
		}
	}
	if r := find(results, "model: gpt-5.4"); r == nil || r.Status != OK {
		t.Fatalf("want OK for the config.json key, got %v", results)
	}
}

// Renaming a model in config.json strands its sidecar key. Nothing in
// config.json shows it and every reference still resolves — the symptom is an
// authentication failure two turns later.
func TestDoctorReportsOrphanedSidecarKey(t *testing.T) {
	dir := initWorkspace(t)
	patchConfig(t, dir, func(cfg map[string]any) {
		entry := cfg["model_list"].([]any)[0].(map[string]any)
		entry["model_name"] = "renamed"
		delete(entry, "api_keys")
	})
	writeSidecar(t, dir, "model_list:\n  gpt-5.4:0:\n    api_keys:\n      - sk-orphaned\n")

	results := Run(context.Background(), Options{Workspace: dir, Engine: fakeEngine{imageExists: true}})
	r := find(results, "matches no model_list entry")
	if r == nil || r.Status != WARN {
		t.Fatalf("want orphan WARN, got %v", results)
	}
	if !strings.Contains(r.Message, "gpt-5.4:0") {
		t.Errorf("orphan warning must name the key: %s", r.Message)
	}
	if strings.Contains(r.Message, "sk-orphaned") {
		t.Errorf("orphan warning leaked a secret: %s", r.Message)
	}
	// And the renamed model must be reported as the keyless one it now is.
	if find(results, `model "renamed" has a placeholder or empty API key`) == nil {
		t.Errorf("renamed model not reported as keyless: %v", results)
	}
}

// A sidecar that does not parse is not a warning: PicoClaw's LoadConfig refuses
// to start the gateway over it, and serve would die with a message about
// config.json that points at the wrong file.
func TestDoctorUnparseableSidecarFails(t *testing.T) {
	dir := initWorkspace(t)
	writeSidecar(t, dir, "model_list: [this is: not: valid: yaml\n")

	results := Run(context.Background(), Options{Workspace: dir, Engine: fakeEngine{imageExists: true}})
	if !Failed(results) {
		t.Fatalf("malformed sidecar must fail the run: %v", results)
	}
	r := find(results, ".security.yml does not parse")
	if r == nil || r.Status != FAIL {
		t.Fatalf("want parse FAIL, got %v", results)
	}
}

func TestDoctorChannelTokenFromSidecar(t *testing.T) {
	dir := initWorkspace(t)
	// Add a discord channel with no token in config.json (moved to the sidecar),
	// which is exactly what upstream's save produces.
	patchConfig(t, dir, func(cfg map[string]any) {
		cfg["channel_list"] = map[string]any{
			"discord": map[string]any{
				"enabled": true, "type": "discord",
				"allow_from": []any{"12345"},
				"settings":   map[string]any{"proxy": ""},
			},
		}
	})
	writeSidecar(t, dir, "channel_list:\n  discord:\n    settings:\n      token: dt-from-sidecar\n")

	results := Run(context.Background(), Options{Workspace: dir, Engine: fakeEngine{imageExists: true}})
	if r := find(results, "has no token in settings"); r != nil {
		t.Errorf("sidecar-held channel token warned anyway: %s", r.Message)
	}
	r := find(results, "channel: discord")
	if r == nil || r.Status != OK {
		t.Fatalf("want OK channel line, got %v", results)
	}
	if !strings.Contains(r.Message, ".security.yml") {
		t.Errorf("channel line must say where the token is: %s", r.Message)
	}
	if strings.Contains(r.Message, "dt-from-sidecar") {
		t.Errorf("channel line leaked the token: %s", r.Message)
	}
}

// The sentinel is PicoClaw writing "the value lives in the sidecar". With no
// sidecar value behind it the channel starts with no token, and that is a
// warning — not the generic placeholder wording, which sends the user to the
// wrong file.
func TestDoctorSentinelWithoutSidecarValue(t *testing.T) {
	dir := initWorkspace(t)
	patchConfig(t, dir, func(cfg map[string]any) {
		cfg["channel_list"] = map[string]any{
			"discord": map[string]any{
				"enabled": true, "type": "discord",
				"allow_from": []any{"12345"},
				"settings":   map[string]any{"token": "[NOT_HERE]"},
			},
		}
	})
	writeSidecar(t, dir, "channel_list:\n  discord:\n    settings: {}\n")

	results := Run(context.Background(), Options{Workspace: dir, Engine: fakeEngine{imageExists: true}})
	r := find(results, "config.json marks")
	if r == nil || r.Status != WARN {
		t.Fatalf("want sentinel WARN naming the gap, got %v", results)
	}
	if !strings.Contains(r.Message, ".security.yml") {
		t.Errorf("sentinel warning must name the sidecar: %s", r.Message)
	}
}

// Acceptance criterion 4 again, from the other side: an untouched workspace has
// no sidecar at all, and none of the new checks may fire there.
func TestDoctorNoSidecarStaysClean(t *testing.T) {
	dir := initWorkspace(t)
	if _, err := os.Stat(filepath.Join(dir, "picoclaw", picoclaw.SecurityFileName)); err == nil {
		t.Fatal("fixture unexpectedly has a sidecar")
	}
	results := Run(context.Background(), Options{
		Workspace: dir,
		Engine:    fakeEngine{imageExists: true},
		LookupEnv: func(string) (string, bool) { return "", false },
	})
	if Failed(results) {
		t.Fatalf("unexpected failures: %v", results)
	}
	if c := statuses(results); c[WARN] != 0 {
		t.Fatalf("unexpected warnings: %v", results)
	}
	for _, r := range results {
		for _, bad := range []string{"sidecar", ".security.yml", "overridden", "matches no model_list"} {
			if strings.Contains(r.Message, bad) {
				t.Errorf("no-sidecar workspace mentioned %q: %s", bad, r.Message)
			}
		}
	}
	// The generated key still reads as a key.
	if r := find(results, "model: gpt-5.4"); r == nil || r.Status != OK || strings.Contains(r.Message, "security.yml") {
		t.Errorf("plain config.json key mis-reported: %v", results)
	}
}

// Duplicate aliases are the case the occurrence-counted keys exist for: each
// entry must be judged by its own sidecar key, not by the first one sharing the
// name.
func TestDoctorDuplicateAliasesResolvePerEntry(t *testing.T) {
	dir := initWorkspace(t)
	patchConfig(t, dir, func(cfg map[string]any) {
		first := cfg["model_list"].([]any)[0].(map[string]any)
		clone := map[string]any{
			"model_name": first["model_name"],
			"provider":   "openai",
			"model":      "gpt-4o",
		}
		delete(first, "api_keys")
		cfg["model_list"] = append(cfg["model_list"].([]any), clone)
	})
	// Only the SECOND occurrence has a key in the sidecar.
	writeSidecar(t, dir, "model_list:\n  gpt-5.4:1:\n    api_keys:\n      - sk-second\n")

	results := Run(context.Background(), Options{Workspace: dir, Engine: fakeEngine{imageExists: true}})
	var okLines, warnLines int
	for _, r := range results {
		// Count the per-entry lines only: "default model: gpt-5.4" and the image
		// model line name the same alias and are not statements about its key. Only
		// the per-entry line carries "(<model id>)" after the alias.
		if strings.Contains(r.Message, "model: gpt-5.4 (") && r.Status == OK {
			okLines++
		}
		if strings.Contains(r.Message, "placeholder or empty API key") {
			warnLines++
		}
	}
	if okLines != 1 || warnLines != 1 {
		t.Fatalf("want 1 OK + 1 warn across the two entries, got %d/%d: %v", okLines, warnLines, results)
	}
}
