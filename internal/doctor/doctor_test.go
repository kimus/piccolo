package doctor

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kimus/piccolo/internal/generate"
	"github.com/kimus/piccolo/internal/manifest"
)

// fakeEngine is a daemon-free doctor.Engine for tests.
type fakeEngine struct {
	pingErr     error
	imageExists bool
	container   bool
}

func (f fakeEngine) Ping(context.Context) error { return f.pingErr }
func (f fakeEngine) ImageExists(context.Context, string) (bool, error) {
	return f.imageExists, nil
}
func (f fakeEngine) ContainerExists(context.Context, string) (bool, error) {
	return f.container, nil
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
	os.Remove(filepath.Join(dir, "agents", "research", "AGENT.md"))
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
	results := Run(context.Background(), Options{
		Workspace: dir,
		Engine:    fakeEngine{imageExists: true, container: true},
	})
	if Failed(results) {
		t.Fatalf("stale container must not fail: %v", results)
	}
	r := find(results, "stale container")
	if r == nil || r.Status != WARN {
		t.Fatalf("expected stale WARN, got %v", results)
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

	dir = initWorkspace(t)
	agentPath := filepath.Join(dir, "agents", "main", "AGENT.md")
	data, err := os.ReadFile(agentPath)
	if err != nil {
		t.Fatal(err)
	}
	content := strings.Replace(string(data), ", load_image", "", 1)
	if err := os.WriteFile(agentPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	results = Run(context.Background(), Options{Workspace: dir, Engine: fakeEngine{imageExists: true}})
	if r := find(results, `agent "main" AGENT.md does not allow load_image`); r == nil || r.Status != WARN {
		t.Fatalf("expected agent load_image WARN, got %v", results)
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
	if find(results, `model_name "vision-missing" is referenced but missing`) == nil {
		t.Fatalf("expected missing image model FAIL, got %v", results)
	}
}

func TestTranslateWorkspacePath(t *testing.T) {
	for _, in := range []string{"~/.picoclaw/agents/main", "/root/.picoclaw/agents/main"} {
		got, ok := translateWorkspacePath("/ws", in)
		if !ok || got != filepath.Join("/ws", "agents", "main") {
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
	path := filepath.Join(dir, "config.json")
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
