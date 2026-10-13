package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkspaceMigrateLegacy(t *testing.T) {
	dir := t.TempDir()
	writeTestManifest(t, dir, "docker.io/sipeed/picoclaw:v0.3.1")
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"version":3}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "agents", "main"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "agents", "main", "AGENT.md"), []byte("---\nname: main\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{".security.yml", "launcher-auth.db"} {
		if err := os.WriteFile(filepath.Join(dir, rel), []byte("test"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(dir, "integrations", "gws"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "integrations", "gws", "backend.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	oldDir := dirFlag
	dirFlag = dir
	defer func() { dirFlag = oldDir }()
	var out bytes.Buffer
	cmd := testCommand(&out)
	if err := runWorkspaceMigrate(cmd, workspaceFlags{yes: true}); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{
		filepath.Join(dir, "picoclaw", "config.json"),
		filepath.Join(dir, "picoclaw", "agents", "main", "AGENT.md"),
		filepath.Join(dir, "picoclaw", "logs"),
		filepath.Join(dir, "picoclaw", ".security.yml"),
		filepath.Join(dir, "picoclaw", "launcher-auth.db"),
		filepath.Join(dir, "integrations", "gws", "backend.json"),
		filepath.Join(dir, "runtime", "devbox"),
		filepath.Join(dir, "runtime", "nix"),
		filepath.Join(dir, "runtime", "media"),
		filepath.Join(dir, "piccolo-migration-summary.md"),
	} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("missing %s: %v", p, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "config.json")); !os.IsNotExist(err) {
		t.Fatalf("legacy config still present err=%v", err)
	}
	if !strings.Contains(out.String(), "moved config.json -> picoclaw/config.json") {
		t.Fatalf("output = %s", out.String())
	}
}

func TestWorkspaceMigrateConflictNeedsConfirmation(t *testing.T) {
	dir := t.TempDir()
	writeTestManifest(t, dir, "docker.io/sipeed/picoclaw:v0.3.1")
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"legacy":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "agents"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "picoclaw"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "picoclaw", "config.json"), []byte(`{"new":true}`), 0o644); err != nil {
		t.Fatal(err)
	}

	oldDir := dirFlag
	dirFlag = dir
	defer func() { dirFlag = oldDir }()
	var out bytes.Buffer
	cmd := testCommand(&out)
	if err := runWorkspaceMigrate(cmd, workspaceFlags{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "would overwrite") {
		t.Fatalf("output = %s", out.String())
	}
	data, _ := os.ReadFile(filepath.Join(dir, "picoclaw", "config.json"))
	if string(data) != `{"new":true}` {
		t.Fatalf("structured file overwritten: %s", data)
	}
}

func TestWorkspaceMigrateIdempotent(t *testing.T) {
	dir := t.TempDir()
	writeTestManifest(t, dir, "docker.io/sipeed/picoclaw:v0.3.1")
	if err := os.MkdirAll(filepath.Join(dir, "picoclaw"), 0o755); err != nil {
		t.Fatal(err)
	}
	oldDir := dirFlag
	dirFlag = dir
	defer func() { dirFlag = oldDir }()
	var out bytes.Buffer
	if err := runWorkspaceMigrate(testCommand(&out), workspaceFlags{yes: true}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "already uses") {
		t.Fatalf("output = %s", out.String())
	}
}
