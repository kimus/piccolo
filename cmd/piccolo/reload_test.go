package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadPidFile(t *testing.T) {
	dir := t.TempDir()

	// Missing file → actionable "run serve" error.
	_, err := readPidFile(filepath.Join(dir, pidFileRel))
	if err == nil || !strings.Contains(err.Error(), "piccolo serve") {
		t.Fatalf("missing file: %v", err)
	}

	// Valid token.
	path := filepath.Join(dir, pidFileRel)
	os.WriteFile(path, []byte(`{"pid":7,"token":"a3f1","version":"v0.3.1","port":18790,"host":"0.0.0.0"}`), 0o600)
	pf, err := readPidFile(path)
	if err != nil {
		t.Fatalf("valid: %v", err)
	}
	if pf.Token != "a3f1" || pf.Port != 18790 {
		t.Fatalf("parsed = %+v", pf)
	}

	// Garbage → restart hint.
	os.WriteFile(path, []byte("not json"), 0o600)
	if _, err := readPidFile(path); err == nil || !strings.Contains(err.Error(), "piccolo restart") {
		t.Fatalf("garbage: %v", err)
	}

	// Empty token → restart hint.
	os.WriteFile(path, []byte(`{"pid":7,"token":"  "}`), 0o600)
	if _, err := readPidFile(path); err == nil || !strings.Contains(err.Error(), "piccolo restart") {
		t.Fatalf("empty token: %v", err)
	}
}
