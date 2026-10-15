package generate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kimus/piccolo/internal/picoclaw"
	"github.com/kimus/piccolo/internal/runtimeimage"
)

func testInputs() Inputs {
	return Inputs{
		AssistantName: "Piccolo",
		OwnerName:     "Kimus",
		Purpose:       "personal",
		Specialists:   []string{"planner", "research"},
		Dashboard:     false,
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
		"picoclaw/config.json",
		"picoclaw/skills/file-handling/SKILL.md",
		"picoclaw/agents/main/AGENT.md",
		"picoclaw/agents/main/SOUL.md",
		"picoclaw/agents/main/USER.md",
		"picoclaw/agents/main/memory/MEMORY.md",
		"picoclaw/agents/planner/AGENT.md",
		"picoclaw/agents/planner/SOUL.md",
		"picoclaw/agents/research/AGENT.md",
		"picoclaw/agents/research/SOUL.md",
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
	if len(cfg.ModelList) != 1 {
		t.Fatalf("model_list = %+v", cfg.ModelList)
	}
	if cfg.ModelList[0].Provider != "openai" || cfg.ModelList[0].Model != "gpt-5.4" {
		t.Errorf("model entry = %+v, want explicit provider openai + bare model gpt-5.4", cfg.ModelList[0])
	}
	if cfg.Agents.Defaults.ImageModel != "gpt-5.4" {
		t.Errorf("image_model = %q, want selected model alias", cfg.Agents.Defaults.ImageModel)
	}
	if !cfg.Tools.Web.Enabled || cfg.Tools.Exec.Enabled {
		t.Errorf("tools = %+v", cfg.Tools)
	}
	if !cfg.Tools.Spawn.Enabled || !cfg.Tools.Subagent.Enabled || !cfg.Tools.LoadImage.Enabled {
		t.Errorf("spawn/subagent/load_image must be enabled: %+v", cfg.Tools)
	}
	if cfg.Agents.Defaults.RestrictToWorkspace {
		t.Error("restrict_to_workspace must be generated OFF: with exec enabled PicoClaw blocks any command naming a path outside the agent workspace")
	}
	if !cfg.Agents.Defaults.AllowReadOutsideWorkspace {
		t.Error("allow_read_outside_workspace must be enabled for media handling")
	}
	if !cfg.Agents.Defaults.ToolFeedback.Enabled || cfg.Agents.Defaults.ToolFeedback.MaxArgsLength != 300 {
		t.Errorf("tool_feedback = %+v", cfg.Agents.Defaults.ToolFeedback)
	}

	// Round-trips as valid JSON.
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(data), "allow_remote") || strings.Contains(string(data), "allow_read_paths") || strings.Contains(string(data), "custom_allow_patterns") {
		t.Fatalf("generated defaults should not emit allow_remote unless explicitly set: %s", data)
	}
	// PicoClaw defaults this flag to TRUE when the key is absent
	// (pkg/config/defaults.go:29), so the OFF posture only exists if the literal
	// false is written out.
	if !strings.Contains(string(data), `"restrict_to_workspace": false`) {
		t.Fatalf("generated config.json must state \"restrict_to_workspace\": false explicitly: %s", data)
	}
	var back picoclaw.Config
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
}

func TestManifestDashboardOffVsOn(t *testing.T) {
	in := testInputs()
	in.Dashboard = false
	m := in.Manifest()
	if m.Container.Image != DefaultImage || len(m.Container.Ports) != 0 || m.Container.Env != nil {
		t.Errorf("dashboard-off manifest = %+v", m.Container)
	}
	for _, want := range []string{"libreoffice", "imagemagick", "poppler-utils"} {
		if !containsString(m.Runtime.Toolchain.Packages, want) {
			t.Errorf("default packages missing %q: %v", want, m.Runtime.Toolchain.Packages)
		}
	}
	for _, want := range []string{"pdfinfo", "pdftoppm", "pdftocairo", "soffice", "magick", "convert", "identify"} {
		if !containsString(m.Runtime.Toolchain.Commands, want) {
			t.Errorf("default commands missing %q: %v", want, m.Runtime.Toolchain.Commands)
		}
	}
	if containsString(m.Runtime.Toolchain.Packages, "opencv") || containsString(m.Runtime.Toolchain.Commands, "cv2") {
		t.Errorf("OpenCV must not be a default toolchain entry: %+v", m.Runtime.Toolchain)
	}
	if len(m.Runtime.Toolchain.Packages) != len(runtimeimage.DefaultPackages) || len(m.Runtime.Toolchain.Commands) != len(runtimeimage.DefaultCommands) {
		t.Errorf("manifest defaults drifted from runtimeimage defaults")
	}
	in.Dashboard = true
	m = in.Manifest()
	if m.Container.Image != LauncherImage {
		t.Errorf("dashboard-on image = %q", m.Container.Image)
	}
	if len(m.Container.Ports) != 1 || m.Container.Ports[0] != LauncherPort {
		t.Errorf("dashboard-on ports = %v", m.Container.Ports)
	}
	if m.Container.Env["PICOCLAW_GATEWAY_HOST"] != "0.0.0.0" {
		t.Errorf("dashboard-on env = %v", m.Container.Env)
	}
}

