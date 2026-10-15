package doctor

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kimus/piccolo/internal/docker"
	"github.com/kimus/piccolo/internal/generate"
	"github.com/kimus/piccolo/internal/manifest"
	"github.com/kimus/piccolo/internal/runtimeimage"
	"github.com/kimus/piccolo/internal/runtimeplan"
)

// fakeEngine is a daemon-free doctor.Engine for tests.
type fakeEngine struct {
	pingErr      error
	imageExists  bool
	imageLabels  map[string]string
	labelsFound  bool
	labelsErr    error
	container    bool
	parkedTarget bool
	setupOrphans []string
	// images/containers default to a healthy workspace (the image in use, held
	// by its running container). Tests that want accumulation inject their own.
	images     []docker.ImageRecord
	containers map[string][]string
	// running is what the container serving traffic reports, image included: a
	// container can exist while being built from an older image than piccolo.yaml.
	running docker.State
}

func (f fakeEngine) Ping(context.Context) error { return f.pingErr }
func (f fakeEngine) ImageExists(context.Context, string) (bool, error) {
	return f.imageExists, nil
}
func (f fakeEngine) ImageLabels(context.Context, string) (map[string]string, bool, error) {
	return f.imageLabels, f.labelsFound, f.labelsErr
}
func (f fakeEngine) ContainerExists(_ context.Context, name string) (bool, error) {
	if strings.HasSuffix(name, docker.PreviousSuffix) {
		return f.parkedTarget, nil
	}
	return f.container, nil
}

// StateByName defaults to "no container", which is the state a fresh workspace has
// and keeps the running-versus-manifest check silent unless a test says otherwise.
func (f fakeEngine) StateByName(context.Context, string) (docker.State, error) {
	return f.running, nil
}

// ListImages/ContainerImages default to exactly what a healthy workspace has:
// the image in use, referenced by its running container. A test that wants
// accumulation injects its own records.
func (f fakeEngine) ListImages(context.Context) ([]docker.ImageRecord, error) {
	if f.images != nil {
		return f.images, nil
	}
	return []docker.ImageRecord{{ID: "sha256:cur", RepoTags: []string{"piccolo/picoclaw-runtime:cur"}, Size: 8 << 30, Containers: 1}}, nil
}

func (f fakeEngine) ContainerImages(context.Context) (map[string][]string, error) {
	if f.containers != nil {
		return f.containers, nil
	}
	return map[string][]string{"piccolo/picoclaw-runtime:cur": {"piccolo-gateway"}}, nil
}

func (f fakeEngine) ListByPrefix(_ context.Context, prefix string) ([]string, error) {
	var out []string
	for _, name := range f.setupOrphans {
		if strings.HasPrefix(name, prefix) {
			out = append(out, name)
		}
	}
	return out, nil
}

