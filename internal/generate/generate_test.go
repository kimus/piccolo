package generate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kimus/piccolo/internal/picoclaw"
)

func testInputs() Inputs {
	return Inputs{
		AssistantName: "Piccolo",
		OwnerName:     "Kimus",
		Purpose:       "personal",
		Specialists:   []string{"planner", "research"},
		Mode:          ModeTerminal,
		Model: ModelInput{
			Alias:    "gpt-5.4",
			Provider: "openai",
			Model:    "gpt-5.4",
			APIKey:   "sk-test",
		},
		WebSearch: true,
		ShellExec: false,
	}
}

func TestBuildPlanDeterministic(t *testing.T) {
	in := testInputs()
	p1, err := BuildPlan(in)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	p2, err := BuildPlan(in)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if len(p1.Files) != len(p2.Files) {
		t.Fatalf("plan sizes differ: %d vs %d", len(p1.Files), len(p2.Files))
	}
	for i := range p1.Files {
		if p1.Files[i].Rel != p2.Files[i].Rel {
			t.Fatalf("file order differs at %d: %s vs %s", i, p1.Files[i].Rel, p2.Files[i].Rel)
		}
		if string(p1.Files[i].Content) != string(p2.Files[i].Content) {
			t.Fatalf("content differs for %s", p1.Files[i].Rel)
		}
	}
}

func TestBuildPlanFiles(t *testing.T) {
	p, err := BuildPlan(testInputs())
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	want := []string{
		"piccolo.yaml",
		"config.json",
		"agents/main/AGENT.md",
		"agents/main/SOUL.md",
		"agents/main/USER.md",
		"agents/main/memory/MEMORY.md",
		"agents/planner/AGENT.md",
		"agents/planner/SOUL.md",
		"agents/research/AGENT.md",
		"agents/research/SOUL.md",
	}
	rels := map[string]bool{}
	for _, f := range p.Files {
		rels[f.Rel] = true
	}
	for _, w := range want {
		if !rels[w] {
			t.Errorf("plan missing %s", w)
		}
	}
	if len(p.Files) != len(want) {
		t.Errorf("plan has %d files, want %d: %v", len(p.Files), len(want), rels)
	}
}

func TestGeneratedPicoclawConfig(t *testing.T) {
	cfg := testInputs().PicoclawConfig()
	if cfg.Version != picoclaw.SchemaVersion {
		t.Errorf("version = %d", cfg.Version)
	}
	if len(cfg.Agents.List) != 3 {
		t.Fatalf("agents.list = %d entries", len(cfg.Agents.List))
	}
	main := cfg.Agents.List[0]
	if main.ID != "main" || !main.Default {
		t.Errorf("first agent = %+v, want default main", main)
	}
	if main.Workspace != "~/.picoclaw/agents/main" {
		t.Errorf("workspace = %q, want container-side path", main.Workspace)
	}
	if main.Subagents == nil || len(main.Subagents.AllowAgents) != 2 {
		t.Errorf("subagents = %+v", main.Subagents)
	}
	if len(cfg.ModelList) != 1 || cfg.ModelList[0].Model != "openai/gpt-5.4" {
		t.Errorf("model_list = %+v", cfg.ModelList)
	}
	if !cfg.Tools.Web.Enabled || cfg.Tools.Exec.Enabled {
		t.Errorf("tools = %+v", cfg.Tools)
	}
	if !cfg.Tools.Spawn.Enabled || !cfg.Tools.Subagent.Enabled {
		t.Errorf("spawn/subagent must be enabled: %+v", cfg.Tools)
	}

	// Round-trips as valid JSON.
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back picoclaw.Config
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
}

func TestManifestTerminalVsWeb(t *testing.T) {
	in := testInputs()
	m := in.Manifest()
	if m.Container.Image != DefaultImage || len(m.Container.Ports) != 0 || m.Container.Env != nil {
		t.Errorf("terminal manifest = %+v", m.Container)
	}
	in.Mode = ModeWeb
	m = in.Manifest()
	if m.Container.Image != LauncherImage {
		t.Errorf("web image = %q", m.Container.Image)
	}
	if len(m.Container.Ports) != 1 || m.Container.Ports[0] != LauncherPort {
		t.Errorf("web ports = %v", m.Container.Ports)
	}
	if m.Container.Env["PICOCLAW_GATEWAY_HOST"] != "0.0.0.0" {
		t.Errorf("web env = %v", m.Container.Env)
	}
}