// TestWebSearchIndependentOfDashboard asserts the two axes are unrelated:
// web search on + dashboard off yields tools.web.enabled true and no :18800
// binding (and the lean gateway image).
func TestWebSearchIndependentOfDashboard(t *testing.T) {
	in := testInputs()
	in.WebSearch = true
	in.Dashboard = false

	cfg := in.PicoclawConfig()
	if !cfg.Tools.Web.Enabled {
		t.Errorf("tools.web.enabled = false, want true (web search on)")
	}
	m := in.Manifest()
	if len(m.Container.Ports) != 0 {
		t.Errorf("dashboard-off must not publish ports, got %v", m.Container.Ports)
	}
	if m.Container.Image != DefaultImage {
		t.Errorf("dashboard-off image = %q, want %q", m.Container.Image, DefaultImage)
	}

	// And the reverse: dashboard on does not force web search on.
	in.WebSearch = false
	in.Dashboard = true
	cfg = in.PicoclawConfig()
	if cfg.Tools.Web.Enabled {
		t.Errorf("tools.web.enabled = true, want false (web search off)")
	}
	m = in.Manifest()
	if len(m.Container.Ports) != 1 || m.Container.Ports[0] != LauncherPort {
		t.Errorf("dashboard-on ports = %v, want [%s]", m.Container.Ports, LauncherPort)
	}
}

func TestNormalizeModelRef(t *testing.T) {
	cases := []struct {
		provider, model string
		wantP, wantM    string
	}{
		{"openrouter", "qwen/qwen3.7-flash", "openrouter", "qwen/qwen3.7-flash"},
		// Redundant prefix is stripped so PicoClaw never gets a double prefix.
		{"openrouter", "openrouter/qwen/qwen3.7-flash", "openrouter", "qwen/qwen3.7-flash"},
		{"openai", "openai/gpt-5.4", "openai", "gpt-5.4"},
		// No provider: model left as-is (PicoClaw infers from first segment).
		{"", "openai/gpt-5.4", "", "openai/gpt-5.4"},
		{"", "gpt-5.4", "", "gpt-5.4"},
		// Whitespace tolerated.
		{" openrouter ", " qwen/qwen3.7-flash ", "openrouter", "qwen/qwen3.7-flash"},
	}
	for _, c := range cases {
		gotP, gotM := normalizeModelRef(c.provider, c.model)
		if gotP != c.wantP || gotM != c.wantM {
			t.Errorf("normalizeModelRef(%q, %q) = (%q, %q), want (%q, %q)",
				c.provider, c.model, gotP, gotM, c.wantP, c.wantM)
		}
	}
}

func TestAgentMDFrontmatter(t *testing.T) {
	p, err := BuildPlan(testInputs())
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	var mainMD, researchMD string
	for _, f := range p.Files {
		switch f.Rel {
		case "picoclaw/agents/main/AGENT.md":
			mainMD = string(f.Content)
		case "picoclaw/agents/research/AGENT.md":
			researchMD = string(f.Content)
		}
	}
	if !strings.HasPrefix(mainMD, "---\n") {
		t.Fatal("AGENT.md must start with frontmatter")
	}
	for _, want := range []string{"name: Piccolo", "skills: [file-handling]"} {
		if !strings.Contains(mainMD, want) {
			t.Errorf("main AGENT.md missing %q", want)
		}
	}
	// The coordinator declares NO tool allowlist. PicoClaw reads a declared
	// `tools:` key as a strict exact-name list, and MCP tools are named
	// mcp_<server>_<tool> only at runtime, so any list Piccolo writes here silently
	// revokes every tool an installed integration provides.
	if hasFrontmatterKey(mainMD, "tools") {
		t.Errorf("main AGENT.md must not declare a tools: allowlist:\n%s", mainMD)
	}
	// Specialists keep theirs — the reduced set is the point of a specialist.
	if !hasFrontmatterKey(researchMD, "tools") {
		t.Fatalf("research AGENT.md must declare its allowlist:\n%s", researchMD)
	}
	for _, want := range []string{"web_search", "load_image"} {
		if !strings.Contains(researchMD, want) {
			t.Errorf("research AGENT.md missing %q", want)
		}
	}
}

func hasFrontmatterKey(md, key string) bool {
	rest := strings.TrimPrefix(md, "---\n")
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return false
	}
	for _, line := range strings.Split(rest[:end], "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), key+":") {
			return true
		}
	}
	return false
}