// initWorkspace generates a full valid workspace and returns its dir.
func initWorkspace(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	in := generate.Inputs{
		AssistantName: "Piccolo",
		OwnerName:     "Kimus",
		Purpose:       "personal",
		Specialists:   []string{"research"},
		Dashboard:     false,
		Model: generate.ModelInput{
			Alias: "gpt-5.4", Provider: "openai", Model: "gpt-5.4", APIKey: "sk-real-key",
		},
		WebSearch: true,
	}
	plan, err := generate.BuildPlan(in)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if _, _, err := plan.Write(dir, nil); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "integrations"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Real init writes the ignore file; a fixture that skips it would make every
	// workspace look half-generated to doctor.
	if _, err := generate.EnsureGitignore(dir); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{filepath.Join("runtime", "devbox"), filepath.Join("runtime", "nix"), filepath.Join("runtime", "media")} {
		if err := os.MkdirAll(filepath.Join(dir, rel), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func statuses(results []Result) map[Status]int {
	counts := map[Status]int{}
	for _, r := range results {
		counts[r.Status]++
	}
	return counts
}

func find(results []Result, substr string) *Result {
	for i := range results {
		if strings.Contains(results[i].Message, substr) {
			return &results[i]
		}
	}
	return nil
}

func TestDoctorReportsInstalledIntegrations(t *testing.T) {
	dir := initWorkspace(t)
	path := filepath.Join(dir, "piccolo.yaml")
	m, err := manifest.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	m.Runtime.Integrations = []manifest.Integration{{Name: "gws", DisplayName: "Google Workspace", Ref: "github:kimus/piccolo-gws", SetupCommand: []string{"piccolo-gws", "setup"}}}
	if err := m.Write(path); err != nil {
		t.Fatal(err)
	}
	results := Run(context.Background(), Options{Workspace: dir, Engine: fakeEngine{imageExists: true}, LookupEnv: func(string) (string, bool) { return "", false }})
	if r := find(results, "integration installed: Google Workspace"); r == nil || r.Status != OK {
		t.Fatalf("integration result not found: %#v", results)
	}
}

// Registration is the LAST act of 'piccolo integration setup' — the server entry is
// written from the action's configPatch through a confirm gate the user can decline
// ("discarded; integration state was left as-is"). That leaves a workspace where the
// image has the binary, the manifest lists the integration, and config.json has never
// heard of it: the user's experience is "I added it and it silently does nothing",
// and before this check nothing compared the two files.
func TestDoctorWarnsOnInstalledButUnregisteredIntegration(t *testing.T) {
	dir := initWorkspace(t)
	path := filepath.Join(dir, "piccolo.yaml")
	m, err := manifest.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	m.Runtime.Integrations = []manifest.Integration{{
		Name: "gws", DisplayName: "Google Workspace",
		// Package only: Ref is the retired source-build field and is empty on every
		// release-based integration, exactly the shape that used to render an empty
		// remedy — "piccolo integration setup " with nothing to run.
		Package:      "github:kimus/piccolo-gws",
		MCPServerKey: "google_workspace",
		SetupCommand: []string{"piccolo-gws", "setup"},
	}}
	if err := m.Write(path); err != nil {
		t.Fatal(err)
	}

	results := Run(context.Background(), Options{Workspace: dir, Engine: fakeEngine{imageExists: true}, LookupEnv: func(string) (string, bool) { return "", false }})
	r := find(results, "never registered")
	if r == nil || r.Status != WARN {
		t.Fatalf("expected never-registered WARN, got %v", results)
	}
	for _, want := range []string{"google_workspace", "piccolo integration setup github:kimus/piccolo-gws"} {
		if !strings.Contains(r.Message, want) {
			t.Fatalf("WARN must name %q so the user knows what to run: %s", want, r.Message)
		}
	}
	// The installed line carries a followable handle too, not empty parentheses.
	if r := find(results, "integration installed: Google Workspace (github:kimus/piccolo-gws)"); r == nil {
		t.Fatalf("installed integration should name its package: %v", results)
	}
	if Failed(results) {
		t.Fatalf("an unfinished setup is a WARN, never a FAIL: %v", results)
	}

	// Registering it clears the finding.
	patchConfig(t, dir, func(cfg map[string]any) {
		tools := cfg["tools"].(map[string]any)
		tools["mcp"] = map[string]any{"enabled": true, "servers": map[string]any{
			"google_workspace": map[string]any{"type": "stdio", "enabled": true, "command": "piccolo-gws", "args": []any{"mcp"}},
		}}
	})
	results = Run(context.Background(), Options{Workspace: dir, Engine: fakeEngine{imageExists: true}, LookupEnv: func(string) (string, bool) { return "", false }})
	if r := find(results, "never registered"); r != nil {
		t.Fatalf("registered integration must not be warned about: %+v", *r)
	}
}

// An integration that needs no MCP server must not be reported for lacking one:
// inventing a requirement is how every warning gets ignored.
func TestDoctorSilentOnIntegrationWithoutMCPServer(t *testing.T) {
	dir := initWorkspace(t)
	path := filepath.Join(dir, "piccolo.yaml")
	m, err := manifest.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	m.Runtime.Integrations = []manifest.Integration{{
		Name: "notes", DisplayName: "Notes helper", Ref: "github:kimus/piccolo-notes",
		SetupCommand: []string{"piccolo-notes", "setup"},
	}}
	if err := m.Write(path); err != nil {
		t.Fatal(err)
	}
	results := Run(context.Background(), Options{Workspace: dir, Engine: fakeEngine{imageExists: true}, LookupEnv: func(string) (string, bool) { return "", false }})
	if r := find(results, "never registered"); r != nil {
		t.Fatalf("no MCP server claimed, so nothing is missing: %+v", *r)
	}
}

func TestDoctorFreshWorkspaceAllOK(t *testing.T) {
	dir := initWorkspace(t)
	results := Run(context.Background(), Options{
		Workspace: dir,
		Engine:    fakeEngine{imageExists: true},
		LookupEnv: func(string) (string, bool) { return "", false },
	})
	if Failed(results) {
		t.Fatalf("unexpected failures: %v", results)
	}
	for _, s := range statuses(results) {
		_ = s
	}
	if r := find(results, "["); r != nil {
		// no-op; statuses checked below
	}
	counts := statuses(results)
	if counts[WARN] != 0 {
		t.Errorf("unexpected warnings: %v", results)
	}
	for _, want := range []string{
		"docker daemon reachable",
		"image present",
		"manifest: piccolo.yaml",
		"schema v3",
		"agent: main (default)",
		"agent: research",
		"model: gpt-5.4",
	} {
		if find(results, want) == nil {
			t.Errorf("missing OK check %q in %v", want, results)
		}
	}
}

func TestDoctorWarnsAbandonedSetupContainers(t *testing.T) {
	dir := initWorkspace(t)
	results := Run(context.Background(), Options{
		Workspace: dir,
		Engine: fakeEngine{
			imageExists:  true,
			setupOrphans: []string{"piccolo-setup-gws-1791932406936295000"},
		},
		LookupEnv: func(string) (string, bool) { return "", false },
	})
	if Failed(results) {
		t.Fatalf("abandoned one-shot containers must only warn: %v", results)
	}
	found := false
	for _, r := range results {
		if r.Status == WARN && strings.Contains(r.Message, "abandoned integration one-shot") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected orphan warning in %v", results)
	}
}

func TestDoctorDaemonDown(t *testing.T) {
	dir := initWorkspace(t)
	results := Run(context.Background(), Options{
		Workspace: dir,
		Engine:    fakeEngine{pingErr: errors.New("connection refused")},
	})
	if !Failed(results) {
		t.Fatal("expected failure when daemon is down")
	}
	r := find(results, "docker daemon not reachable")
	if r == nil || r.Status != FAIL {
		t.Fatalf("expected daemon FAIL, got %v", results)
	}
}

func TestDoctorImageMissingWarnsOnly(t *testing.T) {
	dir := initWorkspace(t)
	results := Run(context.Background(), Options{
		Workspace: dir,
		Engine:    fakeEngine{imageExists: false},
	})
	if Failed(results) {
		t.Fatalf("missing image must not fail: %v", results)
	}
	r := find(results, "image not present")
	if r == nil || r.Status != WARN {
		t.Fatalf("expected image WARN, got %v", results)
	}
}

func TestDoctorMissingManifest(t *testing.T) {
	dir := t.TempDir()
	results := Run(context.Background(), Options{Workspace: dir, Engine: fakeEngine{}})
	if !Failed(results) {
		t.Fatal("expected failure without manifest")
	}
	if find(results, "manifest") == nil {
		t.Fatalf("expected manifest FAIL, got %v", results)
	}
}

func TestDoctorMissingAgentFile(t *testing.T) {
	dir := initWorkspace(t)
	os.Remove(filepath.Join(dir, "picoclaw", "agents", "research", "AGENT.md"))
	results := Run(context.Background(), Options{Workspace: dir, Engine: fakeEngine{imageExists: true}})
	if !Failed(results) {
		t.Fatal("expected failure for missing AGENT.md")
	}
	r := find(results, "missing agent file")
	if r == nil || r.Status != FAIL {
		t.Fatalf("expected missing-agent FAIL, got %v", results)
	}
}

func TestDoctorPlaceholderKeyWarns(t *testing.T) {
	dir := initWorkspace(t)
	patchConfig(t, dir, func(cfg map[string]any) {
		ml := cfg["model_list"].([]any)
		entry := ml[0].(map[string]any)
		entry["api_keys"] = []any{"sk-your-openai-key"}
	})
	results := Run(context.Background(), Options{Workspace: dir, Engine: fakeEngine{imageExists: true}})
	if Failed(results) {
		t.Fatalf("placeholder key must not fail: %v", results)
	}
	r := find(results, "placeholder")
	if r == nil || r.Status != WARN {
		t.Fatalf("expected placeholder WARN, got %v", results)
	}
}

func TestDoctorTwoDefaultsFail(t *testing.T) {
	dir := initWorkspace(t)
	patchConfig(t, dir, func(cfg map[string]any) {
		list := cfg["agents"].(map[string]any)["list"].([]any)
		list[1].(map[string]any)["default"] = true
	})
	results := Run(context.Background(), Options{Workspace: dir, Engine: fakeEngine{imageExists: true}})
	if !Failed(results) {
		t.Fatal("expected failure for two default agents")
	}
	if find(results, "default agents") == nil {
		t.Fatalf("expected defaults FAIL, got %v", results)
	}
}

func TestDoctorUnknownSubagentFails(t *testing.T) {
	dir := initWorkspace(t)
	patchConfig(t, dir, func(cfg map[string]any) {
		list := cfg["agents"].(map[string]any)["list"].([]any)
		list[0].(map[string]any)["subagents"] = map[string]any{"allow_agents": []any{"ghost"}}
	})
	results := Run(context.Background(), Options{Workspace: dir, Engine: fakeEngine{imageExists: true}})
	if !Failed(results) {
		t.Fatal("expected failure for unknown subagent reference")
	}
	if find(results, `unknown agent "ghost"`) == nil {
		t.Fatalf("expected subagent FAIL, got %v", results)
	}
}

func TestDoctorEnvRefWarns(t *testing.T) {
	dir := initWorkspace(t)
	// Rewrite piccolo.yaml with an env reference.
	yamlPath := filepath.Join(dir, "piccolo.yaml")
	content := `version: 1
container:
  image: docker.io/sipeed/picoclaw:v0.3.1
  name: piccolo-gateway
  network: bridge
  env:
    SECRET: env:HOST_SECRET
`
	if err := os.WriteFile(yamlPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	results := Run(context.Background(), Options{
		Workspace: dir,
		Engine:    fakeEngine{imageExists: true},
		LookupEnv: func(string) (string, bool) { return "", false },
	})
	if Failed(results) {
		t.Fatalf("unresolved env ref must not fail: %v", results)
	}
	if r := find(results, "HOST_SECRET"); r == nil || r.Status != WARN {
		t.Fatalf("expected env WARN, got %v", results)
	}
}

func TestDoctorStaleContainerWarns(t *testing.T) {
	dir := initWorkspace(t)
	m, err := manifest.Load(filepath.Join(dir, "piccolo.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	results := Run(context.Background(), Options{
		Workspace: dir,
		Engine: fakeEngine{imageExists: true, container: true, running: docker.State{
			Found: true, Running: false, Status: "exited", Image: m.Container.Image,
		}},
	})
	if Failed(results) {
		t.Fatalf("stale container must not fail: %v", results)
	}
	r := find(results, "stale container")
	if r == nil || r.Status != WARN {
		t.Fatalf("expected stale WARN, got %v", results)
	}
}

// A running container built from the current image is the service, not a leftover.
// Warning about it unconditionally gave every healthy workspace a permanent false
// warning, which is how warnings get ignored.
func TestDoctorDoesNotCallHealthyRunningContainerStale(t *testing.T) {
	dir := initWorkspace(t)
	m, err := manifest.Load(filepath.Join(dir, "piccolo.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	results := Run(context.Background(), Options{
		Workspace: dir,
		Engine: fakeEngine{imageExists: true, container: true, running: docker.State{
			Found: true, Running: true, Status: "running", Image: m.Container.Image,
		}},
	})
	if r := find(results, "stale container"); r != nil {
		t.Fatalf("the current running gateway is not stale: %+v", *r)
	}
	if r := find(results, "running container image"); r == nil || r.Status != OK {
		t.Fatalf("expected running-image OK, got %v", results)
	}
}

// Running-but-stale gets ONE warning, with the verb that fits (restart, not
// "serve will replace it"), rather than two complaints about one condition.
func TestDoctorSingleWarningForRunningStaleImage(t *testing.T) {
	dir := initWorkspace(t)
	results := Run(context.Background(), Options{
		Workspace: dir,
		Engine: fakeEngine{imageExists: true, container: true, running: docker.State{
			Found: true, Running: true, Status: "running", Image: "piccolo/picoclaw-runtime:OLDGENERATION",
		}},
	})
	if r := find(results, "stale container"); r != nil {
		t.Fatalf("a running container is not a leftover: %+v", *r)
	}
	if r := find(results, "but piccolo.yaml names"); r == nil || r.Status != WARN {
		t.Fatalf("expected the pending-restart WARN, got %v", results)
	}
}

// TestDoctorConsoleOffNoWarning asserts a disabled web console produces no
// warning or failure attributable to its absence.
func TestDoctorConsoleOffNoWarning(t *testing.T) {
	// Unit-level: a gateway-image manifest with no :18800 yields no results.
	m := &manifest.Manifest{Container: manifest.Container{
		Image: generate.DefaultImage, Name: "piccolo-gateway", Network: "bridge",
	}}
	if res := checkWebConsole(m); len(res) != 0 {
		t.Fatalf("console-off checkWebConsole = %v, want no results", res)
	}

	// Full run over a console-off workspace stays warning-free.
	dir := initWorkspace(t)
	results := Run(context.Background(), Options{
		Workspace: dir,
		Engine:    fakeEngine{imageExists: true},
		LookupEnv: func(string) (string, bool) { return "", false },
	})
	if Failed(results) {
		t.Fatalf("console-off must not fail: %v", results)
	}
	if statuses(results)[WARN] != 0 {
		t.Errorf("console-off must not warn: %v", results)
	}
}

// TestDoctorConsoleBindingImageMismatchWarns asserts a :18800 binding paired
// with a non-launcher image is flagged as an inconsistency (dead port).
func TestDoctorConsoleBindingImageMismatchWarns(t *testing.T) {
	m := &manifest.Manifest{Container: manifest.Container{
		Image: generate.DefaultImage, Name: "piccolo-gateway", Network: "bridge",
		Ports: []string{"127.0.0.1:18800:18800"},
	}}
	res := checkWebConsole(m)
	if len(res) != 1 || res[0].Status != WARN {
		t.Fatalf("mismatch checkWebConsole = %v, want one WARN", res)
	}
	if !strings.Contains(res[0].Message, "not the launcher image") {
		t.Errorf("unexpected message: %q", res[0].Message)
	}
}

// TestDoctorConsoleEnabledOK asserts a launcher image with :18800 is OK.
func TestDoctorConsoleEnabledOK(t *testing.T) {
	m := &manifest.Manifest{Container: manifest.Container{
		Image: generate.LauncherImage, Name: "piccolo-gateway", Network: "bridge",
		Ports: []string{"127.0.0.1:18800:18800"},
	}}
	res := checkWebConsole(m)
	if len(res) != 1 || res[0].Status != OK {
		t.Fatalf("console-on checkWebConsole = %v, want one OK", res)
	}
}

func TestDoctorImageUnderstandingWarnings(t *testing.T) {
	dir := initWorkspace(t)
	patchConfig(t, dir, func(cfg map[string]any) {
		defaults := cfg["agents"].(map[string]any)["defaults"].(map[string]any)
		delete(defaults, "image_model")
	})
	results := Run(context.Background(), Options{Workspace: dir, Engine: fakeEngine{imageExists: true}})
	if Failed(results) {
		t.Fatalf("missing image_model must warn, not fail: %v", results)
	}
	if r := find(results, "image_model is not configured"); r == nil || r.Status != WARN {
		t.Fatalf("expected image_model WARN, got %v", results)
	}

	dir = initWorkspace(t)
	patchConfig(t, dir, func(cfg map[string]any) {
		tools := cfg["tools"].(map[string]any)
		tools["load_image"] = map[string]any{"enabled": false}
	})
	results = Run(context.Background(), Options{Workspace: dir, Engine: fakeEngine{imageExists: true}})
	if r := find(results, "tools.load_image.enabled is false"); r == nil || r.Status != WARN {
		t.Fatalf("expected load_image disabled WARN, got %v", results)
	}

	// Aimed at a specialist: the coordinator declares no allowlist any more, so
	// there is no list of its own to fall out of sync with load_image.
	dir = initWorkspace(t)
	agentPath := filepath.Join(dir, "picoclaw", "agents", "research", "AGENT.md")
	data, err := os.ReadFile(agentPath)
	if err != nil {
		t.Fatal(err)
	}
	content := strings.Replace(string(data), ", load_image", "", 1)
	if content == string(data) {
		t.Fatal("research AGENT.md does not declare load_image; test no longer exercises the allowlist path")
	}
	if err := os.WriteFile(agentPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	results = Run(context.Background(), Options{Workspace: dir, Engine: fakeEngine{imageExists: true}})
	if r := find(results, `agent "research" AGENT.md does not allow load_image`); r == nil || r.Status != WARN {
		t.Fatalf("expected agent load_image WARN, got %v", results)
	}
}

// The workspace can hold the right image while the CONTAINER serving traffic runs an
// older one: `piccolo integration add` bakes a new image and rewrites container.image,
// but only a restart swaps the running binaries — while bind-mounted config.json is
// re-read immediately. Every other image check here consults the image store, so this
// state was invisible, and it breaks the gateway wholesale: enabling an integration's
// MCP tool against a container that lacks the fixed binary fails every chat message.
func TestDoctorWarnsWhenRunningContainerLagsManifestImage(t *testing.T) {
	dir := initWorkspace(t)
	m, err := manifest.Load(filepath.Join(dir, "piccolo.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	results := Run(context.Background(), Options{Workspace: dir, Engine: fakeEngine{
		imageExists: true,
		container:   true,
		running:     docker.State{Found: true, Running: true, Status: "running", Image: "piccolo/picoclaw-runtime:OLDGENERATION"},
	}})
	r := find(results, "but piccolo.yaml names")
	if r == nil || r.Status != WARN {
		t.Fatalf("expected stale-running-image WARN, got %v", results)
	}
	for _, want := range []string{"piccolo/picoclaw-runtime:OLDGENERATION", m.Container.Image, "piccolo restart"} {
		if !strings.Contains(r.Message, want) {
			t.Fatalf("WARN must name %q so the user can act: %s", want, r.Message)
		}
	}
	if Failed(results) {
		t.Fatalf("a pending restart is a WARN, never a FAIL: %v", results)
	}
}

func TestDoctorReportsMatchingRunningImage(t *testing.T) {
	dir := initWorkspace(t)
	m, err := manifest.Load(filepath.Join(dir, "piccolo.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	results := Run(context.Background(), Options{Workspace: dir, Engine: fakeEngine{
		imageExists: true,
		container:   true,
		running:     docker.State{Found: true, Running: true, Status: "running", Image: m.Container.Image},
	}})
	if r := find(results, "running container image"); r == nil || r.Status != OK {
		t.Fatalf("expected matching-image OK, got %v", results)
	}
	if r := find(results, "but piccolo.yaml names"); r != nil {
		t.Fatalf("matching images must not warn: %s", r.Message)
	}
}

// No container is not a lagging container: the stale-container and image checks
// already speak to an absent one, and a second complaint would be noise.
func TestDoctorSilentOnRunningImageWhenNoContainer(t *testing.T) {
	dir := initWorkspace(t)
	results := Run(context.Background(), Options{Workspace: dir, Engine: fakeEngine{imageExists: true}})
	if r := find(results, "piccolo.yaml names"); r != nil {
		t.Fatalf("no container should mean no running-image finding: %s", r.Message)
	}
}

func TestDoctorWarnsOnRemoteExec(t *testing.T) {
	dir := initWorkspace(t)
	patchConfig(t, dir, func(cfg map[string]any) {
		tools := cfg["tools"].(map[string]any)
		exec := tools["exec"].(map[string]any)
		exec["enabled"] = true
		exec["allow_remote"] = true
	})
	results := Run(context.Background(), Options{Workspace: dir, Engine: fakeEngine{imageExists: true}})
	if r := find(results, "tools.exec.allow_remote is true"); r == nil || r.Status != WARN {
		t.Fatalf("expected remote exec WARN, got %v", results)
	}
}

// PicoClaw's exec path guard takes the raw restrict flag, not the read relaxed by
// allow_read_outside_workspace (verified v0.3.1 pkg/agent/instance.go:85-86,113),
// so restrict + exec is the combination that produces "Command blocked by safety
// guard (path outside working dir)" for ordinary commands. Piccolo generates the
// flag off, so this WARN exists to tell an older or hand-hardened workspace why
// its agent keeps failing.
func TestDoctorWarnsOnExecPathGuard(t *testing.T) {
	dir := initWorkspace(t)
	patchConfig(t, dir, func(cfg map[string]any) {
		defaults := cfg["agents"].(map[string]any)["defaults"].(map[string]any)
		defaults["restrict_to_workspace"] = true
		tools := cfg["tools"].(map[string]any)
		tools["exec"].(map[string]any)["enabled"] = true
	})
	results := Run(context.Background(), Options{Workspace: dir, Engine: fakeEngine{imageExists: true}})
	r := find(results, "restrict_to_workspace is true while tools.exec is enabled")
	if r == nil || r.Status != WARN {
		t.Fatalf("expected exec path-guard WARN, got %v", results)
	}
	if !strings.Contains(r.Message, "allow_read_outside_workspace does NOT relax") {
		t.Fatalf("WARN must state that read access does not relax exec, got: %s", r.Message)
	}
	if Failed(results) {
		t.Fatalf("path guard is a user choice, never a FAIL: %v", results)
	}
}

func TestDoctorAcceptsGeneratedWorkspaceWithoutPathGuardWarning(t *testing.T) {
	dir := initWorkspace(t)
	results := Run(context.Background(), Options{Workspace: dir, Engine: fakeEngine{imageExists: true}})
	if r := find(results, "restrict_to_workspace"); r != nil {
		t.Fatalf("fresh workspace must not be warned about the path guard: %+v", *r)
	}
}

// An absent key is PicoClaw's default, which is TRUE: silence here would report a
// workspace as unrestricted while its agents hit the guard on every command.
func TestDoctorTreatsMissingPathGuardKeyAsRestricted(t *testing.T) {
	dir := initWorkspace(t)
	patchConfig(t, dir, func(cfg map[string]any) {
		defaults := cfg["agents"].(map[string]any)["defaults"].(map[string]any)
		delete(defaults, "restrict_to_workspace")
	})
	results := Run(context.Background(), Options{Workspace: dir, Engine: fakeEngine{imageExists: true}})
	if r := find(results, "restrict_to_workspace"); r == nil || r.Status != WARN {
		t.Fatalf("expected WARN for an absent (therefore on) path guard, got %v", results)
	}
}

// A gateway in this state answers /health and runs its integration binaries, so
// the serve readiness gate passes while the agent is handed none of the
// integration's tools. PicoClaw skips MCP initialization unless tools.mcp.enabled
// is true (pkg/agent/agent_mcp.go:79) and defaults it to false
// (pkg/config/defaults.go:428-431).
func TestDoctorWarnsOnRegisteredButUnreachableMCPServer(t *testing.T) {
	dir := initWorkspace(t)
	patchConfig(t, dir, func(cfg map[string]any) {
		tools := cfg["tools"].(map[string]any)
		tools["mcp"] = map[string]any{
			"enabled": false,
			"servers": map[string]any{
				"google_workspace": map[string]any{"enabled": true, "command": "piccolo-gws"},
			},
		}
	})
	results := Run(context.Background(), Options{Workspace: dir, Engine: fakeEngine{imageExists: true}})
	r := find(results, "tools.mcp.enabled is not true")
	if r == nil || r.Status != WARN {
		t.Fatalf("expected unreachable-MCP WARN, got %v", results)
	}
	if !strings.Contains(r.Message, "google_workspace") {
		t.Fatalf("WARN must name the registered server: %s", r.Message)
	}
	if Failed(results) {
		t.Fatalf("unreachable MCP is a WARN, not a FAIL: %v", results)
	}
}

// The other gate: a declared `tools:` key becomes a strict exact-name allowlist
// (pkg/agent/tool_allowlist.go:143-164 → pkg/tools/registry.go:171-184), and MCP
// tools are named mcp_<server>_<tool> only at runtime, so no such list can hold
// them. Piccolo generates the coordinator without one.
func TestDoctorWarnsWhenCoordinatorDeclaresToolAllowlist(t *testing.T) {
	dir := initWorkspace(t)
	patchConfig(t, dir, func(cfg map[string]any) {
		tools := cfg["tools"].(map[string]any)
		tools["mcp"] = map[string]any{
			"enabled": true,
			"servers": map[string]any{
				"google_workspace": map[string]any{"enabled": true, "command": "piccolo-gws"},
			},
		}
	})
	path := filepath.Join(dir, "picoclaw", "agents", "main", "AGENT.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "\ntools:") {
		t.Fatal("generated coordinator must not declare a tools: allowlist")
	}
	fm := strings.Replace(string(data), "\nskills:", "\ntools: [message]\nskills:", 1)
	if err := os.WriteFile(path, []byte(fm), 0o644); err != nil {
		t.Fatal(err)
	}
	results := Run(context.Background(), Options{Workspace: dir, Engine: fakeEngine{imageExists: true}})
	r := find(results, "declares an AGENT.md tools: allowlist")
	if r == nil || r.Status != WARN {
		t.Fatalf("expected coordinator-allowlist WARN, got %v", results)
	}
	if !strings.Contains(r.Message, "mcp_<server>_<tool>") {
		t.Fatalf("WARN must explain why the list cannot name MCP tools: %s", r.Message)
	}
}

func TestDoctorReportsMCPReachableForGeneratedCoordinator(t *testing.T) {
	dir := initWorkspace(t)
	patchConfig(t, dir, func(cfg map[string]any) {
		tools := cfg["tools"].(map[string]any)
		tools["mcp"] = map[string]any{
			"enabled": true,
			"servers": map[string]any{
				"google_workspace": map[string]any{"enabled": true, "command": "piccolo-gws"},
			},
		}
	})
	results := Run(context.Background(), Options{Workspace: dir, Engine: fakeEngine{imageExists: true}})
	if r := find(results, "reachable by coordinator"); r == nil || r.Status != OK {
		t.Fatalf("expected reachable OK, got %v", results)
	}
	if r := find(results, "tools.mcp.enabled is not true"); r != nil {
		t.Fatalf("unexpected WARN while MCP is reachable: %s", r.Message)
	}
}

// Nothing installed is not the same as something broken: a workspace without
// integration servers gets no MCP finding at all.
func TestDoctorStaysSilentOnMCPWithoutIntegrations(t *testing.T) {
	dir := initWorkspace(t)
	results := Run(context.Background(), Options{Workspace: dir, Engine: fakeEngine{imageExists: true}})
	if r := find(results, "mcp:"); r != nil {
		t.Fatalf("no integrations installed, so no MCP finding expected: %+v", *r)
	}
}

func TestDoctorImageModelReferenceMustExist(t *testing.T) {
	dir := initWorkspace(t)
	patchConfig(t, dir, func(cfg map[string]any) {
		defaults := cfg["agents"].(map[string]any)["defaults"].(map[string]any)
		defaults["image_model"] = "vision-missing"
	})
	results := Run(context.Background(), Options{Workspace: dir, Engine: fakeEngine{imageExists: true}})
	if !Failed(results) {
		t.Fatal("expected failure for missing image_model reference")
	}
	if find(results, `agents.defaults.image_model references model "vision-missing" missing from model_list`) == nil {
		t.Fatalf("expected missing image model FAIL, got %v", results)
	}
}

// TestDoctorNamesEveryModelReferenceSite pins the two things the old check
// could not say: which field held the dangling alias, and that the agent-level
// field is "model" — the key Piccolo used to write, "model_name", is ignored by
// PicoClaw, so a workspace carrying it must see a warning naming the fix.
func TestDoctorNamesEveryModelReferenceSite(t *testing.T) {
	dir := initWorkspace(t)
	patchConfig(t, dir, func(cfg map[string]any) {
		agents := cfg["agents"].(map[string]any)
		agents["defaults"].(map[string]any)["model_fallbacks"] = []any{"ghost-fallback"}
		list := agents["list"].([]any)
		list[1].(map[string]any)["model"] = "ghost-agent"
	})
	results := Run(context.Background(), Options{Workspace: dir, Engine: fakeEngine{imageExists: true}})
	if !Failed(results) {
		t.Fatal("expected failures for dangling model references")
	}
	for _, want := range []string{
		`agents.defaults.model_fallbacks[0] references model "ghost-fallback" missing from model_list`,
		`agents.list[research].model references model "ghost-agent" missing from model_list`,
	} {
		if r := find(results, want); r == nil || r.Status != FAIL {
			t.Errorf("missing FAIL %q in %v", want, results)
		}
	}
}

func TestDoctorModelObjectForm(t *testing.T) {
	dir := initWorkspace(t)
	patchConfig(t, dir, func(cfg map[string]any) {
		list := cfg["agents"].(map[string]any)["list"].([]any)
		list[1].(map[string]any)["model"] = map[string]any{
			"primary":   "ghost-primary",
			"fallbacks": []any{"gpt-5.4", "ghost-second"},
		}
	})
	results := Run(context.Background(), Options{Workspace: dir, Engine: fakeEngine{imageExists: true}})
	if find(results, `agents.list[research].model references model "ghost-primary"`) == nil {
		t.Fatalf("object-form primary not checked: %v", results)
	}
	if find(results, `agents.list[research].model.fallbacks[1] references model "ghost-second"`) == nil {
		t.Fatalf("object-form fallbacks not checked: %v", results)
	}
	// The valid fallback must NOT be reported.
	if find(results, `"gpt-5.4"`) != nil {
		t.Errorf("valid alias reported: %v", results)
	}
}

func TestDoctorWarnsOnIgnoredAgentModelName(t *testing.T) {
	dir := initWorkspace(t)
	patchConfig(t, dir, func(cfg map[string]any) {
		list := cfg["agents"].(map[string]any)["list"].([]any)
		// A workspace written by an older Piccolo: the alias sits in the key
		// PicoClaw never reads, so the agent silently uses the default model.
		list[1].(map[string]any)["model_name"] = "gpt-5.4"
	})
	results := Run(context.Background(), Options{Workspace: dir, Engine: fakeEngine{imageExists: true}})
	if Failed(results) {
		t.Fatalf("a valid alias must not fail on the legacy key: %v", results)
	}
	r := find(results, `agent "research" sets model_name "gpt-5.4", which PicoClaw ignores`)
	if r == nil || r.Status != WARN {
		t.Fatalf("expected legacy-key WARN, got %v", results)
	}
}

// TestDoctorChecksAgentMDModelPin: an AGENT.md frontmatter model outranks
// config.json (verified v0.3.1 pkg/agent/instance.go:380-392), so a stale pin
// there breaks the agent while config.json looks clean.
func TestDoctorChecksAgentMDModelPin(t *testing.T) {
	dir := initWorkspace(t)
	path := filepath.Join(dir, "picoclaw", "agents", "research", "AGENT.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content := strings.Replace(string(data), "tools:", "model: ghost-pin\ntools:", 1)
	if !strings.Contains(content, "model: ghost-pin") {
		t.Fatal("fixture AGENT.md has no tools: line to anchor the edit")
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	results := Run(context.Background(), Options{Workspace: dir, Engine: fakeEngine{imageExists: true}})
	r := find(results, `agent "research": AGENT.md pins model "ghost-pin" missing from model_list`)
	if r == nil || r.Status != FAIL {
		t.Fatalf("expected pinned-model FAIL, got %v", results)
	}
}

func TestTranslateWorkspacePath(t *testing.T) {
	for _, in := range []string{"~/.picoclaw/agents/main", "/root/.picoclaw/agents/main"} {
		got, ok := translateWorkspacePath("/ws", in)
		if !ok || got != filepath.Join("/ws", "picoclaw", "agents", "main") {
			t.Errorf("%s -> %s, %v", in, got, ok)
		}
	}
	if _, ok := translateWorkspacePath("/ws", "/elsewhere/agents/main"); ok {
		t.Error("path outside container home must not translate")
	}
}

// patchConfig rewrites config.json through a generic map mutation.
func patchConfig(t *testing.T, dir string, mutate func(map[string]any)) {
	t.Helper()
	path := filepath.Join(dir, "picoclaw", "config.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	mutate(cfg)
	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCheckChannels(t *testing.T) {
	cfg := []byte(`{"channel_list":{
		"discord":{"enabled":true,"type":"discord","allow_from":["123"],"settings":{"token":"real","proxy":""}},
		"telegram":{"enabled":true,"type":"telegram","allow_from":[],"settings":{"token":"","streaming":{"enabled":true}}},
		"slack":{"enabled":false,"type":"slack","settings":{"bot_token":"xoxb-1"}},
		"open":{"enabled":true,"type":"discord","allow_from":["*"],"settings":{"token":"t"}}
	}}`)
	res := checkChannels(cfg)
	var got = map[string]Status{}
	for _, r := range res {
		got[r.Message] = r.Status
	}
	if len(res) != 4 {
		t.Fatalf("results = %+v", res)
	}
	byName := map[string]Result{}
	for _, r := range res {
		switch {
		case strings.Contains(r.Message, "discord"):
			byName["discord"] = r
		case strings.Contains(r.Message, "telegram"):
			byName["telegram"] = r
		case strings.Contains(r.Message, "slack"):
			byName["slack"] = r
		case strings.Contains(r.Message, `"*"`) && strings.Contains(r.Message, "open"):
			byName["open"] = r
		}
	}
	if byName["discord"].Status != OK {
		t.Errorf("good discord: %+v", byName["discord"])
	}
	if byName["telegram"].Status != WARN {
		t.Errorf("placeholder telegram: %+v", byName["telegram"])
	}
	if byName["slack"].Status != OK {
		t.Errorf("disabled slack: %+v", byName["slack"])
	}
	if byName["open"].Status != WARN {
		t.Errorf("wildcard open: %+v", byName["open"])
	}
	_ = got

	// No channel_list → no results, never a failure.
	if n := len(checkChannels([]byte(`{"version":3}`))); n != 0 {
		t.Errorf("absent channel_list gave %d results", n)
	}
}

// A stale runtime image must be reported with its reason, and never repaired by
// doctor: adopting a rebuild is the user's decision.
func TestDoctorReportsStaleRuntimeImage(t *testing.T) {
	dir := initWorkspace(t)
	path := filepath.Join(dir, "piccolo.yaml")
	m, err := manifest.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	desired, err := runtimeplan.Plan(m)
	if err != nil {
		t.Fatal(err)
	}
	m.Container.Image = desired.Tag
	if err := m.Write(path); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// Up to date: the workspace points at exactly the plan the metadata implies.
	results := Run(ctx, Options{Workspace: dir, Engine: fakeEngine{
		imageExists: true, labelsFound: true, imageLabels: desired.Labels(),
	}})
	r := find(results, "runtime image matches this workspace")
	if r == nil || r.Status != OK {
		t.Fatalf("expected an OK currency line: %#v", results)
	}

	// Toolchain moved: the recorded image is a different tag, and its labels name
	// what changed. Comparing labels (not just tags) is what makes the warning
	// actionable.
	m.Container.Image = desired.Tag[:len(desired.Tag)-12] + "000000000000"
	if err := m.Write(path); err != nil {
		t.Fatal(err)
	}
	lagging := desired.Labels()
	lagging[runtimeimage.LabelToolchain] = "sha256:000000000000"
	results = Run(ctx, Options{Workspace: dir, Engine: fakeEngine{
		imageExists: true, labelsFound: true, imageLabels: lagging,
	}})
	r = find(results, "runtime image is stale")
	if r == nil || r.Status != WARN {
		t.Fatalf("expected a stale warning: %#v", results)
	}
	if !strings.Contains(r.Message, "toolchain package list changed") {
		t.Errorf("stale warning lost the reason: %s", r.Message)
	}
	if !strings.Contains(r.Message, "piccolo image build --set") {
		t.Errorf("stale warning must name the rebuild command: %s", r.Message)
	}
	if Failed(results) {
		t.Error("stale image must warn, never fail")
	}

	// An image built before content labels cannot be attributed, and doctor says
	// that instead of inventing a reason. (An image that is absent entirely gets
	// the "not present locally" reason, which the presence check already covers.)
	results = Run(ctx, Options{Workspace: dir, Engine: fakeEngine{imageExists: true, labelsFound: true}})
	r = find(results, "runtime image is stale")
	if r == nil || !strings.Contains(r.Message, "predates Piccolo content labels") {
		t.Fatalf("expected the predates-labels reason: %#v", results)
	}

	// Image missing locally: the presence check owns that message, and the drift
	// check stays quiet rather than reporting the same problem twice.
	results = Run(ctx, Options{Workspace: dir, Engine: fakeEngine{imageExists: false}})
	if r := find(results, "runtime image is stale"); r != nil {
		t.Errorf("a missing image must not also be reported as content drift: %s", r.Message)
	}
	if r := find(results, "local Piccolo runtime image not present"); r == nil {
		t.Errorf("expected the presence warning: %#v", results)
	}

	// Template-only drift (identical content labels, older tag from a Piccolo
	// upgrade) must NOT be sold as a rebuild the user needs to make.
	m.Container.Image = desired.Tag[:len(desired.Tag)-12] + "111111111111"
	if err := m.Write(path); err != nil {
		t.Fatal(err)
	}
	results = Run(ctx, Options{Workspace: dir, Engine: fakeEngine{
		imageExists: true, labelsFound: true, imageLabels: desired.Labels(),
	}})
	if r := find(results, "runtime image is stale"); r != nil {
		t.Errorf("template-only drift reported as stale: %s", r.Message)
	}
	if r := find(results, "runtime image content matches this workspace"); r == nil || r.Status != OK {
		t.Fatalf("expected an OK content-match line for template drift: %#v", results)
	}
}

// A promotion that was interrupted leaves the previous gateway under the reserved
// name. That is the one abnormal state, and serve repairs it.
func TestDoctorReportsParkedPromotionTarget(t *testing.T) {
	dir := initWorkspace(t)
	results := Run(context.Background(), Options{Workspace: dir, Engine: fakeEngine{imageExists: true, parkedTarget: true}})
	r := find(results, "parked promotion target")
	if r == nil || r.Status != WARN {
		t.Fatalf("expected a parked-target warning: %#v", results)
	}
	if !strings.Contains(r.Message, "piccolo serve") {
		t.Errorf("parked target message should name the repair: %s", r.Message)
	}
}

// Accumulated, unreferenced runtime images must be reported with the command
// that reclaims them -- and never reclaimed by the check itself.
func TestDoctorReportsUnreferencedRuntimeImages(t *testing.T) {
	dir := initWorkspace(t)
	path := filepath.Join(dir, "piccolo.yaml")
	m, err := manifest.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	m.Container.Image = "piccolo/picoclaw-runtime:cur"
	m.Container.PreviousImage = "piccolo/picoclaw-runtime:prev"
	if err := m.Write(path); err != nil {
		t.Fatal(err)
	}
	gib := int64(1) << 30
	engine := fakeEngine{
		imageExists: true, labelsFound: true,
		images: []docker.ImageRecord{
			{ID: "sha256:cur", RepoTags: []string{"piccolo/picoclaw-runtime:cur"}, Size: gib, Containers: 1},
			{ID: "sha256:prev", RepoTags: []string{"piccolo/picoclaw-runtime:prev"}, Size: gib},
			{ID: "sha256:old1", RepoTags: []string{"piccolo/picoclaw-runtime:old1"}, Size: gib},
			{ID: "sha256:old2", RepoTags: []string{"piccolo/picoclaw-runtime:old2"}, Size: gib},
			// A stopped container keeps its image alive even though no tag in
			// piccolo.yaml points at it.
			{ID: "sha256:held", RepoTags: []string{"piccolo/picoclaw-runtime:held"}, Size: gib},
		},
		containers: map[string][]string{
			"piccolo/picoclaw-runtime:cur":  {"piccolo-gateway"},
			"piccolo/picoclaw-runtime:held": {"piccolo-gateway.prev"},
		},
	}
	results := Run(context.Background(), Options{Workspace: dir, Engine: engine, LookupEnv: func(string) (string, bool) { return "", false }})
	r := find(results, "runtime image retention")
	if r == nil || r.Status != WARN {
		t.Fatalf("expected a retention warning: %#v", results)
	}
	if !strings.Contains(r.Message, "2 unreferenced") {
		t.Errorf("count wrong (a stopped container must protect 'held'): %s", r.Message)
	}
	if !strings.Contains(r.Message, "piccolo image prune") {
		t.Errorf("warning must name the reclaim command: %s", r.Message)
	}
	if Failed(results) {
		t.Error("disk accumulation must warn, never fail")
	}
}
