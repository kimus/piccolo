package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/kimus/piccolo/internal/configdoc"
	"github.com/kimus/piccolo/internal/docker"
	"github.com/kimus/piccolo/internal/manifest"
	"github.com/kimus/piccolo/internal/workspace"
)

// okReply is what a compliant (newline-delimited JSON) MCP server answers.
const okReply = `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2024-11-05","capabilities":{},"serverInfo":{"name":"piccolo-gws","version":"0.3.5"}}}` + "\n"

// framedReply is the 0.3.4 behaviour: LSP framing the MCP client cannot parse.
const framedReply = "Content-Length: 148\r\n\r\n{\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{}}\n"

type stubProbeProcess struct {
	stdout io.Reader
}

func (s *stubProbeProcess) Stdin() io.WriteCloser             { return nopWriteCloser{&bytes.Buffer{}} }
func (s *stubProbeProcess) Stdout() io.ReadCloser             { return io.NopCloser(s.stdout) }
func (s *stubProbeProcess) Stderr() io.ReadCloser             { return io.NopCloser(&bytes.Buffer{}) }
func (s *stubProbeProcess) Wait(context.Context) (int, error) { return 0, nil }
func (s *stubProbeProcess) Shutdown(time.Duration) error      { return nil }
func (s *stubProbeProcess) ExitCode() int                     { return 0 }

// nopWriteCloser is declared in integration_test.go; the stub reuses it.

// stubVerifyEngine answers per image, which is exactly the axis the check exists for:
// the same server key behaves differently in the new image than in the container that
// is still running.
type stubVerifyEngine struct {
	pingErr  error
	state    docker.State
	byImage  map[string]string
	probed   []string
	runError error
}

func (e *stubVerifyEngine) Ping(context.Context) error { return e.pingErr }

func (e *stubVerifyEngine) StateByName(context.Context, string) (docker.State, error) {
	return e.state, nil
}

func (e *stubVerifyEngine) RemoveByPrefix(context.Context, string) ([]string, error) {
	return nil, nil
}

func (e *stubVerifyEngine) Run(_ context.Context, spec docker.Spec) (docker.Process, error) {
	if e.runError != nil {
		return nil, e.runError
	}
	e.probed = append(e.probed, spec.Image)
	reply, ok := e.byImage[spec.Image]
	if !ok {
		reply = okReply
	}
	return &stubProbeProcess{stdout: strings.NewReader(reply)}, nil
}

func testManifest(newImage string) *manifest.Manifest {
	return &manifest.Manifest{Container: manifest.Container{
		Image: newImage, Name: "piccolo-gateway", Network: "bridge",
	}}
}

var testServers = []mcpServerEntry{{key: "google_workspace", argv: []string{"piccolo-gws", "mcp"}}}

func verifyArgs(dir string) (workspace.Layout, *manifest.Manifest) {
	return workspace.NewLayout(dir), testManifest("piccolo/runtime:NEW")
}

// The incident, reproduced as the case it must catch: the image piccolo.yaml names
// handshakes fine, while the container still serving traffic replies in old framing.
// Verifying only the manifest image would report success and enable the toggle, and
// then EVERY chat message fails — not just the Gmail ones.
func TestVerifyMCPEnableChangeCatchesStaleRunningContainer(t *testing.T) {
	dir := t.TempDir()
	layout, m := verifyArgs(dir)
	eng := &stubVerifyEngine{
		state:   docker.State{Found: true, Running: true, Image: "piccolo/runtime:OLD"},
		byImage: map[string]string{"piccolo/runtime:OLD": framedReply},
	}

	problems, verified, unverifiable := verifyMCPEnableChange(context.Background(), eng, m.Container.Name, layout, m, testServers, io.Discard)
	if unverifiable {
		t.Fatal("a reachable engine is not an unverifiable one")
	}
	if len(problems) != 1 {
		t.Fatalf("stale running image must be a problem, got %v", problems)
	}
	p := problems[0]
	for _, want := range []string{"google_workspace", "running gateway", "piccolo/runtime:OLD", "Content-Length"} {
		if !strings.Contains(p, want) {
			t.Fatalf("problem must name %q so the user can act: %s", want, p)
		}
	}
	if len(verified) != 1 || !strings.Contains(verified[0], "piccolo/runtime:NEW") {
		t.Fatalf("the good image should still be reported as verified: %v", verified)
	}
	if len(eng.probed) != 2 {
		t.Fatalf("both images must be probed, got %v", eng.probed)
	}
}

