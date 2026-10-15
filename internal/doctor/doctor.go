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

	"github.com/kimus/piccolo/internal/docker"
	"github.com/kimus/piccolo/internal/generate"
	"github.com/kimus/piccolo/internal/manifest"
	"github.com/kimus/piccolo/internal/picoclaw"
	"github.com/kimus/piccolo/internal/runtimeimage"
	"github.com/kimus/piccolo/internal/runtimeplan"
	"github.com/kimus/piccolo/internal/workspace"
)

// Engine is the narrow Docker surface doctor needs.
type Engine interface {
	Ping(ctx context.Context) error
	ImageExists(ctx context.Context, image string) (bool, error)
	ImageLabels(ctx context.Context, image string) (map[string]string, bool, error)
	ContainerExists(ctx context.Context, name string) (bool, error)
	// StateByName reads the container that is actually serving traffic, which is the
	// only way to notice it runs a DIFFERENT image from the one piccolo.yaml names.
	// Every other image check here consults the local image store, and a store can
	// hold a correct image while an older container keeps running the old binaries.
	StateByName(ctx context.Context, name string) (docker.State, error)
	ListByPrefix(ctx context.Context, prefix string) ([]string, error)
	ListImages(ctx context.Context) ([]docker.ImageRecord, error)
	ContainerImages(ctx context.Context) (map[string][]string, error)
}

