// Package generate produces the workspace files for a new Piccolo-managed
// PicoClaw setup: piccolo.yaml, config.json, agent workspace files, and
// .gitignore. Generation is deterministic and create-only: existing files
// are never overwritten without an explicit per-file decision by the caller.
package generate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/kimus/piccolo/internal/manifest"
	"github.com/kimus/piccolo/internal/picoclaw"
	"github.com/kimus/piccolo/internal/runtimeimage"
)

// DefaultImage is the pinned PicoClaw gateway image used when the optional
// web console (dashboard) is disabled.
const DefaultImage = "docker.io/sipeed/picoclaw:v0.3.1"

// LauncherImage is the PicoClaw launcher image (web console + gateway), used
// when the optional web console is enabled.
const LauncherImage = "docker.io/sipeed/picoclaw:launcher"

// LauncherPort is the web console port published by the launcher image. Its
// presence in piccolo.yaml is the single source of truth for "web console
// enabled".
const LauncherPort = "127.0.0.1:18800:18800"

// DefaultContainerName is the deterministic gateway container name.
const DefaultContainerName = "piccolo-gateway"

// ModelInput describes the single model_list entry collected by the wizard.
type ModelInput struct {
	Alias    string // model_name, e.g. "gpt-5.4"
	Provider string // e.g. "openai"
	Model    string // provider-side model id, e.g. "gpt-5.4"
	APIKey   string // may be a placeholder; doctor warns
	APIBase  string // optional
}

// Inputs are the wizard answers that drive generation.
type Inputs struct {
	AssistantName string
	OwnerName     string
	Purpose       string   // personal | research | coding | operations | blank
	Specialists   []string // subset of research, planner, coder, ops
	// Dashboard enables the optional web console. The gateway always runs;
	// when true piccolo uses the launcher image and publishes :18800.
	Dashboard bool
	Model     ModelInput
	WebSearch bool
	ShellExec bool
}

// Validate checks inputs before generation.
func (in Inputs) Validate() error {
	if strings.TrimSpace(in.AssistantName) == "" {
		return fmt.Errorf("assistant name is required")
	}
	switch in.Purpose {
	case "personal", "research", "coding", "operations", "blank":
	default:
		return fmt.Errorf("invalid purpose %q", in.Purpose)
	}
	for _, s := range in.Specialists {
		switch s {
		case "research", "planner", "coder", "ops":
		default:
			return fmt.Errorf("invalid specialist agent %q", s)
		}
	}
	if strings.TrimSpace(in.Model.Alias) == "" || strings.TrimSpace(in.Model.Model) == "" {
		return fmt.Errorf("model alias and model id are required")
	}
	return nil
}

// Manifest builds the piccolo.yaml content (Piccolo-owned launch config plus
// non-secret setup metadata).
func (in Inputs) Manifest() *manifest.Manifest {
	m := &manifest.Manifest{
		Version: manifest.CurrentVersion,
		Container: manifest.Container{
			Image:   DefaultImage,
			Name:    DefaultContainerName,
			Network: "bridge",
		},
		Runtime: manifest.Runtime{
			Toolchain: manifest.Toolchain{
				Packages: append([]string(nil), runtimeimage.DefaultPackages...),
				Commands: append([]string(nil), runtimeimage.DefaultCommands...),
			},
		},
	}
	if in.Dashboard {
		m.Container.Image = LauncherImage
		m.Container.Ports = []string{LauncherPort}
		m.Container.Env = map[string]string{"PICOCLAW_GATEWAY_HOST": "0.0.0.0"}
	}
	return m
}

// PicoclawConfig builds the config.json content (schema v3).
func (in Inputs) PicoclawConfig() *picoclaw.Config {
	provider, model := normalizeModelRef(in.Model.Provider, in.Model.Model)

	main := picoclaw.Agent{
		ID:        "main",
		Default:   true,
		Workspace: picoclaw.AgentWorkspace("main"),
	}
	specialists := append([]string(nil), in.Specialists...)
	sort.Strings(specialists)
	if len(specialists) > 0 {
		main.Subagents = &picoclaw.Subagents{AllowAgents: specialists}
	}

	list := []picoclaw.Agent{main}
	for _, s := range specialists {
		list = append(list, picoclaw.Agent{
			ID:        s,
			Workspace: picoclaw.AgentWorkspace(s),
		})
	}

	return &picoclaw.Config{
		Version: picoclaw.SchemaVersion,
		Agents: picoclaw.Agents{
			Defaults: picoclaw.Defaults{
				Workspace:                 picoclaw.AgentWorkspace("main"),
				RestrictToWorkspace:       true,
				AllowReadOutsideWorkspace: true,
				ModelName:                 in.Model.Alias,
				ImageModel:                in.Model.Alias,
				ToolFeedback: picoclaw.ToolFeedback{
					Enabled:       true,
					MaxArgsLength: 300,
				},
			},
			List: list,
		},
		ModelList: []picoclaw.Model{{
			ModelName: in.Model.Alias,
			Provider:  provider,
			Model:     model,
			APIKeys:   []string{in.Model.APIKey},
			APIBase:   in.Model.APIBase,
		}},
		Tools: picoclaw.Tools{
			Web:       picoclaw.ToolToggle{Enabled: in.WebSearch},
			Exec:      picoclaw.ToolToggle{Enabled: in.ShellExec},
			Spawn:     picoclaw.ToolToggle{Enabled: true},
			Subagent:  picoclaw.ToolToggle{Enabled: true},
			LoadImage: picoclaw.ToolToggle{Enabled: true},
		},
	}
}

