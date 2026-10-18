package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kimus/piccolo/internal/configdoc"
	"github.com/kimus/piccolo/internal/manifest"
	"github.com/kimus/piccolo/internal/setup"
	"github.com/kimus/piccolo/internal/workspace"
)

// removeFixture writes a workspace with one installed integration whose MCP server is
// registered and whose toggle is on. No downloads, no builds: every case here runs
// through --no-rebuild, which is the half of the transaction that touches the user's
// files.
func removeFixture(t *testing.T, extraServers bool) (string, *manifest.Manifest) {
	t.Helper()
	dir := t.TempDir()
	layout := workspace.NewLayout(dir)
	if err := layout.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	m := &manifest.Manifest{Version: 1}
	m.Container.Image = "piccolo-runtime:aaaabbbbcccc"
	m.Container.Name = "piccolo-gateway"
	m.Container.Network = "bridge"
	m.Runtime.Integrations = []manifest.Integration{{
		Package:      "github:acme/gws",
		Name:         "gws",
		StatePath:    filepath.Join("integrations", "gws"),
		MCPServerKey: "piccolo_gws",
		SetupPorts:   []string{"127.0.0.1:18080:18080"},
	}}
	if err := m.Write(layout.Manifest()); err != nil {
		t.Fatal(err)
	}
	servers := map[string]any{"piccolo_gws": map[string]any{"command": "piccolo-gws", "args": []string{"mcp"}}}
	if extraServers {
		servers["other_server"] = map[string]any{"command": "other"}
	}
	cfg := map[string]any{
		"version": 3,
		"tools":   map[string]any{"mcp": map[string]any{"enabled": true, "servers": servers}},
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	// Private from the start: a fixture must not depend on the mode rule to be valid.
	if err := os.WriteFile(layout.Config(), append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "integrations", "gws"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "integrations", "gws", "credentials.enc"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir, m
}

func runRemove(t *testing.T, dir, name string, in string, args ...string) (string, error) {
	t.Helper()
	oldDir := dirFlag
	dirFlag = dir
	defer func() { dirFlag = oldDir }()

	cmd := newIntegrationRemoveCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetIn(strings.NewReader(in))
	cmd.SetContext(context.Background())
	cmd.SetArgs(append([]string{name}, args...))
	err := cmd.Execute()
	return buf.String(), err
}

func readConfig(t *testing.T, dir string) *configdoc.Doc {
	t.Helper()
	doc, err := configdoc.Load(workspace.NewLayout(dir).Config())
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

func mcpEnabled(t *testing.T, dir string) *bool { return setup.GetTools(readConfig(t, dir)).MCP }

// The headline case for this command: uninstalling must remove the registered server
// entry, not just the manifest record. An entry with no integration behind it is not
// inert — with tools.mcp.enabled on the gateway tries to start it and a missing command
// fails every chat turn.
func TestRemoveUnregistersLastServerAndTurnsToggleOff(t *testing.T) {
	dir, _ := removeFixture(t, false)
	out, err := runRemove(t, dir, "gws", "", "--yes", "--no-rebuild")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if got := setup.MCPServerKeys(readConfig(t, dir)); len(got) != 0 {
		t.Errorf("server entry survived removal: %v", got)
	}
	enabled := mcpEnabled(t, dir)
	if enabled == nil || *enabled {
		t.Errorf("tools.mcp.enabled left on with no servers remaining: %v", enabled)
	}
	m, err := manifest.Load(workspace.NewLayout(dir).Manifest())
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Runtime.Integrations) != 0 {
		t.Errorf("manifest still records the integration: %+v", m.Runtime.Integrations)
	}
	if !strings.Contains(out, "--no-rebuild") && !strings.Contains(out, "still contains") {
		t.Errorf("offline removal must name the image that still holds the binary:\n%s", out)
	}
}

// With a second server registered, the toggle is a promise about servers that still
// exist. Removal must not silently re-promise or silently revoke it.
func TestRemoveKeepsToggleWhenServersRemain(t *testing.T) {
	dir, _ := removeFixture(t, true)
	out, err := runRemove(t, dir, "gws", "", "--yes", "--no-rebuild")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	keys := setup.MCPServerKeys(readConfig(t, dir))
	if len(keys) != 1 || keys[0] != "other_server" {
		t.Errorf("remaining servers = %v, want [other_server]", keys)
	}
	enabled := mcpEnabled(t, dir)
	if enabled == nil || !*enabled {
		t.Errorf("toggle was changed although a registered server remains: %v", enabled)
	}
	if !strings.Contains(out, "left as-is") {
		t.Errorf("must state what it did with the toggle:\n%s", out)
	}
}

// Declining must be the end of it: no file edited, no state deleted.
func TestRemoveCancelledEditsNothing(t *testing.T) {
	dir, _ := removeFixture(t, false)
	before := func(p string) string { b, _ := os.ReadFile(p); return string(b) }
	l := workspace.NewLayout(dir)
	cfgBefore, manBefore := before(l.Config()), before(l.Manifest())
	out, err := runRemove(t, dir, "gws", "n\n", "--no-rebuild")
	if err == nil || !strings.Contains(out, "cancelled") {
		t.Fatalf("expected a refusal, got err=%v out=%s", err, out)
	}
	if before(l.Config()) != cfgBefore {
		t.Error("declined removal still edited config.json")
	}
	if before(l.Manifest()) != manBefore {
		t.Error("declined removal still edited piccolo.yaml")
	}
	if _, err := os.Stat(filepath.Join(dir, "integrations", "gws", "credentials.enc")); err != nil {
		t.Error("declined removal deleted state")
	}
}

// Credentials outlive an uninstall by default: re-adding the integration should not
// require re-authentication.
func TestRemovePreservesStateByDefaultAndPurgesOnRequest(t *testing.T) {
	dir, _ := removeFixture(t, false)
	if _, err := runRemove(t, dir, "gws", "", "--yes", "--no-rebuild"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "integrations", "gws", "credentials.enc")); err != nil {
		t.Errorf("default removal deleted stored credentials: %v", err)
	}

	dir2, _ := removeFixture(t, false)
	if _, err := runRemove(t, dir2, "gws", "y\n", "--yes", "--purge", "--no-rebuild"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir2, "integrations", "gws")); !os.IsNotExist(err) {
		t.Errorf("--purge left state behind: %v", err)
	}
}

