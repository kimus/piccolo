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

	"github.com/kimus/piccolo/internal/fileperm"
	"github.com/kimus/piccolo/internal/manifest"
	"github.com/kimus/piccolo/internal/picoclaw"
	"github.com/kimus/piccolo/internal/runtimeimage"
	"github.com/kimus/piccolo/internal/workspace"
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
	// EncryptKeys records the opt-in. It does not itself encrypt anything: the
	// workspace has no key until the caller generates one, which happens after the
	// user confirms the plan. Carried on Inputs because piccolo.yaml is rendered
	// here, and the two files must agree about whether this workspace is encrypted.
	EncryptKeys bool
	// Passphrase is the enc:// decryption passphrase recorded into piccolo.yaml
	// when EncryptKeys is set.
	Passphrase string
	// Encrypt turns a plaintext credential into the value that gets written. nil
	// writes plaintext, which is the default and the only behaviour before a
	// workspace opts in.
	//
	// It is a callback rather than a key path plus passphrase so this package stays
	// free of the crypto and of the filesystem: BuildPlan must be callable before
	// the workspace directory exists, and it is called a second time after the key
	// is generated. Whatever it returns is what lands in config.json, so plaintext
	// never occupies a written byte.
	Encrypt func(plaintext string) (string, error)
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
	// The passphrase itself is validated by the manifest it is about to be written
	// into, so init cannot produce a piccolo.yaml that the loader then refuses.
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
	if in.EncryptKeys {
		// The passphrase is written into piccolo.yaml, which is what lets an
		// encrypted workspace start with no setup ritual. piccolo.yaml is committed
		// by design, so this does NOT protect a copied workspace — see the field
		// comment in manifest.Secrets and the README wording.
		m.Secrets.Passphrase = in.Passphrase
	}
	return m
}

// PicoclawConfig builds the config.json content (schema v3).
// credentialValues applies the encryption callback when the workspace asked for
// one. An error is returned rather than falling back to plaintext: writing an
// unprotected credential after the user asked for protection is the exact silent
// degradation this feature exists to remove, so a plan that cannot be built is
// strictly better than a plan that lies.
//
// An empty or already-indirect value passes through untouched — encrypting a
// placeholder would seal a non-secret, and re-wrapping an enc:// value corrupts it.
func (in Inputs) credentialValues(plaintext string) ([]string, error) {
	if in.Encrypt == nil {
		return []string{plaintext}, nil
	}
	v := strings.TrimSpace(plaintext)
	if v == "" || strings.HasPrefix(v, "enc://") || strings.HasPrefix(v, "file://") {
		return []string{plaintext}, nil
	}
	enc, err := in.Encrypt(plaintext)
	if err != nil {
		return nil, fmt.Errorf("encrypt generated credential: %w", err)
	}
	return []string{enc}, nil
}

// PicoclawConfig renders the config.json for these inputs. It returns an error
// only when an encryption callback fails; every other field is derived without
// I/O, so a caller can build a plan before the workspace directory exists.
func (in Inputs) PicoclawConfig() (*picoclaw.Config, error) {
	provider, model := normalizeModelRef(in.Model.Provider, in.Model.Model)
	apiKeys, err := in.credentialValues(in.Model.APIKey)
	if err != nil {
		return nil, err
	}

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
				Workspace: picoclaw.AgentWorkspace("main"),
				// Written explicitly FALSE. PicoClaw's own default is true
				// (pkg/config/defaults.go:29), so an absent key means restricted;
				// this field carries no omitempty and must stay that way.
				RestrictToWorkspace:       false,
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
			APIKeys:   apiKeys,
			APIBase:   in.Model.APIBase,
		}},
		Tools: picoclaw.Tools{
			Web:       picoclaw.ToolToggle{Enabled: in.WebSearch},
			Exec:      picoclaw.ToolToggle{Enabled: in.ShellExec},
			Spawn:     picoclaw.ToolToggle{Enabled: true},
			Subagent:  picoclaw.ToolToggle{Enabled: true},
			LoadImage: picoclaw.ToolToggle{Enabled: true},
		},
	}, nil
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
// File is one generated workspace file.
type File struct {
	// Rel is the workspace-relative path.
	Rel string
	// Content is the full file content.
	Content []byte
	// Mode is the file permission (0o644 default).
	Mode os.FileMode
	// Source names the template that produced this file. It is recorded in the
	// workspace provenance so a staleness report can name the code to fix
	// instead of the copy that was found stale. Empty means "not Piccolo's
	// content to attribute" and the file is left out of the record.
	Source string
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
	p.addPrivate("piccolo.yaml", []byte(manifestHeader+yamlOut.String()))

	cfg, err := in.PicoclawConfig()
	if err != nil {
		return nil, err
	}
	jsonBytes, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("render config.json: %w", err)
	}
	// Credential-bearing: under --no-encrypt the model API key and channel tokens sit
	// in this file as readable text, so it gets the same private mode as piccolo.yaml
	// rather than a mode that depends on which flags the user happened to pass.
	p.addPrivate(filepath.Join("picoclaw", "config.json"), append(jsonBytes, '\n'))
	p.addSourced(filepath.Join("picoclaw", "skills", FileHandlingSkillName, "SKILL.md"), SourceFileHandlingSkill, []byte(FileHandlingSkillMD()))
	agents := append([]string{"main"}, sortedSpecialists(in.Specialists)...)
	for _, id := range agents {
		role := "main"
		if id != "main" {
			role = id
		}
		for _, f := range agentFiles(role, in) {
			p.addSourced(filepath.Join("picoclaw", "agents", id, f.Rel), agentSource(f.Rel), f.Content)
		}
	}
	return p, nil
}