// normalizeModelRef returns the explicit (provider, model) pair PicoClaw
// prefers. Because an explicit provider means PicoClaw sends model UNCHANGED,
// a redundant "<provider>/" prefix is stripped from model so it is never
// double-prefixed. When provider is empty, model is returned as-is and
// PicoClaw infers the provider from its first "/" segment (unchanged legacy
// behavior).
func normalizeModelRef(provider, model string) (string, string) {
	provider = strings.TrimSpace(provider)
	model = strings.TrimSpace(model)
	if provider != "" {
		if rest, ok := strings.CutPrefix(model, provider+"/"); ok {
			model = strings.TrimSpace(rest)
		}
	}
	return provider, model
}

// File is one generated workspace file.
type File struct {
	// Rel is the workspace-relative path.
	Rel string
	// Content is the full file content.
	Content []byte
	// Mode is the file permission (0o644 default).
	Mode os.FileMode
}

// Plan is the deterministic set of files generation would write.
type Plan struct {
	Files []File
}

// BuildPlan renders every file for the workspace without touching disk.
func BuildPlan(in Inputs) (*Plan, error) {
	if err := in.Validate(); err != nil {
		return nil, err
	}
	p := &Plan{}

	m := in.Manifest()
	var yamlOut strings.Builder
	enc := yaml.NewEncoder(&yamlOut)
	enc.SetIndent(2)
	if err := enc.Encode(m); err != nil {
		return nil, fmt.Errorf("render piccolo.yaml: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("render piccolo.yaml: %w", err)
	}
	p.add("piccolo.yaml", []byte(manifestHeader+yamlOut.String()))

	cfg := in.PicoclawConfig()
	jsonBytes, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("render config.json: %w", err)
	}
	p.add("config.json", append(jsonBytes, '\n'))
	p.add(filepath.Join("skills", FileHandlingSkillName, "SKILL.md"), []byte(FileHandlingSkillMD()))

	agents := append([]string{"main"}, sortedSpecialists(in.Specialists)...)
	for _, id := range agents {
		role := "main"
		if id != "main" {
			role = id
		}
		for _, f := range agentFiles(role, in) {
			p.add(filepath.Join("agents", id, f.Rel), f.Content)
		}
	}
	return p, nil
}

const manifestHeader = "# Generated by piccolo init. Piccolo-owned configuration —\n" +
	"# PicoClaw runtime behavior (agents, models, tools, tokens) lives in config.json.\n"

func (p *Plan) add(rel string, content []byte) {
	p.Files = append(p.Files, File{Rel: rel, Content: content, Mode: 0o644})
}

func sortedSpecialists(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

// Existing returns the plan files that already exist in dir.
func (p *Plan) Existing(dir string) []string {
	var found []string
	for _, f := range p.Files {
		if _, err := os.Stat(filepath.Join(dir, f.Rel)); err == nil {
			found = append(found, f.Rel)
		}
	}
	return found
}

// Write writes the plan into dir, creating parent directories. Files that
// already exist are only written when listed in the overwrite set; all other
// existing files are skipped. It returns the files written and skipped.
func (p *Plan) Write(dir string, overwrite map[string]bool) (written, skipped []string, err error) {
	for _, f := range p.Files {
		path := filepath.Join(dir, f.Rel)
		if _, statErr := os.Stat(path); statErr == nil && !overwrite[f.Rel] {
			skipped = append(skipped, f.Rel)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return written, skipped, err
		}
		mode := f.Mode
		if mode == 0 {
			mode = 0o644
		}
		if err := os.WriteFile(path, f.Content, mode); err != nil {
			return written, skipped, err
		}
		written = append(written, f.Rel)
	}
	return written, skipped, nil
}

// EnsureGitignore creates or appends to dir/.gitignore so sensitive and
// runtime files (config.json holds API keys; .picoclaw.pid holds the gateway
// reload token) are never committed. It reports whether a change was made.
func EnsureGitignore(dir string) (bool, error) {
	path := filepath.Join(dir, ".gitignore")
	entries := []string{"config.json", ".picoclaw.pid"}
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return false, err
	}
	existing := map[string]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		existing[strings.TrimSpace(line)] = true
	}
	changed := false
	out := string(data)
	for _, entry := range entries {
		if existing[entry] {
			continue
		}
		if out != "" && !strings.HasSuffix(out, "\n") {
			out += "\n"
		}
		out += entry + "\n"
		changed = true
	}
	if !changed {
		return false, nil
	}
	if err := os.WriteFile(path, []byte(out), 0o644); err != nil {
		return false, err
	}
	return true, nil
}
