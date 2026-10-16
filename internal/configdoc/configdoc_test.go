package configdoc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The backup is where a sealed credential used to stay readable: 'piccolo secrets
// encrypt' moves a plaintext API key out of config.json and into the overlay, then
// writes the copy it just removed to config.json.bak AT THE MODE OF THE FILE IT CAME
// FROM — 0644. The operation whose purpose was removing the plaintext was the source
// of the leak, and nothing anywhere reported it.
func TestWriteBackupIsPrivateWhateverTheSourceMode(t *testing.T) {
	for _, src := range []os.FileMode{0o644, 0o666, 0o600} {
		dir := t.TempDir()
		path := filepath.Join(dir, "config.json")
		if err := os.WriteFile(path, []byte(`{"api_keys":["sk-PLAIN"]}`), src); err != nil {
			t.Fatal(err)
		}
		d, err := Parse([]byte(`{"api_keys":["enc://x"]}`))
		if err != nil {
			t.Fatal(err)
		}
		if err := Write(path, d); err != nil {
			t.Fatalf("source %o: %v", src, err)
		}
		if got := permOf(t, path+".bak"); got&0o077 != 0 {
			t.Errorf("source %o: config.json.bak is %o and holds the plaintext the sealing run removed", src, got)
		}
		// The plaintext really is in there, which is why the mode matters.
		if b, err := os.ReadFile(path + ".bak"); err != nil || !contains(b, "sk-PLAIN") {
			t.Errorf("backup content unexpected: %q err=%v", b, err)
		}
	}
}

// A 0644 config.json that gets rewritten must not stay 0644 — the mode has to be
// asserted, not requested, or the rule only ever applies to brand-new workspaces.
func TestWriteTightensTarget(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"a":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	d, err := Parse([]byte(`{"a":2}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := Write(path, d); err != nil {
		t.Fatal(err)
	}
	if got := permOf(t, path); got != 0o600 {
		t.Errorf("target mode = %o, want 600", got)
	}
}

// Renaming over a file borrows the temp file's mode. Without preserving the original
// bits, writing a config.json the owner made read-only would hand the write bit back.
func TestWriteDoesNotGrantWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"a":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o444); err != nil {
		t.Fatal(err)
	}
	d, err := Parse([]byte(`{"a":2}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := Write(path, d); err != nil {
		t.Fatal(err)
	}
	if got := permOf(t, path); got&0o200 != 0 {
		t.Errorf("write granted owner-write back to a read-only file: %o", got)
	}
	if got := permOf(t, path); got&0o077 != 0 {
		t.Errorf("group/other bits survived: %o", got)
	}
}

// A credential-bearing temp file that outlives a failure must not be world-readable
// either, and a first write creates no backup at all.
func TestWriteFreshFilePrivateNoBackup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	d := NewDoc()
	d.Set("version", 3)
	if err := Write(path, d); err != nil {
		t.Fatal(err)
	}
	if got := permOf(t, path); got != 0o600 {
		t.Errorf("fresh file mode = %o", got)
	}
	if _, err := os.Stat(path + ".bak"); !os.IsNotExist(err) {
		t.Errorf("a first write created a backup: %v", err)
	}
}

func permOf(t *testing.T, path string) os.FileMode {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}

func contains(b []byte, s string) bool { return strings.Contains(string(b), s) }