func TestVerifyMCPEnableChangePassesWhenImagesAgree(t *testing.T) {
	dir := t.TempDir()
	layout, m := verifyArgs(dir)
	eng := &stubVerifyEngine{state: docker.State{Found: true, Running: true, Image: m.Container.Image}}

	problems, verified, unverifiable := verifyMCPEnableChange(context.Background(), eng, m.Container.Name, layout, m, testServers, io.Discard)
	if unverifiable || len(problems) != 0 {
		t.Fatalf("expected a clean pass, problems=%v unverifiable=%v", problems, unverifiable)
	}
	if len(verified) != 1 || !strings.Contains(verified[0], "piccolo-gws 0.3.5") {
		t.Fatalf("verified line should name the answering server: %v", verified)
	}
	// No restart pending means one probe, not two.
	if len(eng.probed) != 1 {
		t.Fatalf("matching images should probe once, got %v", eng.probed)
	}
}

func TestVerifyMCPEnableChangeWithoutContainer(t *testing.T) {
	dir := t.TempDir()
	layout, m := verifyArgs(dir)
	eng := &stubVerifyEngine{state: docker.State{Found: false}}

	problems, _, unverifiable := verifyMCPEnableChange(context.Background(), eng, m.Container.Name, layout, m, testServers, io.Discard)
	if unverifiable || len(problems) != 0 || len(eng.probed) != 1 {
		t.Fatalf("a workspace with no container yet should probe the manifest image once and pass, got problems=%v probed=%v", problems, eng.probed)
	}
}

// "Could not verify" must be its own outcome: silently treating a dead daemon as a
// pass is how an unchecked toggle reaches production chat.
func TestVerifyMCPEnableChangeReportsUnverifiable(t *testing.T) {
	dir := t.TempDir()
	layout, m := verifyArgs(dir)
	eng := &stubVerifyEngine{pingErr: errors.New("cannot connect to the daemon")}

	problems, verified, unverifiable := verifyMCPEnableChange(context.Background(), eng, m.Container.Name, layout, m, testServers, io.Discard)
	if !unverifiable {
		t.Fatalf("expected unverifiable, got problems=%v verified=%v", problems, verified)
	}
	if len(eng.probed) != 0 {
		t.Fatalf("no probe should run without a daemon, got %v", eng.probed)
	}
}

func TestMCPServersInDoc(t *testing.T) {
	doc, err := configdoc.Parse([]byte(`{"tools":{"mcp":{"servers":{
		"zeta":{"enabled":true,"command":"zeta-mcp","args":["serve"]},
		"google_workspace":{"enabled":true,"command":"piccolo-gws","args":["mcp","--read-only"]},
		"off":{"enabled":false,"command":"nope"},
		"remote":{"type":"sse","url":"http://example"}
	}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	got := mcpServersInDoc(doc)
	var keys []string
	for _, s := range got {
		keys = append(keys, s.key)
	}
	// Sorted, and the two unspawnable entries absent.
	want := []string{"google_workspace", "zeta"}
	if strings.Join(keys, ",") != strings.Join(want, ",") {
		t.Fatalf("keys = %v, want %v", keys, want)
	}
	if strings.Join(got[0].argv, " ") != "piccolo-gws mcp --read-only" {
		t.Fatalf("argv = %v", got[0].argv)
	}
	if !sort.StringsAreSorted(keys) {
		t.Fatalf("verification order must be deterministic: %v", keys)
	}
}
