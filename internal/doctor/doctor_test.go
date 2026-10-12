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
		Mode:          generate.ModeTerminal,
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