// agentSource maps a generated agent file to the template that produced it.
// Unlisted names are left unattributed rather than guessed at: a wrong source
// label would point a staleness report at the wrong code.
func agentSource(rel string) string {
	switch rel {
	case "AGENT.md":
		return SourceAgentMD
	case "SOUL.md":
		return SourceAgentSoul
	case "USER.md":
		return SourceAgentUser
	case filepath.Join("memory", "MEMORY.md"):
		return SourceAgentMemory
	default:
		return ""
	}
}

const manifestHeader = "# Generated by piccolo init. Piccolo-owned configuration —\n" +
	"# PicoClaw runtime behavior (agents, models, tools, tokens) lives in config.json.\n"

func (p *Plan) add(rel string, content []byte) {
	p.Files = append(p.Files, File{Rel: rel, Content: content, Mode: 0o644})
}

// addSourced records which template produced the file, so the workspace keeps a
// pointer from a generated copy back to its source.
// addPrivate records a file that must not be group- or world-readable, used for
// piccolo.yaml because it carries the enc:// passphrase.
func (p *Plan) addPrivate(rel string, content []byte) {
	p.Files = append(p.Files, File{Rel: rel, Content: content, Mode: fileperm.Private})
}

func (p *Plan) addSourced(rel, source string, content []byte) {
	p.Files = append(p.Files, File{Rel: rel, Content: content, Mode: 0o644, Source: source})
}

// Source labels for generated content. They name code, not files a user should
// edit, because the whole point is that a stale copy is fixed at the source.
const (
	SourceFileHandlingSkill = "internal/generate/templates.go:FileHandlingSkillMD"
	SourceAgentMD           = "internal/generate/templates.go:agentMD"
	SourceAgentSoul         = "internal/generate/templates.go:soulMD"
	SourceAgentUser         = "internal/generate/templates.go:userMD"
	SourceAgentMemory       = "internal/generate/templates.go:memoryMD"
)

// RecordProvenance updates the workspace record for the plan files that were
// actually written. Files the create-only rule skipped keep whatever entry they
// already had: Piccolo has no new claim about bytes it did not write, and
// re-attributing them would make the record say a file is untouched-generated
// when it only looks that way because generation declined to touch it.
func (p *Plan) RecordProvenance(dir string, written []string) error {
	wrote := make(map[string]bool, len(written))
	for _, rel := range written {
		wrote[toSlash(rel)] = true
	}
	prov, err := LoadProvenance(dir)
	if err != nil {
		return err
	}
	if prov == nil {
		// First write after the rename: carry the old record's entries forward
		// rather than starting empty, or every file it described would silently
		// become "no record" and doctor would stop telling customised copies from
		// stale ones.
		if seeded, serr := SeedFromLegacy(dir); serr != nil {
			return serr
		} else if seeded != nil {
			prov = seeded
		}
	}
	if prov == nil {
		prov = NewProvenance(VersionStamp())
	} else if VersionStamp() != "" {
		prov.Piccolo = VersionStamp()
	}
	for _, f := range p.Files {
		if f.Source == "" || !wrote[toSlash(f.Rel)] {
			continue
		}
		prov.Record(f.Rel, f.Source, f.Content)
	}
	return prov.Save(dir)
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
		// WriteFile's mode only applies when the file is CREATED; an existing
		// piccolo.yaml kept 0644 through every rewrite, so the declared mode has to
		// be asserted after the fact to mean anything.
		if err := os.Chmod(path, mode); err != nil {
			return written, skipped, err
		}
		written = append(written, f.Rel)
	}
	return written, skipped, nil
}

