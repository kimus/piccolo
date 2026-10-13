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
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/kimus/piccolo/internal/generate"
	"github.com/kimus/piccolo/internal/manifest"
	"github.com/kimus/piccolo/internal/picoclaw"
	"github.com/kimus/piccolo/internal/runtimeimage"
	"github.com/kimus/piccolo/internal/workspace"
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
	layout := workspace.NewLayout(opts.Workspace)
	if workspace.IsLegacyFlat(opts.Workspace) {
		add(FAIL, "workspace layout: legacy flat layout detected — run 'piccolo workspace migrate'")
	} else if _, err := os.Stat(layout.PicoclawHome()); err != nil {
		add(FAIL, "workspace layout: missing picoclaw/ directory — run 'piccolo init' or 'piccolo workspace migrate'")
	} else {
		missing := []string{}
		for _, dir := range []struct{ label, path string }{
			{"integrations/", layout.Integrations()},
			{"runtime/devbox/", layout.RuntimeDevbox()},
			{"runtime/nix/", layout.RuntimeNix()},
			{"runtime/media/", layout.RuntimeMedia()},
		} {
			if st, err := os.Stat(dir.path); err != nil || !st.IsDir() {
				missing = append(missing, dir.label)
			}
		}
		if len(missing) > 0 {
			add(WARN, "workspace layout: missing %s — run 'piccolo workspace migrate --yes' or 'piccolo serve' to create runtime dirs", strings.Join(missing, ", "))
		} else {
			add(OK, "workspace layout: structured")
		}
	}

	// 3. Image presence (warning only).
	if opts.Engine != nil {
		present, err := opts.Engine.ImageExists(ctx, m.Container.Image)
		switch {
		case err != nil:
			add(WARN, "image check failed for %s: %v", m.Container.Image, err)
		case !present:
			if isPiccoloLocalRuntimeImage(m.Container.Image) {
				add(WARN, "local Piccolo runtime image not present: %s (run 'piccolo image build --set')", m.Container.Image)
			} else {
				add(WARN, "image not present locally: %s (serve will pull it once)", m.Container.Image)
			}
		default:
			add(OK, "image present: %s", m.Container.Image)
		}
	}

	// 3b. Web console consistency: a published :18800 binding only makes sense
	// with the launcher image (which serves the dashboard). A plain gateway
	// image would publish a dead port with nothing listening behind it.
	results = append(results, checkWebConsole(m)...)

	// 3c. Runtime toolchain configuration (live command availability is reported
	// by `piccolo runtime status/apply`; doctor remains daemon-free here).
	results = append(results, checkRuntimeToolchain(m)...)
	results = append(results, checkIntegrations(m)...)

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
	configPath := layout.Config()
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

	// 9. Image understanding. PicoClaw can receive channel attachments; Piccolo
	// recommends routing them through a vision-capable image_model and the
	// load_image tool.
	results = append(results, checkImageUnderstanding(opts.Workspace, cfg, data)...)

	// 10. Exec safety.
	results = append(results, checkExecSafety(data)...)

	// 11. Channels (raw parse — setup manages channel_list; unknown fields
	// are user-owned and ignored here).
	results = append(results, checkChannels(data)...)

	return results
}

func checkRuntimeToolchain(m *manifest.Manifest) []Result {
	tc := m.Runtime.Toolchain
	packages := tc.Packages
	commands := tc.Commands
	if len(packages) == 0 {
		packages = runtimeimage.DefaultPackages
	}
	if len(commands) == 0 {
		commands = runtimeimage.DefaultCommands
	}
	var results []Result
	results = append(results, Result{Status: OK, Message: fmt.Sprintf("runtime toolchain: %d packages, %d commands configured", len(packages), len(commands))})
	for _, want := range []string{"poppler-utils", "libreoffice", "imagemagick"} {
		if !hasString(packages, want) {
			results = append(results, Result{Status: WARN, Message: fmt.Sprintf("runtime toolchain: default media/office package %q is not configured", want)})
		}
	}
	for _, want := range []string{"pdfinfo", "pdftoppm", "pdftocairo", "soffice", "magick", "convert", "identify"} {
		if !hasString(commands, want) {
			results = append(results, Result{Status: WARN, Message: fmt.Sprintf("runtime toolchain: useful media/office command %q is not exposed", want)})
		}
	}
	return results
}

