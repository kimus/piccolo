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

func TestSyncEnabledToolWorkspaceChangesAddsDevboxJSON(t *testing.T) {
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
	for _, ch := range changes {
		if ch.path == filepath.Join(dir, "skills", "file-handling", "SKILL.md") {
			foundSkill = true
		}
		if ch.path == agentPath {
			foundAgentMD = true
		}
		if filepath.Base(ch.path) == "devbox.json" {
			t.Fatalf("setup must not write a workspace devbox.json (tools are global): %+v", ch.path)
		}
	}
	if !foundSkill {
		t.Fatalf("missing file-handling skill change: %+v", changes)
	}
	if !foundAgentMD {
		t.Fatalf("missing AGENT.md allowlist change: %+v", changes)
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
