package fileperm

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTightenOnlyRemovesBits(t *testing.T) {
	for _, tc := range []struct{ in, want os.FileMode }{
		{0o644, 0o600}, // the common exposure
		{0o640, 0o600},
		{0o444, 0o400}, // read-only stays read-only: no write bit handed back
		{0o600, 0o600},
		{0o755, 0o700}, // execute preserved, group/other dropped
		{0o666, 0o600},
	} {
		if got := Tighten(tc.in); got != tc.want {
			t.Errorf("Tighten(%o) = %o, want %o", tc.in, got, tc.want)
		}
	}
}

// os.WriteFile's perm applies only on CREATE, so a mode that is not asserted after
// the fact does not exist for any file that was already on disk.
func TestWritePrivateTightensAPreExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WritePrivate(path, []byte("new")); err != nil {
		t.Fatal(err)
	}
	if got := permOf(t, path); got != Private {
		t.Errorf("mode = %o, want %o", got, Private)
	}
}

func TestEnsurePrivateReportsChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if changed, err := EnsurePrivate(path); err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if changed, err := EnsurePrivate(path); err != nil || changed {
		t.Fatalf("idempotency: changed=%v err=%v", changed, err)
	}
	if _, err := EnsurePrivate(filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Error("missing file reported no error")
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
