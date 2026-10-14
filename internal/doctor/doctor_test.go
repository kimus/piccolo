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
	agentPath := filepath.Join(dir, "picoclaw", "agents", "main", "AGENT.md")
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
