package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kimus/piccolo/internal/configdoc"
	"github.com/kimus/piccolo/internal/setup"
)

// A model rename re-points config.json and reports AGENT.md pins instead of
// rewriting them — so reading that pin has to survive the shapes people
// actually write, and must not fire on a body line that merely mentions a model.
func TestAgentFrontmatterModel(t *testing.T) {
	cases := []struct {
		name string
		want string
	}{
		{"---\nname: Main\nmodel: fast\ntools: [read_file]\n---\n\nBody\n", "fast"},
		{"---\nname: Main\nmodel: \"fast\"\n---\n", "fast"},
		{"---\nname: Main\nmodel: 'fast'\n---\n", "fast"},
		{"---\nname: Main\nmodel:    \n---\n", ""},
		{"---\nname: Main\n---\nmodel: fast\n", ""},
		{"name: Main\nmodel: fast\n", ""},
		{"---\nname: Main\ndescription: model: not-a-key\n---\n", ""},
	}
	for _, tc := range cases {
		if got := agentFrontmatterModel(tc.name); got != tc.want {
			t.Errorf("agentFrontmatterModel(%q) = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestUpdateAgentSkillsFrontmatter(t *testing.T) {
	content := "---\nname: Main\ntools: [read_file]\n---\n\nBody\n"
	updated, changed := updateAgentSkillsFrontmatter(content, []string{"file-handling"}, true)
	if !changed {
		t.Fatal("expected skills frontmatter change")
	}
	if !strings.Contains(updated, "skills: [file-handling]") {
		t.Fatalf("skills not added: %s", updated)
	}
	updatedAgain, changed := updateAgentSkillsFrontmatter(updated, []string{"file-handling"}, true)
	if changed || updatedAgain != updated {
		t.Fatal("adding existing skill should be a no-op")
	}
}

// The name records the old expectation. Setup must write NO workspace package
// config: the toolchain is image content, and a workspace file here would be a
// second, divergent install path.
func TestSyncEnabledToolWorkspaceChangesWritesNoWorkspacePackageConfig(t *testing.T) {
	dir := t.TempDir()
	agentDir := filepath.Join(dir, "picoclaw", "agents", "main")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	agentPath := filepath.Join(agentDir, "AGENT.md")
	if err := os.WriteFile(agentPath, []byte("---\nname: Main\ntools: [read_file]\n---\n\nBody\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	doc, err := configdoc.Parse([]byte(`{
  "version": 3,
  "agents": {
    "defaults": { "workspace": "~/.picoclaw/agents/main" },
    "list": [ { "id": "main", "default": true } ]
  },
  "tools": {
    "exec": { "enabled": true },
    "load_image": { "enabled": true }
  }
}`))
	if err != nil {
		t.Fatal(err)
	}
	st := &setupState{dir: dir, doc: doc}
	changes, err := st.syncEnabledToolWorkspaceChanges()
	if err != nil {
		t.Fatal(err)
	}
	agents, ok := doc.GetObject("agents")
	if !ok {
		t.Fatal("agents object missing")
	}
	defaults, ok := agents.GetObject("defaults")
	if !ok {
		t.Fatal("agents.defaults object missing")
	}
	if v, _ := defaults.GetBool("allow_read_outside_workspace"); !v {
		t.Fatal("allow_read_outside_workspace not set")
	}
	// Syncing media read convenience must NOT reach for the exec path guard. A
	// user who turned it on deliberately owns that decision, and silently flipping
	// a security setting in a user-owned config.json is forbidden; the answer for an
	// older workspace is the doctor warning plus 'piccolo setup', not a free write.
	if _, ok := defaults.Get("restrict_to_workspace"); ok {
		t.Fatal("tool sync must not write agents.defaults.restrict_to_workspace")
	}
	feedback, ok := defaults.GetObject("tool_feedback")
	if !ok {
		t.Fatal("tool_feedback object missing")
	}
	if enabled, _ := feedback.GetBool("enabled"); !enabled {
		t.Fatal("tool_feedback.enabled not set")
	}
	maxRaw, ok := feedback.Get("max_args_length")
	if !ok || fmt.Sprint(maxRaw) != "300" {
		t.Fatalf("tool_feedback.max_args_length = %#v", maxRaw)
	}
	foundSkill := false
	foundAgentMD := false
	// The skill lives under picoclaw/ — PicoClaw's home. It once resolved to the
	// workspace root, so "sync" wrote a copy nothing loads, left the real file
	// stale, and still reported success.
	wantSkill := filepath.Join(dir, "picoclaw", "skills", "file-handling", "SKILL.md")
	for _, ch := range changes {
		if ch.path == wantSkill {
			foundSkill = true
		}
		if ch.path == filepath.Join(dir, "skills", "file-handling", "SKILL.md") {
			t.Fatalf("sync must not write outside PicoClaw's home: %s", ch.path)
		}
		if ch.path == agentPath {
			foundAgentMD = true
		}
		if filepath.Base(ch.path) == "devbox.json" {
			t.Fatalf("setup must not write a workspace devbox.json (tools are global): %+v", ch.path)
		}
	}
	if !foundSkill {
		t.Fatalf("missing file-handling skill change at %s", wantSkill)
	}
	if !foundAgentMD {
		t.Fatalf("missing AGENT.md allowlist change: %+v", changes)
	}
}

// A declared `tools:` line is a strict, exact-name allowlist in PicoClaw
// (pkg/agent/tool_allowlist.go:143-164 → pkg/tools/registry.go:171-184), and MCP
// tools are named mcp_<server>_<tool> only at runtime, so no list Piccolo writes
// can contain them. Sync must therefore never add one to the coordinator — doing so
// would revoke every tool an installed integration provides — while specialists keep
// their reduced lists in sync.
func TestSyncDoesNotDeclareAllowlistOnCoordinator(t *testing.T) {
	dir := t.TempDir()
	coordDir := filepath.Join(dir, "picoclaw", "agents", "main")
	resDir := filepath.Join(dir, "picoclaw", "agents", "research")
	for _, d := range []string{coordDir, resDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	coordPath := filepath.Join(coordDir, "AGENT.md")
	resPath := filepath.Join(resDir, "AGENT.md")
	if err := os.WriteFile(coordPath, []byte("---\nname: Piccolo\ndescription: Coordinator.\nskills: [file-handling]\n---\n\nBody\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(resPath, []byte("---\nname: Research\ndescription: Research.\ntools: [read_file]\nskills: [file-handling]\n---\n\nBody\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	doc, err := configdoc.Parse([]byte(`{
	  "version": 3,
	  "agents": {
	    "defaults": { "workspace": "~/.picoclaw/agents/main" },
	    "list": [ { "id": "main", "default": true, "workspace": "~/.picoclaw/agents/main" }, { "id": "research", "workspace": "~/.picoclaw/agents/research" } ]
	  },
	  "tools": { "exec": { "enabled": true }, "load_image": { "enabled": true } }
	}`))
	if err != nil {
		t.Fatal(err)
	}
	st := &setupState{dir: dir, doc: doc}
	changes, err := st.syncEnabledToolWorkspaceChanges()
	if err != nil {
		t.Fatal(err)
	}

	for _, ch := range changes {
		if ch.path == coordPath {
			t.Fatalf("sync must not touch the coordinator's frontmatter:\n%s", ch.content)
		}
	}
	var research *agentFileChange
	for i, ch := range changes {
		if ch.path == resPath {
			research = &changes[i]
		}
	}
	if research == nil {
		var got []string
		for _, ch := range changes {
			got = append(got, filepath.Base(filepath.Dir(ch.path)))
		}
		t.Fatalf("specialist allowlist was not synced (changed: %v)", got)
	}
	if !strings.Contains(string(research.content), "tools: [read_file, exec, load_image]") {
		t.Fatalf("specialist must gain the enabled tools:\n%s", research.content)
	}
}

func TestToolStateSummaryIncludesRemoteExec(t *testing.T) {
	on := true
	off := false
	s := toolStateSummary(setup.ToolsState{
		Web:             &on,
		Exec:            &on,
		ExecAllowRemote: &off,
		Spawn:           &on,
		Subagent:        &on,
		LoadImage:       &on,
	})
	if !strings.Contains(s, "remote-exec:off") {
		t.Fatalf("summary missing remote exec state: %s", s)
	}
}
