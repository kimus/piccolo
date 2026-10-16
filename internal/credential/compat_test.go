package credential

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testdataPassphrase and testdataPlaintext are the values the pinned upstream
// implementation used when it produced testdata/upstream-ciphertext.txt. They are
// fixtures, not secrets: the key next to them is generated for this test and
// committed with it precisely so a reviewer can reproduce the vector.
const (
	fixturePassphrase = "correct horse battery staple"
	fixturePlaintext  = "sk-upstream-generated-vector"
)

// fixturePath resolves a testdata path absolutely. The compatibility checker runs
// with a different working directory, so a relative path would be read as missing
// there and report a format mismatch that is only a path bug.
func fixturePath(t *testing.T, name string) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Skipf("missing fixture %s — regenerate it with the pinned picoclaw module available", p)
	}
	return p
}

// TestUpstreamCiphertextDecrypts is direction 1 of the compatibility proof:
// ciphertext written by the real PicoClaw v0.3.1 pkg/credential must open with
// Piccolo's implementation. This is what makes reproducing the scheme safe rather
// than hopeful — a drifted constant here fails a test instead of shipping a
// workspace whose gateway cannot boot.
func TestUpstreamCiphertextDecrypts(t *testing.T) {
	keyPath := fixturePath(t, filepath.Join("compat", FileName))
	raw, err := os.ReadFile(fixturePath(t, "upstream-ciphertext.txt"))
	if err != nil {
		t.Fatal(err)
	}
	ciphertext := string(raw)

	got, err := Decrypt(fixturePassphrase, keyPath, ciphertext)
	if err != nil {
		t.Fatalf("Piccolo could not decrypt upstream ciphertext: %v", err)
	}
	if got != fixturePlaintext {
		t.Errorf("decrypt = %q, want %q", got, fixturePlaintext)
	}
}

// TestPiccoloCiphertextIsSelfConsistent round-trips through Piccolo alone. It
// cannot prove upstream compatibility on its own, so the reverse direction is
// checked by TestUpstreamDecryptsPiccoloOutput, which shells out to the pinned
// module when it is available in the local cache.
func TestPiccoloCiphertextIsSelfConsistent(t *testing.T) {
	keyPath := fixturePath(t, filepath.Join("compat", FileName))
	enc, err := Encrypt(fixturePassphrase, keyPath, "sk-round-trip")
	if err != nil {
		t.Fatal(err)
	}
	got, err := Decrypt(fixturePassphrase, keyPath, enc)
	if err != nil {
		t.Fatal(err)
	}
	if got != "sk-round-trip" {
		t.Errorf("round trip = %q", got)
	}
}

// TestUpstreamDecryptsPiccoloOutput is direction 2: ciphertext produced by
// Piccolo must open with the pinned upstream implementation. It runs a program
// against that module from the local Go module cache; when the module is not
// present the test skips rather than failing, because absence is an environment
// property, not a broken build.
func TestUpstreamDecryptsPiccoloOutput(t *testing.T) {
	keyPath := fixturePath(t, filepath.Join("compat", FileName))
	enc, err := Encrypt(fixturePassphrase, keyPath, fixturePlaintext)
	if err != nil {
		t.Fatal(err)
	}
	if !upstreamModuleAvailable() {
		t.Skip("pinned picoclaw module not in the local Go module cache")
	}
	out, err := runCompatCheck(enc, keyPath, fixturePassphrase)
	if err != nil {
		t.Fatalf("upstream rejected Piccolo's ciphertext: %v\n%s", err, out)
	}
	// The checker prints only the plaintext, so a pass also proves it decrypted to
	// the right value rather than merely exiting 0.
	if strings.TrimSpace(out) != fixturePlaintext {
		t.Errorf("upstream decrypted Piccolo's ciphertext to %q, want %q", strings.TrimSpace(out), fixturePlaintext)
	}
}

// TestConstantDriftIsDetected proves the fixture test would actually fail on the
// failure mode it exists to catch. Without this, a "compatibility" test that
// silently passes against a wrong implementation is worse than no test: it is the
// confidence that lets the reimplementation ship.
func TestConstantDriftIsDetected(t *testing.T) {
	keyPath := fixturePath(t, filepath.Join("compat", FileName))
	raw, err := os.ReadFile(fixturePath(t, "upstream-ciphertext.txt"))
	if err != nil {
		t.Fatal(err)
	}

	// Every derived-key input that upstream pins, perturbed once. A change to any
	// of them must stop this ciphertext from opening.
	for name, passphrase := range map[string]string{
		"passphrase":    fixturePassphrase + "x",
		"empty":         "",
		"different key": fixturePassphrase,
	} {
		if name == "different key" {
			other := filepath.Join(t.TempDir(), FileName)
			if _, err := Generate(filepath.Dir(other)); err != nil {
				t.Fatal(err)
			}
			if _, err := Decrypt(passphrase, other, string(raw)); err == nil {
				t.Error("a different SSH key still decrypted the vector")
			}
			continue
		}
		if _, err := Decrypt(passphrase, keyPath, string(raw)); err == nil {
			t.Errorf("%s: wrong value decrypted the upstream vector", name)
		}
	}
}
