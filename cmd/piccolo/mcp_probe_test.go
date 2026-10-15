package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kimus/piccolo/internal/configdoc"
	"github.com/kimus/piccolo/internal/setup"
)

func TestClassifyMCPPrelude(t *testing.T) {
	good := `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2024-11-05","capabilities":{"tools":{}},"serverInfo":{"name":"piccolo-gws","version":"0.3.4"}}}`
	detail, err := classifyMCPPrelude(good)
	if err != nil {
		t.Fatalf("a JSON-RPC result must pass: %v", err)
	}
	if detail != "piccolo-gws 0.3.4" {
		t.Fatalf("detail = %q, want the answering server", detail)
	}

	// The observed failure: an integration whose stdio server writes LSP-style
	// framing. The verdict must name the framing, because that is what the user can
	// act on upstream.
	framed := "Content-Length: 148\r\n\r\n"
	_, err = classifyMCPPrelude(framed)
	if err == nil || !strings.Contains(err.Error(), "Content-Length") || !strings.Contains(err.Error(), "newline-delimited") {
		t.Fatalf("framed reply must be refused with an actionable reason, got %v", err)
	}

	if _, err = classifyMCPPrelude("piccolo-gws: fatal: no such subcommand"); err == nil || !strings.Contains(err.Error(), "not JSON") {
		t.Fatalf("non-JSON first line must fail, got %v", err)
	}
	if _, err = classifyMCPPrelude(`{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"no initialize support"}}`); err == nil || !strings.Contains(err.Error(), "refused initialization") {
		t.Fatalf("a JSON-RPC error reply must fail, got %v", err)
	}
	// A bare JSON line that is not a response (e.g. a log object) must not pass.
	if _, err = classifyMCPPrelude(`{"level":"info","msg":"server started"}`); err == nil {
		t.Fatal("a reply without a result must not count as a handshake")
	}
}

func TestQuoteForLogStripsControlCharacters(t *testing.T) {
	got := quoteForLog("x\x1b[2J\x07" + strings.Repeat("y", 300))
	if strings.ContainsAny(got, "\x1b\x07") {
		t.Fatalf("control characters reached user-facing output: %q", got)
	}
	if len(got) > 200 {
		t.Fatalf("log quote unbounded: %d bytes", len(got))
	}
}

// stubHandshake is the daemon-free probe: the gate decision is what is under test,
// not Docker.
type stubHandshake struct {
	detail string
	err    error
	argv   []string
	key    string
	calls  int
}

func (s *stubHandshake) Probe(_ context.Context, serverKey string, argv []string) (string, error) {
	s.calls++
	s.key = serverKey
	s.argv = argv
	return s.detail, s.err
}

const gwsPatch = `{"tools":{"mcp":{"servers":{"google_workspace":{"type":"stdio","enabled":true,"command":"piccolo-gws","args":["mcp","--read-only"]}}}}}`

func writeConfig(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte("{\n  \"version\": 3\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// A verified handshake is the only path that turns MCP on.
func TestApplyIntegrationPatchEnablesMCPAfterVerifiedHandshake(t *testing.T) {
	path := writeConfig(t, t.TempDir())
	out := &bytes.Buffer{}
	probe := &stubHandshake{detail: "piccolo-gws 0.3.4"}
	yes := func() (bool, error) { return true, nil }

	if err := applyIntegrationPatch(context.Background(), out, path, json.RawMessage(gwsPatch), "google_workspace", probe, yes); err != nil {
		t.Fatal(err)
	}
	if probe.calls != 1 {
		t.Fatalf("probe calls = %d, want 1", probe.calls)
	}
	if probe.key != "google_workspace" {
		t.Fatalf("probe keyed %q", probe.key)
	}
	want := []string{"piccolo-gws", "mcp", "--read-only"}
	if strings.Join(probe.argv, " ") != strings.Join(want, " ") {
		t.Fatalf("probe argv = %v, want %v", probe.argv, want)
	}
	doc, err := configdoc.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := setup.MCPToolEnabled(doc); got == nil || !*got {
		t.Fatalf("verified server must leave tools.mcp.enabled true (got %v)", got)
	}
	if !strings.Contains(out.String(), "answered initialize") {
		t.Fatalf("output should report the verified handshake:\n%s", out.String())
	}
}

// A failed handshake still registers the server, but must NOT enable the tool: with
// the toggle on and the server unreachable, PicoClaw fails every chat message.
func TestApplyIntegrationPatchWithholdsEnableOnFailedHandshake(t *testing.T) {
	path := writeConfig(t, t.TempDir())
	out := &bytes.Buffer{}
	probe := &stubHandshake{err: errors.New(`the server answered with LSP-style "Content-Length:" framing`)}
	yes := func() (bool, error) { return true, nil }

	if err := applyIntegrationPatch(context.Background(), out, path, json.RawMessage(gwsPatch), "google_workspace", probe, yes); err != nil {
		t.Fatal(err)
	}
	doc, err := configdoc.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	// Registered…
	if got := setup.MCPServerArgv(doc, "google_workspace"); len(got) == 0 {
		t.Fatal("the server entry must still be written")
	}
	// …not enabled.
	if got := setup.MCPToolEnabled(doc); got != nil && *got {
		t.Fatal("a server that failed the handshake must not flip tools.mcp.enabled")
	}
	text := out.String()
	for _, want := range []string{"cannot use it yet", "every chat message"} {
		if !strings.Contains(text, want) {
			t.Fatalf("output must explain the withheld enable, missing %q:\n%s", want, text)
		}
	}
}

// Without a prober there is no verification, and Piccolo must say so instead of
// assuming the integration works.
func TestApplyIntegrationPatchWithoutProberStaysUnverified(t *testing.T) {
	path := writeConfig(t, t.TempDir())
	out := &bytes.Buffer{}
	yes := func() (bool, error) { return true, nil }

	if err := applyIntegrationPatch(context.Background(), out, path, json.RawMessage(gwsPatch), "google_workspace", nil, yes); err != nil {
		t.Fatal(err)
	}
	doc, err := configdoc.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := setup.MCPToolEnabled(doc); got != nil && *got {
		t.Fatal("an unverified registration must not enable MCP")
	}
	if !strings.Contains(out.String(), "could not run a handshake") {
		t.Fatalf("output should state it could not verify:\n%s", out.String())
	}
}
