package manifest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseAcceptsSecretsMetadata(t *testing.T) {
	m, err := Parse(strings.NewReader(validYAML + `secrets:
  passphrase_env: PICCOLO_PASSPHRASE
`))
	if err != nil {
		t.Fatalf("Parse secrets metadata: %v", err)
	}
	if m.PassphraseVar() != "PICCOLO_PASSPHRASE" {
		t.Fatalf("passphrase var = %q", m.Secrets.PassphraseEnv)
	}
	// An absent block means an unencrypted workspace, not an error.
	plain, err := Parse(strings.NewReader(validYAML))
	if err != nil || plain.PassphraseVar() != "" {
		t.Fatalf("no secrets block: %q %v", plain.PassphraseVar(), err)
	}
}

// The block holds a variable NAME. A value pasted here would put the passphrase in
// plaintext in the very file the feature exists to protect, and encryption would
// look configured while doing nothing — so the shape is rejected at parse time
// rather than discovered at boot.
func TestParseRejectsPassphraseValueNotName(t *testing.T) {
	for _, bad := range []string{
		"hunter2",                // lowercase, looks like a secret
		"correct horse battery",  // spaces cannot be an env name
		"piccolo=secret",         // assignment shape
		"env:PICCOLO_PASSPHRASE", // copied container.env style; the field takes a bare name
		"9LIVES",                 // cannot start with a digit
		"PICCOLO-PASSPHRASE",     // hyphen is not valid in an exported name
		"$(cat /tmp/pass)",       // shell interpolation is never a name
	} {
		_, err := Parse(strings.NewReader(validYAML + "secrets:\n  passphrase_env: " + quoteIf(bad) + "\n"))
		if err == nil {
			t.Errorf("passphrase_env %q was accepted; it is not an env var name", bad)
		} else if !strings.Contains(err.Error(), "passphrase_env") || !strings.Contains(err.Error(), "uppercase") {
			t.Errorf("error for %q does not name the field: %v", bad, err)
		}
	}

	for _, good := range []string{"PICCOLO_PASSPHRASE", "P", "_HIDDEN", "A1", "A_B_C"} {
		if !IsEnvVarName(good) {
			t.Errorf("IsEnvVarName(%q) = false, want true", good)
		}
	}
	// Lowercase is legal in POSIX but rejected here on purpose: "hunter2" is the
	// single most likely pasted passphrase, and it matches the permissive rule.
	for _, lower := range []string{"hunter2", "my_passphrase", "piccolo_passphrase"} {
		if IsEnvVarName(lower) {
			t.Errorf("IsEnvVarName(%q) = true; a lowercase word is the shape of a secret, not of this field", lower)
		}
	}
}

func quoteIf(s string) string {
	// YAML needs quoting for values with spaces or leading special characters; the
	// point of the test is the parsed value, not the YAML escape rules.
	if strings.ContainsAny(s, " $(") || strings.Contains(s, "=") {
		return `"` + s + `"`
	}
	return s
}

// An unknown sibling under secrets: must still be refused. Strict decoding is what
// makes a typo in this field loud instead of silently disabling encryption.
func TestParseRejectsUnknownSecretsKey(t *testing.T) {
	_, err := Parse(strings.NewReader(validYAML + "secrets:\n  passphrase_env: PICCOLO_PASSPHRASE\n  passphrase: hunter2\n"))
	if err == nil {
		t.Fatal("a `passphrase:` key (a value, not a name) must be rejected, not ignored")
	}
}

// Round-tripping must not invent an empty secrets block in every workspace's
// piccolo.yaml.
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
	// And the encrypted form survives a write/load cycle unchanged.
	m.Secrets.PassphraseEnv = "PICCOLO_PASSPHRASE"
	if err := m.Write(path); err != nil {
		t.Fatal(err)
	}
	back, err := Load(path)
	if err != nil || back.PassphraseVar() != "PICCOLO_PASSPHRASE" {
		t.Fatalf("round trip = %q, %v", back.PassphraseVar(), err)
	}
}
