// Package doctor validates a Piccolo workspace: Docker reachability, the
// piccolo.yaml manifest, the generated PicoClaw config.json, and agent
// workspace files. All Docker interaction goes through a narrow Engine
// interface so checks are unit-testable without a daemon.
package doctor

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/kimus/piccolo/internal/manifest"
	"github.com/kimus/piccolo/internal/picoclaw"
)

// Engine is the narrow Docker surface doctor needs.
type Engine interface {
	Ping(ctx context.Context) error
	ImageExists(ctx context.Context, image string) (bool, error)
	ContainerExists(ctx context.Context, name string) (bool, error)
}

// Status is the severity of one check result.
type Status int

const (
	// OK is a passing check.
	OK Status = iota
	// WARN is a non-fatal problem.
	WARN
	// FAIL is a fatal problem (non-zero exit).
	FAIL
)

func (s Status) String() string {
	switch s {
	case OK:
		return "[OK]  "
	case WARN:
		return "[WARN]"
	default:
		return "[FAIL]"
	}
}

// Result is one check outcome.
type Result struct {
	Status  Status
	Message string
}

// Failed reports whether any result is a FAIL.
func Failed(results []Result) bool {
	for _, r := range results {
		if r.Status == FAIL {
			return true
		}
	}
	return false
}

// Options control a doctor run.
type Options struct {
	// Workspace is the absolute workspace directory.
	Workspace string
	// Engine probes Docker. When nil, Docker checks are skipped (offline
	// mode used by tests and by serve's config-only preflight).
	Engine Engine
	// LookupEnv resolves host environment variables for env: references.
	LookupEnv func(string) (string, bool)
}

// Run executes every check in order and returns the results.
func Run(ctx context.Context, opts Options) []Result {
	var results []Result
	add := func(s Status, format string, args ...any) {
		results = append(results, Result{Status: s, Message: fmt.Sprintf(format, args...)})
	}

	// 1. Docker daemon.
	if opts.Engine != nil {
		if err := opts.Engine.Ping(ctx); err != nil {
			add(FAIL, "docker daemon not reachable: %v", err)
			return results // nothing else is actionable without a daemon
		}
		add(OK, "docker daemon reachable")
	}

	// 2. Workspace + manifest.
	if _, err := os.Stat(opts.Workspace); err != nil {
		add(FAIL, "workspace missing: %s", opts.Workspace)
		return results
	}
	add(OK, "workspace: %s", opts.Workspace)

	manifestPath := filepath.Join(opts.Workspace, "piccolo.yaml")
	m, err := manifest.Load(manifestPath)
	if err != nil {
		add(FAIL, "manifest: %v", err)
		return results
	}
	add(OK, "manifest: piccolo.yaml")

	// 3. Image presence (warning only).
	if opts.Engine != nil {
		present, err := opts.Engine.ImageExists(ctx, m.Container.Image)
		switch {
		case err != nil:
			add(WARN, "image check failed for %s: %v", m.Container.Image, err)
		case !present:
			add(WARN, "image not present locally: %s (serve will pull it once)", m.Container.Image)
		default:
			add(OK, "image present: %s", m.Container.Image)
		}
	}

	// 4. Env references.
	lookup := opts.LookupEnv
	if lookup == nil {
		lookup = os.LookupEnv
	}
	for _, ref := range m.EnvRefs() {
		if _, found := lookup(ref); !found {
			add(WARN, "piccolo.yaml env reference %s: host variable not set", "env:"+ref)
		}
	}

	// 5. Stale container (warning only).
	if opts.Engine != nil {
		if exists, err := opts.Engine.ContainerExists(ctx, m.Container.Name); err == nil && exists {
			add(WARN, "stale container %q exists (serve will replace it)", m.Container.Name)
		}
	}

	// 6. PicoClaw config.
	configPath := filepath.Join(opts.Workspace, "config.json")
	data, err := os.ReadFile(configPath)
	if err != nil {
		add(FAIL, "picoclaw config: %v", err)
		return results
	}
	var cfg picoclaw.Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		add(FAIL, "config.json does not parse: %v", err)
		return results
	}
	if cfg.Version != picoclaw.SchemaVersion {
		add(FAIL, "config.json schema version %d (want %d)", cfg.Version, picoclaw.SchemaVersion)
		return results
	}
	add(OK, "picoclaw config: config.json (schema v%d)", cfg.Version)

	// 7. Agents.
	results = append(results, checkAgents(opts.Workspace, cfg)...)

	// 8. Models.
	results = append(results, checkModels(cfg)...)

	return results
}

