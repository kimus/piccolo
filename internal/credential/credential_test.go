package credential

import (
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func tempKey(t *testing.T) (dir, path string) {
	t.Helper()
	dir = t.TempDir()
	path, err := Generate(dir)
	if err != nil {
		t.Fatal(err)
	}
	return dir, path
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	_, key := tempKey(t)
	for _, plain := range []string{"sk-abc123", "", "token with spaces", "ünïcødé ✓", strings.Repeat("x", 4096)} {
		enc, err := Encrypt("pw", key, plain)
		if err != nil {
			t.Fatalf("encrypt %q: %v", plain, err)
		}
		if !IsEncrypted(enc) {
			t.Errorf("value %q is not prefixed with the scheme", enc)
		}
		if strings.Contains(enc, plain) && plain != "" {
			t.Errorf("ciphertext leaks the plaintext for %q", plain)
		}
		got, err := Decrypt("pw", key, enc)
		if err != nil {
			t.Fatalf("decrypt %q: %v", plain, err)
		}
		if got != plain {
			t.Errorf("round trip = %q, want %q", got, plain)
		}
	}
}

// Encrypting the same secret twice must not produce the same string, or a config
// diff would reveal equality and a re-encryption would look like a no-op.
func TestEncryptIsNonDeterministic(t *testing.T) {
	_, key := tempKey(t)
	a, err := Encrypt("pw", key, "sk-same")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := Encrypt("pw", key, "sk-same")
	if a == b {
		t.Error("two encryptions of the same value produced identical ciphertext")
	}
	// Both must still open.
	for _, s := range []string{a, b} {
		if got, err := Decrypt("pw", key, s); err != nil || got != "sk-same" {
			t.Errorf("decrypt = %q, %v", got, err)
		}
	}
}

// Task 1.5: every failure mode is distinguishable and, above all, never returns a
// wrong plaintext with a nil error. A silent wrong answer is the failure that
// would ship a workspace whose gateway boots and then authenticates badly.
func TestFailureModes(t *testing.T) {
	_, key := tempKey(t)
	good, err := Encrypt("right-pass", key, "sk-value")
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name   string
		pass   string
		keyArg string
		value  string
		want   error
	}{
		{"empty passphrase", "", key, good, ErrPassphraseRequired},
		{"empty key path", "right-pass", "", good, ErrKeyRequired},
		{"missing key file", "right-pass", filepath.Join(t.TempDir(), "nope.key"), good, ErrKeyRequired},
		{"wrong passphrase", "wrong-pass", key, good, ErrDecryptionFailed},
		{"not base64", "right-pass", key, Scheme + "!!!not base64!!!", nil},
		{"payload too short", "right-pass", key, Scheme + base64.StdEncoding.EncodeToString([]byte("tiny")), nil},
	}
	for _, tc := range cases {
		got, err := Decrypt(tc.pass, tc.keyArg, tc.value)
		if err == nil {
			t.Errorf("%s: decrypted to %q with no error", tc.name, got)
			continue
		}
		if tc.want != nil && !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", tc.name, err, tc.want)
		}
		if got != "" {
			t.Errorf("%s: returned %q alongside an error", tc.name, got)
		}
	}

	// Encrypt guards the same two inputs, so a caller cannot write a credential
	// that is guaranteed to fail at boot.
	if _, err := Encrypt("", key, "sk-x"); !errors.Is(err, ErrPassphraseRequired) {
		t.Errorf("Encrypt with empty passphrase: %v", err)
	}
	if _, err := Encrypt("pw", "", "sk-x"); !errors.Is(err, ErrKeyRequired) {
		t.Errorf("Encrypt with no key: %v", err)
	}
}

func TestIsEncrypted(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  bool
	}{
		{"enc://AAAA", true},
		{"  enc://AAAA  ", true},
		{"sk-plain", false},
		{"file://keys/openai.key", false},
		{"", false},
		{"enc:/missing-slash", false},
	} {
		if got := IsEncrypted(tc.value); got != tc.want {
			t.Errorf("IsEncrypted(%q) = %v, want %v", tc.value, got, tc.want)
		}
	}
}

// A decrypt that ignores the scheme prefix must still work, because the gateway
// strips it itself and a Piccolo-side check should not disagree with it.
func TestDecryptAcceptsBarePayload(t *testing.T) {
	_, key := tempKey(t)
	enc, err := Encrypt("pw", key, "sk-value")
	if err != nil {
		t.Fatal(err)
	}
	bare := strings.TrimPrefix(enc, Scheme)
	if got, err := Decrypt("pw", key, bare); err != nil || got != "sk-value" {
		t.Errorf("bare payload = %q, %v", got, err)
	}
}

func TestGenerate(t *testing.T) {
	dir, key := tempKey(t)

	info, err := os.Stat(key)
	if err != nil {
		t.Fatal(err)
	}
	// The private key must not be world- or group-readable. Permission drift here
	// is the whole point of encrypting, so it is asserted rather than trusted.
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("private key mode = %o, want 600", perm)
	}
	if perm := mustStat(t, key+".pub").Mode().Perm(); perm != 0o644 {
		t.Errorf("public key mode = %o, want 644", perm)
	}
	if perm := mustStat(t, dir).Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("key directory mode = %o, want no group/other bits", perm)
	}

	// It must be a real OpenSSH private key, matching upstream's keygen output:
	// anything else is a different byte string, and the bytes are the key.
	head, _ := os.ReadFile(key)
	if !strings.Contains(string(head), "-----BEGIN OPENSSH PRIVATE KEY-----") {
		t.Errorf("private key is not an OpenSSH PEM:\n%s", head)
	}
}

// Re-running generation must not silently rotate the key: every credential
// encrypted with the old bytes would become unreadable.
func TestGenerateRefusesToOverwrite(t *testing.T) {
	dir, first := tempKey(t)
	secret, err := Encrypt("pw", first, "sk-precious")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := Generate(dir); err == nil {
		t.Fatal("Generate overwrote an existing key without being asked")
	} else if !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("unexpected error: %v", err)
	}

	// The decisive half: the existing key still works after the refusal.
	if got, err := Decrypt("pw", first, secret); err != nil || got != "sk-precious" {
		t.Errorf("credentials broke after a refused rotation: %q, %v", got, err)
	}
}

// A MkdirAll on an existing directory keeps its old mode, so a keys dir created
// by something else at 0755 must still be tightened on the next generate.
func TestGenerateTightensExistingDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "keys")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Generate(dir); err != nil {
		t.Fatal(err)
	}
	if perm := mustStat(t, dir).Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("existing keys dir kept mode %o", perm)
	}
}

func mustStat(t *testing.T, path string) os.FileInfo {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info
}