// Unreferenced runtime images are reported once they exceed either bound, and
// the smaller threshold wins: two images is the point where a workspace is
// holding more than "current + rollback", and 10 GB is where accumulation
// becomes a disk-full event rather than untidiness.
const (
	unreferencedImageWarnCount = 2
	unreferencedImageWarnBytes = int64(10) << 30
)

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

	results = append(results, checkGeneratedProvenance(opts.Workspace)...)
	results = append(results, checkGitignoreHygiene(opts.Workspace)...)

	// 3. Image presence (warning only).
	imagePresent := true
	if opts.Engine != nil {
		present, err := opts.Engine.ImageExists(ctx, m.Container.Image)
		imagePresent = present && err == nil
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

	// 3b. Runtime image currency. The image in use must contain the toolchain and
	// the integration versions the workspace describes. Doctor reports the drift
	// and names the rebuild; it never starts one, because a stale image is not an
	// error — adopting a rebuild is a user decision.
	results = append(results, checkImageDrift(ctx, opts.Engine, m, imagePresent)...)
	results = append(results, checkImageRetention(ctx, opts.Engine, m)...)

	// 3c. An interrupted promotion leaves the previous gateway parked under the
	// reserved name. The canonical name always belongs to what should be running,
	// so this is the one abnormal state to look for.
	if opts.Engine != nil {
		if parked, err := opts.Engine.ContainerExists(ctx, docker.PreviousName(m.Container.Name)); err == nil && parked {
			add(WARN, "parked promotion target %q exists — a previous restart was interrupted; 'piccolo serve' restores it", docker.PreviousName(m.Container.Name))
		}
	}

	// 3d. Web console consistency: a published :18800 binding only makes sense
	// with the launcher image (which serves the dashboard). A plain gateway
	// image would publish a dead port with nothing listening behind it.
	results = append(results, checkWebConsole(m)...)

	// 3e. Runtime toolchain configuration (live command availability is reported
	// by `piccolo runtime status`; doctor stays daemon-free here).
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

	// 5. Stale container (warning only): one that exists but is NOT running. A
	// running container built from the current image is the service itself, not a
	// leftover, and calling it stale made every healthy workspace carry a permanent
	// false warning. A running container built from a DIFFERENT image is reported by
	// checkRunningImage below with the accurate verb (restart), so it is not
	// double-reported here.
	if opts.Engine != nil {
		exists, err := opts.Engine.ContainerExists(ctx, m.Container.Name)
		state, serr := opts.Engine.StateByName(ctx, m.Container.Name)
		if err == nil && exists && !(serr == nil && state.Found && state.Running) {
			add(WARN, "stale container %q exists (serve will replace it)", m.Container.Name)
		}
		results = append(results, checkRunningImage(ctx, opts.Engine, m)...)
		// One-shot setup/status containers are reclaimed automatically before
		// the next run, but a leftover means a previous attempt was interrupted.
		if names, err := opts.Engine.ListByPrefix(ctx, docker.SetupContainerPrefix); err == nil && len(names) > 0 {
			add(WARN, "%d abandoned integration one-shot container(s): %s (removed on the next setup/status run)", len(names), strings.Join(names, ", "))
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
	results = append(results, checkLegacyAgentModelNames(data)...)

	// 9. Image understanding. PicoClaw can receive channel attachments; Piccolo
	// recommends routing them through a vision-capable image_model and the
	// load_image tool.
	results = append(results, checkImageUnderstanding(opts.Workspace, cfg, data)...)

	// 10. Exec safety.
	results = append(results, checkExecSafety(data)...)

	// 11. Channels (raw parse — setup manages channel_list; unknown fields
	// are user-owned and ignored here).
	results = append(results, checkChannels(data)...)

	// 12. MCP reachability: a registered integration server is not a callable tool.
	results = append(results, checkIntegrationRegistration(m, data)...)
	results = append(results, checkMCPAccess(opts.Workspace, cfg, data)...)

	return results
}

// checkIntegrationRegistration closes the half of "installed" that the manifest
// cannot prove. Registration is the LAST act of `piccolo integration setup`: the
// server entry is written from the action result's configPatch, through a
// confirmation gate the user can decline (or reach by pressing esc), and Piccolo
// prints "discarded; integration state was left as-is". That leaves a workspace where
// the image contains the binary, the manifest lists the integration, credentials are
// stored, and config.json has never heard of it — which a user experiences exactly as
// "I added it and it silently does nothing", because checkMCPAccess stays silent when
// no server is registered and nothing else compares the two files.
//
// Warning, never failure: the integration is installed, and completing it is the
// user's action, not Piccolo's to take.
func checkIntegrationRegistration(m *manifest.Manifest, configData []byte) []Result {
	if m == nil || len(m.Runtime.Integrations) == 0 {
		return nil
	}
	var raw struct {
		Tools struct {
			MCP struct {
				Servers map[string]json.RawMessage `json:"servers"`
			} `json:"mcp"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(configData, &raw); err != nil {
		return nil
	}
	var results []Result
	for _, in := range m.Runtime.Integrations {
		// Only an integration that records a server key claims to need one. An empty
		// key means it works without MCP (a CLI-only integration), and demanding an
		// entry would invent a problem.
		key := strings.TrimSpace(in.MCPServerKey)
		if key == "" {
			continue
		}
		if _, ok := raw.Tools.MCP.Servers[key]; ok {
			continue
		}
		ref := integrationHandle(in)
		label := in.Name
		if label == "" {
			label = ref
		}
		results = append(results, Result{Status: WARN, Message: fmt.Sprintf("integration %s is installed and configured but never registered: config.json has no tools.mcp.servers.%s entry — its tools are not offered to any agent; finish it with 'piccolo integration setup %s' and accept the config change", label, key, ref)})
	}
	return results
}

// checkMCPAccess reports whether the MCP servers an integration registered can
// reach an agent at all. Two independent gates stand between a registered server
// and a tool the model can call, and both fail silently:
//
//  1. tools.mcp.enabled — PicoClaw returns from ensureMCPInitialized before it ever
//     reads the servers map while the MCP tool is off (pkg/agent/agent_mcp.go:79),
//     and the toggle defaults to false (pkg/config/defaults.go:428-431). A
//     registered server is therefore inert, and an integration cannot fix that
//     itself: the patch allowlist confines it to tools.mcp.servers.<key> precisely
//     so it cannot touch tool toggles.
//  2. a declared AGENT.md `tools:` list — PicoClaw builds a strict, exact-name
//     allowlist from it (pkg/agent/tool_allowlist.go:143-164, applied at
//     pkg/tools/registry.go:171-184) while MCP tools are named
//     mcp_<server>_<tool> at runtime (pkg/tools/integration/mcp_tool.go:143). No
//     list written at generation time can contain those names, so declaring one on
//     the coordinator revokes every tool every installed integration provides.
//
// A gateway in either state answers /health and executes its integration binaries,
// so the serve readiness gate passes; this check exists because that gate proves
// the binary, not the capability. It stays silent when nothing is installed, since
// an absent integration is not a broken one.
func checkMCPAccess(workspace string, cfg picoclaw.Config, configData []byte) []Result {
	var results []Result
	add := func(s Status, format string, args ...any) {
		results = append(results, Result{Status: s, Message: fmt.Sprintf(format, args...)})
	}

	var raw struct {
		Tools struct {
			MCP struct {
				Enabled *bool `json:"enabled"`
				Servers map[string]struct {
					Enabled *bool `json:"enabled"`
				} `json:"servers"`
			} `json:"mcp"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(configData, &raw); err != nil {
		return results
	}

	var registered, disabled []string
	for name, srv := range raw.Tools.MCP.Servers {
		if srv.Enabled != nil && !*srv.Enabled {
			disabled = append(disabled, name)
			continue
		}
		registered = append(registered, name)
	}
	if len(registered) == 0 && len(disabled) == 0 {
		return results
	}
	sort.Strings(registered)
	sort.Strings(disabled)
	if len(disabled) > 0 {
		add(WARN, "mcp: server %s is registered but disabled — its tools are not offered to any agent", strings.Join(disabled, ", "))
	}
	if len(registered) == 0 {
		return results
	}
	names := strings.Join(registered, ", ")

	if raw.Tools.MCP.Enabled == nil || !*raw.Tools.MCP.Enabled {
		add(WARN, "mcp: tools.mcp.enabled is not true while server(s) {%s} are registered — PicoClaw skips MCP initialization entirely, so agents are handed none of these integrations' tools even though their binaries run; enable it with 'piccolo setup' \u2192 Tools \u2192 MCP tool servers", names)
		return results
	}

	for _, a := range cfg.Agents.List {
		if !a.Default {
			continue
		}
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
		if len(fm.Tools) > 0 {
			add(WARN, "mcp: coordinator %q declares an AGENT.md tools: allowlist — PicoClaw matches tool names exactly and MCP tools are named mcp_<server>_<tool> at runtime, so this agent cannot receive any tool from {%s}; Piccolo generates the coordinator with no tools: line, and deleting that line grants it every tool config.json enables", a.ID, names)
			continue
		}
		add(OK, "mcp: %d server(s) reachable by coordinator %q (%s)", len(registered), a.ID, names)
	}
	return results
}

// checkImageDrift compares the image in use with the image the workspace
// describes and names the reason. It is a warning, never a failure, and it never
// builds: adopting a rebuild is the user's decision, and a doctor run must not
// spend minutes building on someone's behalf.
// checkImageDrift compares the image in use with the image the workspace
// describes. imagePresent comes from the presence check: when the image is
// simply not here, that check already said so, and repeating it as a content
// complaint would be two warnings for one problem.
func checkImageDrift(ctx context.Context, engine Engine, m *manifest.Manifest, imagePresent bool) []Result {
	if engine == nil || !isPiccoloLocalRuntimeImage(m.Container.Image) || !imagePresent {
		return nil
	}
	info, err := runtimeplan.Check(ctx, engine, m)
	if err != nil {
		return []Result{{Status: WARN, Message: fmt.Sprintf("runtime image currency: could not verify (%v)", err)}}
	}
	if !info.Verifiable {
		return nil
	}
	switch info.Kind {
	case runtimeplan.DriftNone:
		return []Result{{Status: OK, Message: fmt.Sprintf("runtime image matches this workspace: %s", info.Current)}}
	case runtimeplan.DriftTemplate:
		// Content is right; the tag is only cosmetically behind. Reporting this as
		// a problem would push the user toward a rebuild that changes nothing.
		return []Result{{Status: OK, Message: fmt.Sprintf("runtime image content matches this workspace (%s); the tag predates a Piccolo recipe change (%s)", info.Current, info.Desired)}}
	}
	return []Result{{
		Status: WARN,
		Message: fmt.Sprintf(
			"runtime image is stale: %s, wants %s (%s) — rebuild with 'piccolo image build --set && piccolo restart'; Piccolo never rebuilds implicitly",
			info.Current, info.Desired, strings.Join(info.Reasons, "; ")),
	}}
}

// checkImageRetention reports locally built runtime images that nothing
// references any more. It is a warning with a count, a size, and the command:
// deleting images on the user's behalf is never doctor's job, and the rollback
// target in particular is the one image nothing else can recover.
func checkImageRetention(ctx context.Context, engine Engine, m *manifest.Manifest) []Result {
	if engine == nil {
		return nil
	}
	images, err := engine.ListImages(ctx)
	if err != nil {
		return []Result{{Status: WARN, Message: fmt.Sprintf("runtime image retention: could not check (%v)", err)}}
	}
	containers, err := engine.ContainerImages(ctx)
	if err != nil {
		return []Result{{Status: WARN, Message: fmt.Sprintf("runtime image retention: could not check (%v)", err)}}
	}
	candidates := make([]runtimeplan.ImageInfo, 0, len(images))
	for _, img := range images {
		for _, ref := range img.RepoTags {
			if !runtimeplan.RuntimeImagePrefix(ref, runtimeimage.DefaultTagPrefix) || strings.HasPrefix(ref, "<none>") {
				continue
			}
			candidates = append(candidates, runtimeplan.ImageInfo{
				Ref: ref, ID: img.ID, Size: img.Size, Created: img.Created,
				Containers: append([]string(nil), containers[ref]...),
			})
		}
	}
	plan := runtimeplan.Retain(candidates, m.Container.Image, m.Container.PreviousImage)
	if len(plan.Removable) == 0 {
		return []Result{{Status: OK, Message: "runtime image retention: no unreferenced runtime images"}}
	}
	if len(plan.Removable) < unreferencedImageWarnCount && plan.Reclaimable < unreferencedImageWarnBytes {
		return []Result{{Status: OK, Message: fmt.Sprintf("runtime image retention: %d unreferenced image(s) (%s), below the reporting threshold", len(plan.Removable), humanBytes(plan.Reclaimable))}}
	}
	return []Result{{Status: WARN, Message: fmt.Sprintf(
		"runtime image retention: %d unreferenced runtime image(s), up to %s — reclaim with 'piccolo image prune' (keeps the image in use and the rollback target); Piccolo never deletes images on its own",
		len(plan.Removable), humanBytes(plan.Reclaimable))}}
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%cB", float64(n)/float64(div), "KMGTPE"[exp])
}

// checkGeneratedProvenance classifies the workspace's generated files from
// Piccolo's own record. The two findings mean opposite things and must never be
// merged into one "differs from template" line:
//
//   - customised: the user changed it. Piccolo must not overwrite it, and a
//     template fix will never land here — reporting it as stale would invite a
//     refresh that destroys their work.
//   - stale: byte-identical to what Piccolo wrote, but the current template
//     renders differently, so the copy predates a fix. Refreshing is strictly
//     better, and the message names the source so a fix lands in code.
//
// Staleness is only provable for templates Piccolo can re-render with no user
// input (today: the file-handling skill). For agent files the honest ceiling is
// "edited or not", so this check says nothing about their currency rather than
// guessing from the record's version string.
func checkGeneratedProvenance(dir string) []Result {
	var results []Result
	add := func(s Status, format string, args ...any) {
		results = append(results, Result{Status: s, Message: fmt.Sprintf(format, args...)})
	}

	prov, err := generate.LoadProvenance(dir)
	if err != nil {
		add(WARN, "generated-file provenance: unreadable %s: %v", generate.ProvenanceFileName, err)
		return results
	}
	if prov == nil {
		add(OK, "generated-file provenance: no record (workspace generated before provenance existed) — 'piccolo setup' writes it on the next change")
		return results
	}

	var customised, stale, missing, current int
	for _, rel := range prov.Paths() {
		data, readErr := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
		if readErr != nil {
			missing++
			add(WARN, "generated file is missing: %s (source %s) — restore it or delete its entry from %s", rel, prov.Source(rel), generate.ProvenanceFileName)
			continue
		}
		rendered, _ := generate.CurrentRender(prov.Source(rel))
		switch prov.Classify(rel, data, rendered) {
		case generate.StatusStale:
			stale++
			add(WARN, "generated file predates the current template: %s — refresh with 'piccolo setup' (fix belongs in %s, not in the copy)", rel, prov.Source(rel))
		case generate.StatusCustomised:
			customised++
		default:
			// Byte-identical to the record. Either it matches the current template,
			// or the template needs user input we cannot re-derive: currency is
			// unknown in that case, so it is not claimed.
			current++
		}
	}
	switch {
	case customised > 0:
		add(OK, "generated files: %d as generated, %d customised by you (user-owned; Piccolo will not overwrite), %d recorded", current, customised, len(prov.Files))
	case missing == 0 && stale == 0:
		add(OK, "generated files: %d as generated by %s", current, orUnknownVersion(prov.Piccolo))
	}
	return results
}

func orUnknownVersion(v string) string {
	if strings.TrimSpace(v) == "" {
		return "an unstamped build"
	}
	return "piccolo " + v
}

// checkGitignoreHygiene reports required .gitignore entries the workspace does
// not carry. Read-only by design: EnsureGitignore only runs during init, so a
// workspace created before an entry was required never gains it, and the normal
// way that surfaces is the junk appearing in the user's own git status — after
// it is already tracked, at which point ignoring it does not untrack it.
func checkGitignoreHygiene(dir string) []Result {
	var results []Result
	missing, err := generate.MissingGitignoreEntries(dir)
	if err != nil {
		return append(results, Result{Status: WARN, Message: fmt.Sprintf("workspace .gitignore: unreadable: %v", err)})
	}
	if len(missing) == 0 {
		return append(results, Result{Status: OK, Message: "workspace .gitignore: secrets, caches, and OS metadata ignored"})
	}
	quoted := make([]string, 0, len(missing))
	for _, entry := range missing {
		quoted = append(quoted, fmt.Sprintf("%q", entry))
	}
	return append(results, Result{Status: WARN, Message: fmt.Sprintf("workspace .gitignore is missing %s — run 'piccolo init' here (it appends missing entries and never overwrites your files) or add them by hand", strings.Join(quoted, ", "))})
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
		results = append(results, Result{Status: OK, Message: fmt.Sprintf("integration installed: %s (%s)", label, integrationHandle(in))})
		if len(in.SetupCommand) == 0 {
			results = append(results, Result{Status: WARN, Message: fmt.Sprintf("integration %s has no setup command", in.Name)})
		}
	}
	return results
}

// integrationHandle is the identifier a user can paste straight back into another
// piccolo command. Integration.Ref is the RETIRED source-build field and is empty for
// every release-based integration, so printing it alone produced both an empty
// "installed: Google Workspace ()" and, worse, a remedy reading "piccolo integration
// setup " with nothing in it — advice that cannot be followed. The order mirrors what
// integrationMatches actually accepts.
func integrationHandle(in manifest.Integration) string {
	for _, candidate := range []string{in.Package, in.Name, in.Ref} {
		if s := strings.TrimSpace(candidate); s != "" {
			return s
		}
	}
	return "(unnamed integration)"
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
		Agents struct {
			Defaults struct {
				// Pointer: an absent key is PicoClaw's default, which is TRUE
				// (pkg/config/defaults.go:29), not false.
				RestrictToWorkspace *bool `json:"restrict_to_workspace"`
			} `json:"defaults"`
		} `json:"agents"`
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
	// Absent means restricted; only an explicit false clears it. Piccolo generates
	// false, so this fires on a workspace hand-hardened or created by an older
	// Piccolo, and names the guard the agent will keep tripping.
	restricted := raw.Agents.Defaults.RestrictToWorkspace == nil || *raw.Agents.Defaults.RestrictToWorkspace
	if restricted {
		if raw.Tools.Exec.Enabled != nil && *raw.Tools.Exec.Enabled {
			results = append(results, Result{Status: WARN, Message: "exec: agents.defaults.restrict_to_workspace is true while tools.exec is enabled — PicoClaw blocks shell commands naming any path outside the agent's own workspace (\"Command blocked by safety guard (path outside working dir)\"), and allow_read_outside_workspace does NOT relax that exec check; turn it off with 'piccolo setup' → Tools → Workspace path guard unless you want the sandbox"})
		} else {
			results = append(results, Result{Status: WARN, Message: "agents.defaults.restrict_to_workspace is true — write/edit/append stay confined to each agent workspace (Piccolo now generates false; the container is the boundary). Change it with 'piccolo setup' → Tools → Workspace path guard"})
		}
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
		fm, err := parseFrontmatter(agentMD)
		if err != nil {
			add(FAIL, "agent %q: %v", a.ID, err)
			continue
		}
		// AGENT.md frontmatter outranks config.json for this agent's model
		// (verified v0.3.1 pkg/agent/instance.go:380-392), so a pin here must
		// resolve too. Piccolo never generates a model key, which makes this a
		// user-written reference — reported, never rewritten.
		if pin := strings.TrimSpace(fm.Model); pin != "" && !knownModels(cfg)[pin] {
			add(FAIL, "agent %q: AGENT.md pins model %q missing from model_list", a.ID, pin)
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

// knownModels is the set of aliases model_list defines.
func knownModels(cfg picoclaw.Config) map[string]bool {
	names := map[string]bool{}
	for _, mdl := range cfg.ModelList {
		names[mdl.ModelName] = true
	}
	return names
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
	names := knownModels(cfg)
	for _, mdl := range cfg.ModelList {
		if len(mdl.APIKeys) == 0 || isPlaceholderKey(mdl.APIKeys[0]) {
			add(WARN, "model %q has a placeholder or empty API key", mdl.ModelName)
		} else {
			add(OK, "model: %s (%s)", mdl.ModelName, mdl.Model)
		}
	}
	// Every model alias config.json references must exist in model_list. Walked
	// per location and reported with it, so a dangling alias says where to fix
	// it — and in document order, because ranging over a map here made the line
	// order vary between runs.
	type modelRef struct{ path, alias string }
	var refs []modelRef
	addRef := func(path, alias string) {
		if a := strings.TrimSpace(alias); a != "" {
			refs = append(refs, modelRef{path, a})
		}
	}
	d := cfg.Agents.Defaults
	addRef("agents.defaults.model_name", d.ModelName)
	for i, f := range d.ModelFallbacks {
		addRef(fmt.Sprintf("agents.defaults.model_fallbacks[%d]", i), f)
	}
	addRef("agents.defaults.image_model", d.ImageModel)
	for i, f := range d.ImageModelFallbacks {
		addRef(fmt.Sprintf("agents.defaults.image_model_fallbacks[%d]", i), f)
	}
	for _, a := range cfg.Agents.List {
		label := "agents.list[" + a.ID + "]"
		if a.Model != nil {
			// Both spellings of the field — bare alias string or
			// {primary,fallbacks} — are named by the field path, matching how
			// internal/setup reports a reference.
			addRef(label+".model", a.Model.Primary)
			for i, f := range a.Model.Fallbacks {
				addRef(fmt.Sprintf("%s.model.fallbacks[%d]", label, i), f)
			}
		}
		if a.Subagents != nil && a.Subagents.Model != nil {
			addRef(label+".subagents.model", a.Subagents.Model.Primary)
			for i, f := range a.Subagents.Model.Fallbacks {
				addRef(fmt.Sprintf("%s.subagents.model.fallbacks[%d]", label, i), f)
			}
		}
	}
	for _, r := range refs {
		if !names[r.alias] {
			add(FAIL, "%s references model %q missing from model_list", r.path, r.alias)
		}
	}
	if d.ModelName != "" {
		add(OK, "default model: %s", d.ModelName)
	}
	return results
}

// checkLegacyAgentModelNames reports the key Piccolo itself used to write on
// agents.list entries. PicoClaw reads "model" there and ignores "model_name"
// (verified v0.3.1 pkg/config/config.go:313-321, pkg/agent/instance.go:388), so
// such an entry silently keeps the default model. Nothing can repair that
// automatically — the value may name a model that does not exist — so it is
// reported, never rewritten.
func checkLegacyAgentModelNames(configData []byte) []Result {
	var raw struct {
		Agents struct {
			List []struct {
				ID        string `json:"id"`
				ModelName string `json:"model_name"`
				Model     any    `json:"model"`
			} `json:"list"`
		} `json:"agents"`
	}
	var results []Result
	if err := json.Unmarshal(configData, &raw); err != nil {
		return nil
	}
	for _, a := range raw.Agents.List {
		if strings.TrimSpace(a.ModelName) == "" {
			continue
		}
		suffix := ""
		if a.Model != nil {
			suffix = fmt.Sprintf(" (its model field already says %v and wins)", a.Model)
		}
		results = append(results, Result{
			Status: WARN,
			Message: fmt.Sprintf(
				"agent %q sets model_name %q, which PicoClaw ignores on an agents.list entry — the field is model%s",
				a.ID, a.ModelName, suffix),
		})
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

// checkRunningImage compares the image the container serving traffic was created
// from against the image piccolo.yaml names, and warns when they differ.
//
// No other check in doctor can see this gap, and it is not cosmetic.
// `piccolo integration add`/`upgrade` bakes a NEW image and rewrites container.image,
// but the running gateway keeps the OLD binaries until a restart — while config.json,
// bind-mounted, is re-read immediately. The combination that breaks is the ordinary
// one: enable an integration's tool, forget the restart. The failure is then total,
// because an MCP server baked only into the new image is absent from the running one,
// and PicoClaw fails every chat message rather than only the integration's requests.
// That is exactly how this was observed: piccolo-gws 0.3.4 still serving, the manifest
// naming the 0.3.5 image, and `invalid character 'C' looking for beginning of value`
// on each turn.
func checkRunningImage(ctx context.Context, engine Engine, m *manifest.Manifest) []Result {
	if engine == nil || m == nil {
		return nil
	}
	state, err := engine.StateByName(ctx, m.Container.Name)
	if err != nil || !state.Found || state.Image == "" {
		// No daemon, or no container: the image-store and stale-container checks
		// already speak to that, and inventing a second complaint about the same
		// absent container would double-report one condition.
		return nil
	}
	if !state.Running {
		// A stopped container is a leftover, and the stale-container warning covers it
		// with the right verb; "the running gateway holds old binaries" would be a lie.
		return nil
	}
	if state.Image == m.Container.Image {
		return []Result{{Status: OK, Message: fmt.Sprintf("running container image: %s", state.Image)}}
	}
	verb := "serves"
	if !state.Running {
		verb = "was created from"
	}
	return []Result{{Status: WARN, Message: fmt.Sprintf("container %q %s image %q, but piccolo.yaml names %q — the running gateway holds the OLD integration binaries while config.json is re-read immediately; run 'piccolo restart' before enabling anything the new image provides", m.Container.Name, verb, state.Image, m.Container.Image)}}
}
