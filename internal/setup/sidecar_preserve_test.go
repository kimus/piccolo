package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kimus/piccolo/internal/credential"
)

// The state the user found: config.json encrypted, overlay still plaintext, and the
// overlay is what the gateway loads. Healing must seal the overlay while leaving
// every node Piccolo does not understand exactly as it was.
func TestEncryptSidecarHealsAndPreserves(t *testing.T) {
	dir := t.TempDir()
	key, err := mkKeyDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	sc := filepath.Join(dir, ".security.yml")
	body := `# a hand-written note that must survive
model_list:
  gpt-4o:0:
    api_keys:
      - sk-PLAINTEXT-WINS
  other:0:
    api_keys: []
channel_list:
  discord:
    settings:
      token: dt-PLAIN
      proxy: http://keep-me
  irc:
    settings:
      nickserv_password: pn-PLAIN
username: plain-not-a-credential
skills:
  registries: {}
`
	if err := os.WriteFile(sc, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	res, err := EncryptSidecar(sc, key, "PASS", LiveOverlay{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Changed() {
		t.Fatalf("nothing healed: %+v", res)
	}
	after, _ := os.ReadFile(sc)
	s := string(after)
	t.Logf("encrypted: %v", res.Encrypted)
	for _, must := range []string{
		"# a hand-written note that must survive",
		"http://keep-me",
		"username: plain-not-a-credential",
		"api_keys: []",
		"registries: {}",
	} {
		if !strings.Contains(s, must) {
			t.Errorf("lost %q after the heal:\n%s", must, s)
		}
	}
	for _, gone := range []string{"sk-PLAINTEXT-WINS", "dt-PLAIN", "pn-PLAIN"} {
		if strings.Contains(s, gone) {
			t.Errorf("plaintext %q still in the sidecar", gone)
		}
	}
	if n := strings.Count(s, "enc://"); n != 3 {
		t.Errorf("expected 3 sealed values, got %d:\n%s", n, s)
	}
	// Idempotent, and a second run must not rewrite.
	res2, err := EncryptSidecar(sc, key, "PASS", LiveOverlay{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res2.Changed() {
		t.Error("second pass re-encrypted ciphertext")
	}
	if a, _ := os.ReadFile(sc); string(a) != s {
		t.Error("second pass rewrote the file")
	}
	t.Logf("RESULT:\n%s", s)
}

func mkKeyDir(dir string) (string, error) {
	return credential.Generate(filepath.Join(dir, "keys"))
}