// A recorded StatePath is user-visible data, not a trusted path: an escape would delete
// outside the workspace.
func TestPurgeRefusesPathOutsideIntegrationsRoot(t *testing.T) {
	dir, _ := removeFixture(t, false)
	if err := os.MkdirAll(filepath.Join(dir, "outside"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Rewrite the record with an escaping StatePath.
	m, err := manifest.Load(workspace.NewLayout(dir).Manifest())
	if err != nil {
		t.Fatal(err)
	}
	m.Runtime.Integrations[0].StatePath = filepath.Join("..", "..", "outside")
	if err := m.Write(workspace.NewLayout(dir).Manifest()); err != nil {
		t.Fatal(err)
	}
	if _, err := runRemove(t, dir, "gws", "", "--yes", "--purge", "--no-rebuild"); err == nil {
		t.Fatal("an escaping StatePath was accepted")
	}
	if _, err := os.Stat(filepath.Join(dir, "outside")); err != nil {
		t.Error("refusal must not delete anything")
	}
}

// --keep-server-entry is an explicit half-removal; it must be loud, because the result
// is a gateway that fails every chat turn.
func TestKeepServerEntryIsWarnedAndDetectable(t *testing.T) {
	dir, _ := removeFixture(t, false)
	out, err := runRemove(t, dir, "gws", "", "--yes", "--no-rebuild", "--keep-server-entry")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, "[WARN]") || !strings.Contains(out, "EVERY chat turn") {
		t.Errorf("half-removal was not stated loudly:\n%s", out)
	}
	if got := setup.MCPServerKeys(readConfig(t, dir)); len(got) != 1 {
		t.Errorf("entry was removed despite --keep-server-entry: %v", got)
	}
}

func TestRemoveUnknownIntegrationNamesIt(t *testing.T) {
	dir, _ := removeFixture(t, false)
	if _, err := runRemove(t, dir, "nope", "", "--yes", "--no-rebuild"); err == nil ||
		!strings.Contains(err.Error(), "not installed") {
		t.Fatalf("want a clear 'not installed' error, got %v", err)
	}
}