func checkAgents(workspace string, cfg picoclaw.Config) []Result {
	var results []Result
	add := func(s Status, format string, args ...any) {
		results = append(results, Result{Status: s, Message: fmt.Sprintf(format, args...)})
	}

	if len(cfg.Agents.List) == 0 {
		add(FAIL, "config.json agents.list is empty")
		return results
	}
	defaults := 0
	for _, a := range cfg.Agents.List {
		if a.Default {
			defaults++
		}
	}
	if defaults != 1 {
		add(FAIL, "config.json agents.list has %d default agents (want exactly 1)", defaults)
	}

	ids := map[string]bool{}
	for _, a := range cfg.Agents.List {
		if strings.TrimSpace(a.ID) == "" {
			add(FAIL, "config.json agent entry with empty id")
			continue
		}
		if ids[a.ID] {
			add(FAIL, "duplicate agent id %q", a.ID)
		}
		ids[a.ID] = true

		ws := a.Workspace
		if ws == "" {
			ws = cfg.Agents.Defaults.Workspace
		}
		hostPath, ok := translateWorkspacePath(workspace, ws)
		if !ok {
			add(FAIL, "agent %q: workspace path %q is not inside the container home", a.ID, ws)
			continue
		}
		if st, err := os.Stat(hostPath); err != nil || !st.IsDir() {
			add(FAIL, "agent %q: workspace missing: %s", a.ID, hostPath)
			continue
		}
		agentMD := filepath.Join(hostPath, "AGENT.md")
		fm, err := parseFrontmatter(agentMD)
		if err != nil {
			add(FAIL, "agent %q: %v", a.ID, err)
			continue
		}
		suffix := ""
		if a.Default {
			suffix = " (default)"
		}
		add(OK, "agent: %s%s", a.ID, suffix)
		_ = fm
	}

	// Subagent references must resolve to configured agents.
	for _, a := range cfg.Agents.List {
		if a.Subagents == nil {
			continue
		}
		for _, ref := range a.Subagents.AllowAgents {
			if !ids[ref] {
				add(FAIL, "agent %q: subagents.allow_agents references unknown agent %q", a.ID, ref)
			}
		}
	}
	return results
}

func checkModels(cfg picoclaw.Config) []Result {
	var results []Result
	add := func(s Status, format string, args ...any) {
		results = append(results, Result{Status: s, Message: fmt.Sprintf(format, args...)})
	}

	if len(cfg.ModelList) == 0 {
		add(FAIL, "config.json model_list is empty")
		return results
	}
	names := map[string]bool{}
	for _, mdl := range cfg.ModelList {
		names[mdl.ModelName] = true
		if len(mdl.APIKeys) == 0 || isPlaceholderKey(mdl.APIKeys[0]) {
			add(WARN, "model %q has a placeholder or empty API key", mdl.ModelName)
		} else {
			add(OK, "model: %s (%s)", mdl.ModelName, mdl.Model)
		}
	}
	// Every agent-referenced model must exist in model_list.
	refs := map[string]bool{cfg.Agents.Defaults.ModelName: true}
	for _, a := range cfg.Agents.List {
		if a.ModelName != "" {
			refs[a.ModelName] = true
		}
	}
	for ref := range refs {
		if ref != "" && !names[ref] {
			add(FAIL, "model_name %q is referenced but missing from model_list", ref)
		}
	}
	return results
}

func isPlaceholderKey(key string) bool {
	k := strings.ToLower(strings.TrimSpace(key))
	switch {
	case k == "":
		return true
	case strings.HasPrefix(k, "sk-your"), strings.HasPrefix(k, "your-"):
		return true
	case strings.Contains(k, "placeholder"), strings.Contains(k, "changeme"), strings.Contains(k, "xxx"):
		return true
	}
	return false
}

// translateWorkspacePath maps a container-side workspace path (e.g.
// ~/.picoclaw/agents/main or /root/.picoclaw/agents/main) onto the host
// workspace directory.
func translateWorkspacePath(workspace, containerPath string) (string, bool) {
	p := strings.TrimSpace(containerPath)
	for _, prefix := range []string{"~/.picoclaw/", "/root/.picoclaw/"} {
		if rest, ok := strings.CutPrefix(p, prefix); ok {
			return filepath.Join(workspace, filepath.FromSlash(rest)), true
		}
	}
	return "", false
}

// frontmatter is the AGENT.md YAML header.
type frontmatter struct {
	Name        string   `yaml:"name"`
	Description string   `yaml:"description"`
	Tools       []string `yaml:"tools"`
	Model       string   `yaml:"model"`
}

// parseFrontmatter reads and parses the YAML frontmatter of an AGENT.md file.
func parseFrontmatter(path string) (*frontmatter, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("missing agent file: %s", path)
		}
		return nil, err
	}
	body := string(data)
	if !strings.HasPrefix(body, "---\n") {
		return nil, fmt.Errorf("%s has no YAML frontmatter", path)
	}
	rest := body[4:]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return nil, fmt.Errorf("%s frontmatter is not terminated", path)
	}
	var fm frontmatter
	if err := yaml.Unmarshal([]byte(rest[:end]), &fm); err != nil {
		return nil, fmt.Errorf("%s frontmatter does not parse: %w", path, err)
	}
	if strings.TrimSpace(fm.Name) == "" {
		return nil, fmt.Errorf("%s frontmatter has no name", path)
	}
	return &fm, nil
}