func checkIntegrations(m *manifest.Manifest) []Result {
	if len(m.Runtime.Integrations) == 0 {
		return nil
	}
	var results []Result
	for _, in := range m.Runtime.Integrations {
		label := in.DisplayName
		if label == "" {
			label = in.Name
		}
		results = append(results, Result{Status: OK, Message: fmt.Sprintf("integration installed: %s (%s)", label, in.Ref)})
		if len(in.SetupCommand) == 0 {
			results = append(results, Result{Status: WARN, Message: fmt.Sprintf("integration %s has no setup command", in.Name)})
		}
	}
	return results
}

func hasString(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

func checkExecSafety(configData []byte) []Result {
	var results []Result
	var raw struct {
		Tools struct {
			Exec struct {
				Enabled     *bool `json:"enabled"`
				AllowRemote *bool `json:"allow_remote"`
			} `json:"exec"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(configData, &raw); err != nil {
		return results
	}
	if raw.Tools.Exec.AllowRemote != nil && *raw.Tools.Exec.AllowRemote {
		results = append(results, Result{Status: WARN, Message: "exec: tools.exec.allow_remote is true — remote chats can request shell commands inside the container subject to PicoClaw safety checks; enable only for trusted channels/users"})
	}
	return results
}

// checkChannels inspects channel_list entries for the classic half-configured
// state: enabled channel without a real token (empty/placeholder).
func checkChannels(configData []byte) []Result {
	var results []Result
	add := func(s Status, format string, args ...any) {
		results = append(results, Result{Status: s, Message: fmt.Sprintf(format, args...)})
	}
	var raw struct {
		ChannelList map[string]struct {
			Enabled   bool           `json:"enabled"`
			Type      string         `json:"type"`
			AllowFrom []string       `json:"allow_from"`
			Settings  map[string]any `json:"settings"`
		} `json:"channel_list"`
	}
	if err := json.Unmarshal(configData, &raw); err != nil || len(raw.ChannelList) == 0 {
		return results // absent/other shape: not a failure, config is user-owned
	}
	names := make([]string, 0, len(raw.ChannelList))
	for n := range raw.ChannelList {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		ch := raw.ChannelList[name]
		if !ch.Enabled {
			add(OK, "channel: %s (disabled)", name)
			continue
		}
		tokenKey := ""
		for _, k := range []string{"token", "bot_token", "app_token", "client_secret", "app_secret"} {
			if _, ok := ch.Settings[k]; ok {
				tokenKey = k
				break
			}
		}
		if tokenKey == "" {
			add(WARN, "channel %q is enabled but has no token in settings", name)
			continue
		}
		tok, _ := ch.Settings[tokenKey].(string)
		if isPlaceholderKey(tok) {
			add(WARN, "channel %q is enabled but %q is empty or a placeholder", name, tokenKey)
			continue
		}
		if len(ch.AllowFrom) == 0 {
			add(WARN, "channel %q: allow_from is empty — PicoClaw accepts messages from ANYONE (set user IDs or use \"*\" to acknowledge)", name)
			continue
		}
		if len(ch.AllowFrom) == 1 && ch.AllowFrom[0] == "*" {
			add(WARN, "channel %q: allow_from is \"*\" — open to everyone who can reach the bot", name)
			continue
		}
		add(OK, "channel: %s", name)
	}
	return results
}

// checkWebConsole verifies the optional web console is configured
// consistently. The console is enabled solely by a published :18800 binding;
// that binding only works with the launcher image, which serves the dashboard.
// A disabled console (no :18800) is perfectly valid and produces no result.
func checkWebConsole(m *manifest.Manifest) []Result {
	var results []Result
	if !publishesContainerPort(m.Container.Ports, 18800) {
		return results // console disabled — nothing to report
	}
	if !isLauncherImage(m.Container.Image) {
		results = append(results, Result{Status: WARN, Message: fmt.Sprintf(
			"piccolo.yaml publishes :18800 (web console) but image %q is not the launcher image — the port would have nothing listening", m.Container.Image)})
		return results
	}
	results = append(results, Result{Status: OK, Message: "web console: enabled (:18800, launcher image)"})
	return results
}

// isLauncherImage reports whether ref points at the PicoClaw launcher image
// (the flavor that serves the web dashboard).
func isPiccoloLocalRuntimeImage(ref string) bool {
	return runtimeimage.IsLocalRuntimeImage(ref) || strings.HasPrefix(ref, "piccolo-picoclaw:")
}

func isLauncherImage(ref string) bool {
	ref = strings.TrimSpace(ref)
	if runtimeimage.IsLocalRuntimeImage(ref) {
		return true
	}
	if ref == generate.LauncherImage {
		return true
	}
	// Allow registry/path variants pinned to the "launcher" tag.
	if i := strings.LastIndex(ref, ":"); i >= 0 && i > strings.LastIndex(ref, "/") {
		return ref[i+1:] == "launcher"
	}
	return false
}

// publishesContainerPort reports whether any binding maps the given container
// port. Bindings look like [host_ip:]host_port:container_port[/proto].
func publishesContainerPort(ports []string, containerPort int) bool {
	want := fmt.Sprintf(":%d", containerPort)
	for _, p := range ports {
		portion := strings.TrimSpace(p)
		if i := strings.LastIndex(portion, "/"); i >= 0 {
			portion = portion[:i]
		}
		if strings.HasSuffix(portion, want) {
			return true
		}
	}
	return false
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
		if _, err := parseFrontmatter(agentMD); err != nil {
			add(FAIL, "agent %q: %v", a.ID, err)
			continue
		}
		suffix := ""
		if a.Default {
			suffix = " (default)"
		}
		add(OK, "agent: %s%s", a.ID, suffix)
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
	if cfg.Agents.Defaults.ImageModel != "" {
		refs[cfg.Agents.Defaults.ImageModel] = true
	}
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

func checkImageUnderstanding(workspace string, cfg picoclaw.Config, configData []byte) []Result {
	var results []Result
	add := func(s Status, format string, args ...any) {
		results = append(results, Result{Status: s, Message: fmt.Sprintf(format, args...)})
	}

	if strings.TrimSpace(cfg.Agents.Defaults.ImageModel) == "" {
		add(WARN, "image understanding: agents.defaults.image_model is not configured — image attachments may be received but not visually interpreted")
	} else {
		add(OK, "image understanding: image_model %s", cfg.Agents.Defaults.ImageModel)
	}

	var raw struct {
		Tools map[string]json.RawMessage `json:"tools"`
	}
	if err := json.Unmarshal(configData, &raw); err == nil {
		if msg, ok := raw.Tools["load_image"]; ok {
			var tool struct {
				Enabled *bool `json:"enabled"`
			}
			if err := json.Unmarshal(msg, &tool); err == nil && tool.Enabled != nil && !*tool.Enabled {
				add(WARN, "image understanding: tools.load_image.enabled is false — agents cannot visually inspect local image attachments")
			}
		}
	}

	for _, a := range cfg.Agents.List {
		ws := a.Workspace
		if ws == "" {
			ws = cfg.Agents.Defaults.Workspace
		}
		hostPath, ok := translateWorkspacePath(workspace, ws)
		if !ok {
			continue // checkAgents reports this precisely.
		}
		fm, err := parseFrontmatter(filepath.Join(hostPath, "AGENT.md"))
		if err != nil {
			continue // checkAgents reports this precisely.
		}
		if len(fm.Tools) > 0 && !hasTool(fm.Tools, "load_image") {
			add(WARN, "image understanding: agent %q AGENT.md does not allow load_image", a.ID)
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

func hasTool(tools []string, want string) bool {
	for _, t := range tools {
		if strings.TrimSpace(t) == want {
			return true
		}
	}
	return false
}

// translateWorkspacePath maps a container-side workspace path (e.g.
// ~/.picoclaw/agents/main or /root/.picoclaw/agents/main) onto the host
// picoclaw/ directory.
func translateWorkspacePath(workspaceRoot, containerPath string) (string, bool) {
	p := strings.TrimSpace(containerPath)
	layout := workspace.NewLayout(workspaceRoot)
	for _, prefix := range []string{"~/.picoclaw/", "/root/.picoclaw/"} {
		if rest, ok := strings.CutPrefix(p, prefix); ok {
			return filepath.Join(layout.PicoclawHome(), filepath.FromSlash(rest)), true
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
