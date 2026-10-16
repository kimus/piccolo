package manifest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseAcceptsSecretsMetadata(t *testing.T) {
	m, err := Parse(strings.NewReader(validYAML + `secrets:
  passphrase: "horse battery"
`))
	if err != nil {
		t.Fatalf("Parse secrets metadata: %v", err)
	}
	if m.Passphrase() != "horse battery" {
		t.Fatalf("passphrase = %q", m.Secrets.Passphrase)
	}
	// An absent block means an unencrypted workspace, not an error.
	plain, err := Parse(strings.NewReader(validYAML))
	if err != nil || plain.Passphrase() != "" {
		t.Fatalf("no secrets block: %q %v", plain.Passphrase(), err)
	}
}

// The passphrase may be any string, including one that looks like a variable name
// or contains spaces — it is a secret, not an identifier. Only whitespace-only is
// refused, because that reads as "encrypted" while making decryption impossible.
func TestParseAcceptsAnyPassphraseShape(t *testing.T) {
	for _, v := range []string{"hunter2", "correct horse battery staple", "PICCOLO_KEY_PASSPHRASE", "p\tc\n", "ünïcødé"} {
		_, err := Parse(strings.NewReader(validYAML + "secrets:\n  passphrase: " + quoteIf(v) + "\n"))
		if err != nil {
			t.Errorf("passphrase %q rejected: %v", v, err)
		}
	}
}

func TestValidateRejectsWhitespaceOnlyPassphrase(t *testing.T) {
	for _, v := range []string{" ", "\t", "   \n"} {
		_, err := Parse(strings.NewReader(validYAML + "secrets:\n  passphrase: " + quoteIf(v) + "\n"))
		if err == nil {
			t.Errorf("whitespace-only passphrase %q accepted", v)
			continue
		}
		if !strings.Contains(err.Error(), "secrets.passphrase") {
			t.Errorf("error must name the field: %v", err)
		}
	}
}

func quoteIf(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\t", `\t`).Replace(s) + `"`
}

// An unknown sibling under secrets: must still be refused. Strict decoding is what
// makes a typo here loud.
func TestParseRejectsUnknownSecretsKey(t *testing.T) {
	_, err := Parse(strings.NewReader(validYAML + "secrets:\n  passphrase: hunter2\n  passphrase_env: PICCOLO_KEY_PASSPHRASE\n"))
	if err == nil {
		t.Fatal("the retired passphrase_env key must be rejected, not silently ignored: a workspace that still uses it would look encrypted and not be")
	}
}

// Write must not invent an empty secrets block in every workspace's piccolo.yaml.
func TestWriteOmitsEmptySecrets(t *testing.T) {
	m, err := Parse(strings.NewReader(validYAML))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "piccolo.yaml")
	if err := m.Write(path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "secrets:") {
		t.Errorf("unencrypted workspace rendered a secrets block:\n%s", data)
	}
	// And the passphrase survives a write/load cycle unchanged.
	m.Secrets.Passphrase = "horse battery"
	if err := m.Write(path); err != nil {
		t.Fatal(err)
	}
	back, err := Load(path)
	if err != nil || back.Passphrase() != "horse battery" {
		t.Fatalf("round trip = %q, %v", back.Passphrase(), err)
	}
}

// The passphrase is now YAML content, and the awkward shapes are exactly the ones
// a passphrase can legitimately contain: spaces, a `#` that YAML would read as a
// comment, a `: ` that would parse as a mapping, quotes, leading whitespace. A
// silent mangling here would encrypt with one value and decrypt with another,
// failing only at gateway boot with no hint that the file altered the secret.
func TestPassphraseSurvivesYamlRoundTrip(t *testing.T) {
	for _, want := range []string{
		"correct horse battery staple",
		"p@ss: #word",
		"trailing ",
		" leading",
		`it's "quoted"`,
		"# starts as a comment",
		"- looks like a list",
		"tab\there",
		"ünïcødé ✓",
	} {
		dir := t.TempDir()
		path := filepath.Join(dir, "piccolo.yaml")
		m, err := Parse(strings.NewReader(validYAML))
		if err != nil {
			t.Fatal(err)
		}
		m.Secrets.Passphrase = want
		if err := m.Write(path); err != nil {
			t.Fatalf("%q: write: %v", want, err)
		}
		back, err := Load(path)
		if err != nil {
			t.Fatalf("%q: load: %v", want, err)
		}
		if back.Passphrase() != want {
			t.Errorf("passphrase mangled by the round trip: got %q want %q", back.Passphrase(), want)
		}
	}
}

// The mode has to be asserted, not merely requested: os.WriteFile applies its perm
// argument only when CREATING, so a workspace created before this rule kept a
// world-readable piccolo.yaml — holding the passphrase — through every later write.
func TestWriteTightensAnExistingReadableFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "piccolo.yaml")
	// Simulate a pre-existing 0644 file, as an old workspace has on disk.
	if err := os.WriteFile(path, []byte(validYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := mustPerm(t, path); got&0o077 == 0 {
		t.Fatalf("fixture is already private (%o); the test proves nothing", got)
	}

	m, err := Parse(strings.NewReader(validYAML))
	if err != nil {
		t.Fatal(err)
	}
	m.Secrets.Passphrase = "horse"
	if err := m.Write(path); err != nil {
		t.Fatal(err)
	}
	if got := mustPerm(t, path); got&0o077 != 0 {
		t.Errorf("rewrite left piccolo.yaml group/world readable: %o", got)
	}
	if got := mustPerm(t, path); got != 0o600 {
		t.Errorf("mode = %o, want 600", got)
	}
}

func TestWriteCreatesPrivateFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "piccolo.yaml")
	m, err := Parse(strings.NewReader(validYAML))
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Write(path); err != nil {
		t.Fatal(err)
	}
	if got := mustPerm(t, path); got != 0o600 {
		t.Errorf("freshly written piccolo.yaml mode = %o, want 600", got)
	}
}

func mustPerm(t *testing.T, path string) os.FileMode {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}

// Tightening must never hand back access the owner removed: a read-only
// piccolo.yaml is a supported way to stop Piccolo editing it, and "make it
// private" that also adds owner-write would quietly undo that.
func TestEnsurePrivateNeverGrantsAccess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "piccolo.yaml")
	if err := os.WriteFile(path, []byte(validYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o444); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsurePrivate(path); err != nil {
		t.Fatal(err)
	}
	got := mustPerm(t, path)
	if got&0o077 != 0 {
		t.Errorf("group/other bits survived: %o", got)
	}
	if got&0o200 != 0 {
		t.Errorf("tightening granted owner write to a read-only file: %o", got)
	}
}