func TestFileHandlingSkillGenerated(t *testing.T) {
	p, err := BuildPlan(testInputs())
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	var skillMD string
	for _, f := range p.Files {
		if f.Rel == "picoclaw/skills/file-handling/SKILL.md" {
			skillMD = string(f.Content)
		}
	}
	for _, want := range []string{
		"name: file-handling",
		"deterministic tools for exact payloads",
		"available directly on PATH",
		"the runtime image was built without it",
		"zbarimg --quiet --raw <image-path>",
		"Do not use `apt-get`",
		"Do not import `cv2`",
		"/tmp/picoclaw_media",
		"workspace-relative output directories",
		"pdfinfo <file.pdf>",
		"pdftoppm",
		"pdftocairo",
		"soffice --headless --convert-to",
		"magick",
		"tesseract <image-path> stdout",
		"pdftotext <file.pdf> -",
		"pandoc <file> -t plain",
		"file <path>",
	} {
		if !strings.Contains(skillMD, want) {
			t.Errorf("file-handling skill missing guidance %q", want)
		}
	}
	if strings.Contains(skillMD, "cd /opt/piccolo/devbox") || strings.Contains(skillMD, "devbox run --") {
		t.Error("skill must not tell agents to cd to /opt or wrap tools in devbox run")
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
	target := filepath.Join(dir, "picoclaw", "config.json")
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
	written, skipped, err = p.Write(dir, map[string]bool{"picoclaw/config.json": true})
	if err != nil {
		t.Fatalf("third Write: %v", err)
	}
	if len(written) != 1 || written[0] != "picoclaw/config.json" || len(skipped) != len(p.Files)-1 {
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
	if strings.TrimSpace(string(data)) != "picoclaw/config.json\npicoclaw/.security.yml\npicoclaw/.picoclaw.pid\npicoclaw/launcher-auth.db\npicoclaw/logs/\nintegrations/*/auth/\nruntime/\n.DS_Store" {
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
	if string(data) != "*.log\npicoclaw/config.json\npicoclaw/.security.yml\npicoclaw/.picoclaw.pid\npicoclaw/launcher-auth.db\npicoclaw/logs/\nintegrations/*/auth/\nruntime/\n.DS_Store\n" {
		t.Fatalf("appended gitignore = %q", data)
	}

	// Partially covered file: only the missing entry is appended.
	os.WriteFile(filepath.Join(dir2, ".gitignore"), []byte("picoclaw/config.json\n"), 0o644)
	changed, err = EnsureGitignore(dir2)
	if err != nil || !changed {
		t.Fatalf("partial append: changed=%v err=%v", changed, err)
	}
	data, _ = os.ReadFile(filepath.Join(dir2, ".gitignore"))
	if string(data) != "picoclaw/config.json\npicoclaw/.security.yml\npicoclaw/.picoclaw.pid\npicoclaw/launcher-auth.db\npicoclaw/logs/\nintegrations/*/auth/\nruntime/\n.DS_Store\n" {
		t.Fatalf("partial appended gitignore = %q", data)
	}
}

// A workspace created before .DS_Store was a required entry keeps the hole, and
// re-running init is the documented remedy only if the missing line is appended
// without touching anything else. Both halves are asserted: an existing rule is
// left alone, and a user's own entry survives.
func TestEnsureGitignoreHealsOSMetadataWithoutRewriting(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("picoclaw/config.json\npicoclaw/logs/\nintegrations/*/auth/\nruntime/\n/my-own-rule\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	changed, err := EnsureGitignore(dir)
	if err != nil || !changed {
		t.Fatalf("heal must report a change: changed=%v err=%v", changed, err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, ".gitignore"))
	for _, want := range []string{".DS_Store", "/my-own-rule"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("missing %q in %q", want, data)
		}
	}
	if strings.Count(string(data), ".DS_Store") != 1 {
		t.Errorf("heal is not idempotent: %q", data)
	}
	if _, err := EnsureGitignore(dir); err != nil {
		t.Fatal(err)
	}
	again, _ := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if string(again) != string(data) {
		t.Errorf("second heal changed the file:\n%q\n%q", data, again)
	}
}

func TestMissingGitignoreEntries(t *testing.T) {
	dir := t.TempDir()
	missing, err := MissingGitignoreEntries(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) == 0 {
		t.Fatal("an absent .gitignore must report missing entries, not 'all good'")
	}

	if _, err := EnsureGitignore(dir); err != nil {
		t.Fatal(err)
	}
	if missing, err := MissingGitignoreEntries(dir); err != nil || len(missing) != 0 {
		t.Fatalf("fully generated workspace should report nothing missing: %v (%v)", missing, err)
	}

	// Drop the OS rule and confirm it is the one named.
	data, _ := os.ReadFile(filepath.Join(dir, ".gitignore"))
	kept := []string{}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) != GitignoreOSMetadata {
			kept = append(kept, line)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(strings.Join(kept, "\n")), 0o644); err != nil {
		t.Fatal(err)
	}
	missing, err = MissingGitignoreEntries(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) != 1 || missing[0] != GitignoreOSMetadata {
		t.Fatalf("missing = %v, want just %q", missing, GitignoreOSMetadata)
	}

	// A commented line is not a rule.
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("# .DS_Store\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if missing, err := MissingGitignoreEntries(dir); err != nil || len(missing) == 0 {
		t.Fatalf("a comment must not count as an ignore rule: %v (%v)", missing, err)
	}
}

func TestValidateInputs(t *testing.T) {
	in := testInputs()
	in.Purpose = "bogus"
	if err := in.Validate(); err == nil {
		t.Error("expected purpose error")
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

func containsString(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}