// GitignoreEntries is the single source of truth for what a Piccolo workspace
// must never commit. It exists as one list because three places need it (init,
// doctor's read-only check, and the flat-layout migration), and they had already
// drifted: the check carried five entries while init wrote eight, so a workspace
// missing picoclaw/.security.yml — the file that holds every credential PicoClaw
// relocates out of config.json — was reported as clean. Adding an entry here is
// the only way to add it everywhere at once.
var GitignoreEntries = []string{
	"picoclaw/config.json",
	// Every confirmed write leaves the previous file behind, and before encryption
	// that previous file is the plaintext the user just removed. Ignoring it is not
	// tidiness: `git add -A` would commit the credentials that
	// `piccolo secrets encrypt` exists to take out of the tree.
	"picoclaw/config.json.bak",
	"picoclaw/.security.yml",
	"picoclaw/.picoclaw.pid",
	"picoclaw/launcher-auth.db",
	"picoclaw/logs/",
	"integrations/*/auth/",
	"runtime/",
	GitignoreOSMetadata,
}

// EnsureGitignore creates or appends to dir/.gitignore so sensitive and
// runtime files (config.json holds API keys; .picoclaw.pid holds the gateway
// reload token) and OS directory metadata are never committed. It reports
// whether a change was made.
//
// Callers that write generated content OUTSIDE init (a workspace heal, a future
// migration) must call this too: it is the only writer of the file, and it is
// append-only for missing entries, so a curated .gitignore is never rewritten.
func EnsureGitignore(dir string) (bool, error) {
	path := filepath.Join(dir, ".gitignore")
	entries := GitignoreEntries
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
	for _, entry := range append(append([]string{}, entries...), conditionalEntries(dir)...) {
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

// GitignoreOSMetadata is the ignore rule for Finder/Explorer directory
// databases. It is a bare pattern (no slash) so one entry covers every depth,
// which is exactly how this repository shipped: two .DS_Store files were
// tracked with no rule, and any `git add -A` in a macOS workspace commits
// directory metadata alongside the user's agent files.
const GitignoreOSMetadata = ".DS_Store"

// MissingGitignoreEntries reports required entries a workspace's .gitignore does
// not carry. It exists because EnsureGitignore runs only during init: a workspace
// created before an entry was required never gains it, and without a read-only
// check that gap is invisible rather than merely unfixed.
// ConditionalGitignoreEntries are required only once the workspace actually holds
// the thing they cover. Demanding them always made `piccolo doctor` warn on
// untouched plaintext workspaces about a picoclaw/keys/ directory they will never
// have, which is a permanent false warning on a healthy workspace — the exact
// thing this repo keeps having to un-learn, and a sure way to train users to
// ignore the findings that are real.
var ConditionalGitignoreEntries = []struct {
	Path  string
	Entry string
}{
	// The credential encryption key: key file plus committed config.json is the
	// complete recipe for every enc:// value in the workspace.
	{Path: filepath.Join("picoclaw", workspace.KeysDirName), Entry: "picoclaw/" + workspace.KeysDirName + "/"},
}

func MissingGitignoreEntries(dir string) ([]string, error) {
	data, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	present := map[string]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		present[line] = true
	}
	var missing []string
	for _, entry := range GitignoreEntries {
		if !present[entry] {
			missing = append(missing, entry)
		}
	}
	for _, c := range ConditionalGitignoreEntries {
		if present[c.Entry] {
			continue
		}
		if fi, err := os.Stat(filepath.Join(dir, c.Path)); err == nil && fi.IsDir() {
			missing = append(missing, c.Entry)
		}
	}
	return missing, nil
}

// conditionalEntries lists the conditional ignore rules that apply to this
// workspace right now.
func conditionalEntries(dir string) []string {
	var out []string
	for _, c := range ConditionalGitignoreEntries {
		if fi, err := os.Stat(filepath.Join(dir, c.Path)); err == nil && fi.IsDir() {
			out = append(out, c.Entry)
		}
	}
	return out
}
