package doctor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kimus/piccolo/internal/credential"
	"github.com/kimus/piccolo/internal/generate"
	"github.com/kimus/piccolo/internal/manifest"
	"github.com/kimus/piccolo/internal/picoclaw"
	"github.com/kimus/piccolo/internal/workspace"
)

// initEncryptedWorkspace produces the same state `piccolo init` leaves behind
// now that encryption is the default — key file, ciphertext in config.json, recorded route — built
// through the generators rather than by hand, so a fixture cannot drift from what
// init actually writes.
func initEncryptedWorkspace(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	in := generate.Inputs{
		AssistantName: "Piccolo", OwnerName: "Kimus", Purpose: "personal",
		Model:       generate.ModelInput{Alias: "gpt-5.4", Provider: "openai", Model: "gpt-4o", APIKey: "sk-real-key"},
		WebSearch:   true,
		EncryptKeys: true,
		Passphrase:  "fixture-passphrase",
	}
	// Encryption at render time needs a key, which init generates after the plan is
	// confirmed; do the same here so config.json carries a real ciphertext.
	layout := workspace.NewLayout(dir)
	if err := layout.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	keyPath, err := credential.Generate(layout.Keys())
	if err != nil {
		t.Fatal(err)
	}
	in.Encrypt = func(plain string) (string, error) {
		return credential.Encrypt("fixture-passphrase", keyPath, plain)
	}
	plan, err := generate.BuildPlan(in)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := plan.Write(dir, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := generate.EnsureGitignore(dir); err != nil {
		t.Fatal(err)
	}
	return dir
}

// dropPassphrase removes secrets.passphrase from piccolo.yaml, leaving the
// ciphertext behind — the drift a workspace clone or a hand edit produces.
func dropPassphrase(t *testing.T, dir string) {
	t.Helper()
	path := filepath.Join(dir, "piccolo.yaml")
	m, err := manifest.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	m.Secrets.Passphrase = ""
	if err := m.Write(path); err != nil {
		t.Fatal(err)
	}
}

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

// Upstream's own example placeholder shape, warned about exactly as the hyphenated
// one always was. Before this rule an untouched config.example.json value read as a
// usable credential and doctor stayed quiet on a workspace with no real key.
func TestDoctorWarnsOnUntouchedExampleToken(t *testing.T) {
	for _, tc := range []struct {
		name  string
		patch func(map[string]any)
		want  string
	}{
		{
			"model key",
			func(cfg map[string]any) {
				cfg["model_list"].([]any)[0].(map[string]any)["api_keys"] = []any{"YOUR_OPENAI_KEY"}
			},
			"placeholder or empty API key",
		},
		{
			"channel token",
			func(cfg map[string]any) {
				cfg["channel_list"] = map[string]any{
					"discord": map[string]any{
						"enabled": true, "type": "discord",
						"allow_from": []any{"12345"},
						"settings":   map[string]any{"token": "YOUR_DISCORD_BOT_TOKEN"},
					},
				}
			},
			`is empty or a placeholder`,
		},
	} {
		dir := initWorkspace(t)
		patchConfig(t, dir, tc.patch)
		results := Run(context.Background(), Options{Workspace: dir, Engine: fakeEngine{imageExists: true}})
		if Failed(results) {
			t.Fatalf("%s: example token must warn, not fail: %v", tc.name, results)
		}
		if r := find(results, tc.want); r == nil || r.Status != WARN {
			t.Errorf("%s: no %q warning in %v", tc.name, tc.want, results)
		}
	}
}

// The narrowness guarantee, at the level a user feels it: a real credential in a
// normal shape must still read as configured. Without this the template rule is a
// free way to make every workspace warn, and nobody would notice until they
// ignored the warnings.
func TestDoctorStillAcceptsRealCredentialShapes(t *testing.T) {
	for _, real := range []string{"sk-proj-abc123", "xoxb-123-456-abc", "ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZabcdef123", "AKIAIOSFODNN7EXAMPLE"} {
		dir := initWorkspace(t)
		patchConfig(t, dir, func(cfg map[string]any) {
			cfg["model_list"].([]any)[0].(map[string]any)["api_keys"] = []any{real}
		})
		results := Run(context.Background(), Options{Workspace: dir, Engine: fakeEngine{imageExists: true}})
		if r := find(results, "placeholder or empty API key"); r != nil {
			t.Errorf("%q was flagged as a placeholder: %s", real, r.Message)
		}
		if find(results, "model: gpt-5.4") == nil {
			t.Errorf("%q: model line missing entirely: %v", real, results)
		}
	}
}

// PicoClaw models more channel secrets than the primary bot token — line has
// channel_secret/channel_access_token, matrix and onebot have access_token, and
// several have webhook_url. A credential check that only looks at "token" leaves
// those in plaintext AND stays silent about them, which is the
// silent-under-protection shape this whole area exists to close.
func TestDoctorChecksSecondaryChannelSecrets(t *testing.T) {
	for _, field := range picoclaw.SecretFieldName {
		dir := initWorkspace(t)
		patchConfig(t, dir, func(cfg map[string]any) {
			cfg["channel_list"] = map[string]any{
				"line": map[string]any{
					"enabled": true, "type": "line",
					"allow_from": []any{"12345"},
					"settings":   map[string]any{field: "YOUR_LINE_SECRET"},
				},
			}
		})
		results := Run(context.Background(), Options{Workspace: dir, Engine: fakeEngine{imageExists: true}})
		r := find(results, "empty or a placeholder")
		if r == nil || r.Status != WARN {
			t.Errorf("field %q: no placeholder warning — the credential list does not reach it: %v", field, results)
			continue
		}
		if !strings.Contains(r.Message, fmt.Sprintf("%q", field)) {
			t.Errorf("field %q: warning does not name the field: %s", field, r.Message)
		}
	}
}

// A workspace whose key was deleted after encryption looks identical to a healthy
// one from config.json, and fails at boot with an error saying "cannot read SSH
// key". doctor and status are where a user looks when the gateway misbehaves.
func TestDoctorReportsCredentialDeliveryState(t *testing.T) {

	// Healthy encrypted workspace.
	dir := initEncryptedWorkspace(t)
	results := Run(context.Background(), Options{Workspace: dir, Engine: fakeEngine{imageExists: true}})
	if r := find(results, "credential encryption"); r == nil || r.Status != OK {
		t.Fatalf("healthy encrypted workspace not reported OK: %v", results)
	}
	// The honest half: it must say the passphrase sits in a committed file.
	if r := find(results, "credential encryption"); !strings.Contains(r.Message, "committed") {
		t.Errorf("encryption line omits the piccolo.yaml exposure: %s", r.Message)
	}
	if r := find(results, "placeholder or empty API key"); r != nil {
		t.Errorf("the enc:// value was judged an empty key: %s", r.Message)
	}

	// Passphrase removed from piccolo.yaml.
	dropPassphrase(t, dir)
	results = Run(context.Background(), Options{Workspace: dir, Engine: fakeEngine{imageExists: true}})
	r := find(results, "no secrets.passphrase")
	if r == nil || r.Status != WARN {
		t.Fatalf("missing passphrase not warned: %v", results)
	}
	// Never phrase it as a broken config file: that sends the user to the wrong
	// file, which is the misdirection this check exists to remove.
	if strings.Contains(r.Message, "does not parse") || strings.Contains(r.Message, "malformed") {
		t.Errorf("misattributed to config.json: %s", r.Message)
	}
	if Failed(results) {
		t.Error("a missing passphrase must warn, not fail: doctor stays a report, serve is the gate")
	}

	// Key file removed.
	if err := os.RemoveAll(filepath.Join(dir, "picoclaw", "keys")); err != nil {
		t.Fatal(err)
	}
	results = Run(context.Background(), Options{Workspace: dir, Engine: fakeEngine{imageExists: true}})
	r = find(results, "key file")
	if r == nil || r.Status != WARN {
		t.Fatalf("missing key file not warned: %v", results)
	}
	if !strings.Contains(r.Message, "derives its AES key") {
		t.Errorf("warning should explain why the file matters: %s", r.Message)
	}
}

// A plaintext workspace gets no credential-encryption finding at all. Warning on
// every non-opted-in workspace is how warnings get ignored everywhere.
func TestDoctorSilentOnUnencryptedWorkspace(t *testing.T) {
	dir := initWorkspace(t)
	results := Run(context.Background(), Options{Workspace: dir, Engine: fakeEngine{imageExists: true}, LookupEnv: func(string) (string, bool) { return "", false }})
	if r := find(results, "credential encryption"); r != nil {
		t.Errorf("plaintext workspace got an encryption finding: %s", r.Message)
	}
	if r := find(results, "enc://"); r != nil {
		t.Errorf("plaintext workspace was warned about enc://: %s", r.Message)
	}
}

// Task 6.3: no doctor line may carry a credential, in either form. Run over every
// state an encrypted workspace can be in and assert on the whole output, since a
// leaked value in one branch among many is exactly what a per-line test misses.
func TestDoctorNeverPrintsCredentialValues(t *testing.T) {
	dir := initEncryptedWorkspace(t)
	secret := "sk-MUST-NOT-APPEAR"
	// Put a plaintext credential in too, so both forms are present at once.
	patchConfig(t, dir, func(cfg map[string]any) {
		cfg["channel_list"] = map[string]any{
			"discord": map[string]any{
				"enabled": true, "type": "discord",
				"allow_from": []any{"1"},
				"settings":   map[string]any{"token": secret},
			},
		}
	})
	writeSidecar(t, dir, "model_list:\n  gpt-5.4:0:\n    api_keys:\n      - sk-SIDECAR-SECRET\n")

	for name, lookup := range map[string]func(string) (string, bool){
		"passphrase set":   func(string) (string, bool) { return "pw", true },
		"passphrase unset": func(string) (string, bool) { return "", false },
	} {
		results := Run(context.Background(), Options{Workspace: dir, Engine: fakeEngine{imageExists: true}, LookupEnv: lookup})
		for _, r := range results {
			for _, forbidden := range []string{secret, "sk-SIDECAR-SECRET", "pw"} {
				if strings.Contains(r.Message, forbidden) {
					t.Errorf("%s: output leaked %q: %s", name, forbidden, r.Message)
				}
			}
		}
	}
}

// config.json holding a readable key for a field the overlay owns is a dead
// duplicate: never loaded, still plaintext on disk. The message must say the value
// is dead and offer the way to drop it, not merely "edit the key there" — which
// leaves a user with a readable copy they never chose to keep.
func TestDoctorReportsDeadReadableCopyOwnedByOverlay(t *testing.T) {
	dir := initEncryptedWorkspace(t)
	writeSidecar(t, dir, "model_list:\n  gpt-5.4:0:\n    api_keys:\n      - \"enc://AAAA\"\n")
	patchConfig(t, dir, func(cfg map[string]any) {
		cfg["model_list"].([]any)[0].(map[string]any)["api_keys"] = []any{"sk-DEAD-DUPLICATE"}
	})

	results := Run(context.Background(), Options{Workspace: dir, Engine: fakeEngine{imageExists: true}})
	r := find(results, "is ignored")
	if r == nil || r.Status != WARN {
		t.Fatalf("dead duplicate not reported: %v", results)
	}
	for _, want := range []string{"never loaded", "secrets encrypt", "gpt-5.4:0"} {
		if !strings.Contains(r.Message, want) {
			t.Errorf("message must contain %q: %s", want, r.Message)
		}
	}
	if strings.Contains(r.Message, "sk-DEAD-DUPLICATE") {
		t.Errorf("warning leaked the credential value: %s", r.Message)
	}

	// Once the dead copy is gone, neither the duplicate warning nor a false
	// "no API key" warning may remain: the key legitimately lives only in the
	// overlay, and that is the state PicoClaw's own writer produces.
	patchConfig(t, dir, func(cfg map[string]any) {
		delete(cfg["model_list"].([]any)[0].(map[string]any), "api_keys")
	})
	results = Run(context.Background(), Options{Workspace: dir, Engine: fakeEngine{imageExists: true}})
	if find(results, "is ignored") != nil {
		t.Errorf("finding persisted after the copy was removed: %v", results)
	}
	if r := find(results, "placeholder or empty API key"); r != nil {
		t.Errorf("overlay-only key wrongly called keyless: %s", r.Message)
	}
	if r := find(results, "key from picoclaw/.security.yml"); r == nil || r.Status != OK {
		t.Errorf("overlay-only key not reported as present: %v", results)
	}
}
