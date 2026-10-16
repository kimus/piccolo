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