func TestAgentMDFrontmatter(t *testing.T) {
	p, err := BuildPlan(testInputs())
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	var mainMD string
	for _, f := range p.Files {
		if f.Rel == "agents/main/AGENT.md" {
			mainMD = string(f.Content)
		}
	}
	if !strings.HasPrefix(mainMD, "---\n") {
		t.Fatal("AGENT.md must start with frontmatter")
	}
	for _, want := range []string{"name: Piccolo", "web_search", "spawn", "subagent"} {
		if !strings.Contains(mainMD, want) {
			t.Errorf("main AGENT.md missing %q", want)
		}
	}
}

func TestCoderToolsRespectShellExec(t *testing.T) {
	in := testInputs()
	in.Specialists = []string{"coder"}
	in.ShellExec = false
	if tools := toolsFor("coder", in); contains(tools, "exec") {
		t.Error("exec must be absent when shell disabled")
	}
	in.ShellExec = true
	if tools := toolsFor("coder", in); !contains(tools, "exec") {
		t.Error("exec must be present when shell enabled")
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func TestWriteIsCreateOnly(t *testing.T) {
	dir := t.TempDir()
	p, err := BuildPlan(testInputs())
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}

	written, skipped, err := p.Write(dir, nil)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if len(written) != len(p.Files) || len(skipped) != 0 {
		t.Fatalf("first write: written=%d skipped=%d", len(written), len(skipped))
	}

	// Modify one file; a second write without overwrite must keep it.
	target := filepath.Join(dir, "config.json")
	if err := os.WriteFile(target, []byte(`{"user":"edited"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	written, skipped, err = p.Write(dir, nil)
	if err != nil {
		t.Fatalf("second Write: %v", err)
	}
	if len(written) != 0 || len(skipped) != len(p.Files) {
		t.Fatalf("second write: written=%v skipped=%v", written, skipped)
	}
	data, _ := os.ReadFile(target)
	if string(data) != `{"user":"edited"}` {
		t.Fatal("user edits were overwritten")
	}

	// Explicit overwrite replaces it.
	written, skipped, err = p.Write(dir, map[string]bool{"config.json": true})
	if err != nil {
		t.Fatalf("third Write: %v", err)
	}
	if len(written) != 1 || written[0] != "config.json" || len(skipped) != len(p.Files)-1 {
		t.Fatalf("third write: written=%v skipped=%v", written, skipped)
	}
}

func TestEnsureGitignore(t *testing.T) {
	dir := t.TempDir()

	changed, err := EnsureGitignore(dir)
	if err != nil || !changed {
		t.Fatalf("create: changed=%v err=%v", changed, err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if strings.TrimSpace(string(data)) != "config.json" {
		t.Fatalf("gitignore = %q", data)
	}

	changed, err = EnsureGitignore(dir)
	if err != nil || changed {
		t.Fatalf("idempotent: changed=%v err=%v", changed, err)
	}

	// Appends to an existing file.
	dir2 := t.TempDir()
	os.WriteFile(filepath.Join(dir2, ".gitignore"), []byte("*.log"), 0o644)
	changed, err = EnsureGitignore(dir2)
	if err != nil || !changed {
		t.Fatalf("append: changed=%v err=%v", changed, err)
	}
	data, _ = os.ReadFile(filepath.Join(dir2, ".gitignore"))
	if string(data) != "*.log\nconfig.json\n" {
		t.Fatalf("appended gitignore = %q", data)
	}
}

func TestValidateInputs(t *testing.T) {
	in := testInputs()
	in.Purpose = "bogus"
	if err := in.Validate(); err == nil {
		t.Error("expected purpose error")
	}
	in = testInputs()
	in.Mode = ""
	if err := in.Validate(); err == nil {
		t.Error("expected mode error")
	}
	in = testInputs()
	in.Specialists = []string{"lawyer"}
	if err := in.Validate(); err == nil {
		t.Error("expected specialist error")
	}
	in = testInputs()
	in.AssistantName = ""
	if err := in.Validate(); err == nil {
		t.Error("expected name error")
	}
}
