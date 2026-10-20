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

	"github.com/kimus/piccolo/internal/configdoc"
	"github.com/kimus/piccolo/internal/credential"
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

// String keeps `%v` on a Result readable: without it an int type prints 0/1/2 and
// every test failure that dumps a result list loses the severity it is showing.
func (s Status) String() string { return s.Marker() }

// Word is the severity in the vocabulary the styler colours and the JSON report
// emits: "ok", "warn", "fail".
func (s Status) Word() string {
	switch s {
	case OK:
		return "ok"
	case WARN:
		return "warn"
	default:
		return "fail"
	}
}

// Marker is the plain-text severity marker, one bracketed word with no padding.
// It lives here rather than at the call sites because the renderer, the styler,
// and anyone grepping a CI log all have to agree on these exact strings: a second
// copy of the vocabulary is how `[FAIL]` and `[fail]` end up in one report.
func (s Status) Marker() string {
	switch s {
	case OK:
		return "[OK]"
	case WARN:
		return "[WARN]"
	default:
		return "[FAIL]"
	}
}

// Result is one check outcome.
type Result struct {
	Status Status
	// Section is the group this result belongs to in the rendered report. It is
	// set by the check that produced the result, never inferred from the message
	// text: a renderer that classifies by substring silently loses every line it
	// cannot spell, and a lost FAIL is worse than an ugly one.
	Section Section
	// Message is the finding. It states the condition, not the remedy.
	Message string
	// Hint is the remedy or the consequence, rendered on its own muted line.
	// Kept out of Message so a finding that runs 300 characters does not wrap
	// into an unreadable block, and so "what is wrong" stays scannable.
	// Optional.
	Hint string
}

// Text is the whole finding — condition plus remedy — for callers that search
// results without rendering them (tests, grep-like helpers).
func (r Result) Text() string {
	if r.Hint == "" {
		return r.Message
	}
	return r.Message + " \u2014 " + r.Hint
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

// GroupBySection reorders results into report order: grouped by section, the
// declared section order, and stable within a section so the checks keep the
// order they were written in. Two consequences worth having: the rendered report
// reads as a checklist of the parts of a setup rather than one long list, and a
// caller that only wants the problems can take a whole section at a time.
//
// A result with an unknown or empty section sorts last rather than being dropped:
// a new check that forgot to declare a group must still be reported.
func GroupBySection(results []Result) []Result {
	order := Sections()
	rank := make(map[Section]int, len(order)+1)
	for i, sec := range order {
		rank[sec] = i
	}
	last := len(order)
	key := func(s Section) int {
		if i, ok := rank[s]; ok {
			return i
		}
		return last
	}
	out := append([]Result(nil), results...)
	sort.SliceStable(out, func(i, j int) bool {
		return key(out[i].Section) < key(out[j].Section)
	})
	return out
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
		results = append(results, Result{Status: WARN,
			Message: fmt.Sprintf("integration %s is installed and configured but never registered: config.json has no tools.mcp.servers.%s entry", label, key),
			Hint:    fmt.Sprintf("its tools are not offered to any agent; finish it with 'piccolo integration setup %s' and accept the config change", ref)})
	}
	return results
}

// checkOrphanMCPServers reports the mirror image of checkIntegrationRegistration: a
// servers.<key> entry in config.json that NO installed integration claims. That is what
// a half-finished uninstall leaves, and it is the more dangerous of the two states,
// because an orphan is not inert — with tools.mcp.enabled true PicoClaw tries to start
// every listed server, and a server whose command no longer exists in the image fails
// the whole turn, so EVERY chat message errors, not only the removed integration's.
//
// Warning rather than failure for one reason: the entry might belong to a server the
// user registered by hand, which is legal and which Piccolo must not call a mistake.
// The consequence is therefore stated in the message instead of asserted by the exit
// code.
func checkOrphanMCPServers(m *manifest.Manifest, configData []byte) []Result {
	var raw struct {
		Tools struct {
			MCP struct {
				Enabled *bool                      `json:"enabled"`
				Servers map[string]json.RawMessage `json:"servers"`
			} `json:"mcp"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(configData, &raw); err != nil {
		return nil
	}
	if len(raw.Tools.MCP.Servers) == 0 {
		return nil
	}
	claimed := map[string]bool{}
	if m != nil {
		for _, in := range m.Runtime.Integrations {
			if k := strings.TrimSpace(in.MCPServerKey); k != "" {
				claimed[k] = true
			}
		}
	}
	keys := make([]string, 0, len(raw.Tools.MCP.Servers))
	for k := range raw.Tools.MCP.Servers {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var results []Result
	enabled := raw.Tools.MCP.Enabled != nil && *raw.Tools.MCP.Enabled
	for _, key := range keys {
		if claimed[key] {
			continue
		}
		if enabled {
			results = append(results, Result{Status: WARN,
				Message: fmt.Sprintf("config.json registers tools.mcp.servers.%s but no installed integration provides it", key),
				Hint:    "tools.mcp.enabled is on, so the gateway will try to start it and every chat turn fails if it cannot — remove the entry (or reinstall the integration with 'piccolo integration add <package>')"})
			continue
		}
		results = append(results, Result{Status: WARN,
			Message: fmt.Sprintf("config.json registers tools.mcp.servers.%s but no installed integration provides it", key),
			Hint:    "the entry is inert while tools.mcp.enabled is off"})
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
		results = append(results, Result{Status: WARN,
			Message: fmt.Sprintf("mcp: server %s is registered but disabled", strings.Join(disabled, ", ")),
			Hint:    "its tools are not offered to any agent",
		})
	}
	if len(registered) == 0 {
		return results
	}
	names := strings.Join(registered, ", ")

	if raw.Tools.MCP.Enabled == nil || !*raw.Tools.MCP.Enabled {
		results = append(results, Result{Status: WARN,
			Message: fmt.Sprintf("mcp: tools.mcp.enabled is not true while server(s) {%s} are registered", names),
			Hint:    "PicoClaw skips MCP initialization entirely, so agents are handed none of these integrations' tools even though their binaries run; enable it with 'piccolo setup' \u2192 Tools \u2192 MCP tool servers",
		})
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
			results = append(results, Result{Status: WARN,
				Message: fmt.Sprintf("mcp: coordinator %q declares an AGENT.md tools: allowlist", a.ID),
				Hint:    fmt.Sprintf("PicoClaw matches tool names exactly and MCP tools are named mcp_<server>_<tool> at runtime, so this agent cannot receive any tool from {%s}; Piccolo generates the coordinator with no tools: line, and deleting that line grants it every tool config.json enables", names),
			})
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
func checkImageDrift(ctx context.Context, facts dockerFacts, m *manifest.Manifest, imagePresent bool) []Result {
	if !facts.live || !isPiccoloLocalRuntimeImage(m.Container.Image) || !imagePresent {
		return nil
	}
	info, err := runtimeplan.Check(ctx, facts, m)
	if err != nil {
		return []Result{{Status: WARN, Message: fmt.Sprintf("runtime image currency: could not verify (%v)", err), Hint: "an unverifiable check never blocks serve"}}
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
		return []Result{{Status: OK, Message: fmt.Sprintf("runtime image content matches this workspace (%s)", info.Current), Hint: fmt.Sprintf("the tag predates a Piccolo recipe change (%s); no rebuild needed", info.Desired)}}
	}
	return []Result{{
		Status: WARN,
		Message: fmt.Sprintf(
			"runtime image is stale: %s, wants %s (%s)",
			info.Current, info.Desired, strings.Join(info.Reasons, "; ")),
		Hint: "rebuild with 'piccolo image build --set && piccolo restart'; Piccolo never rebuilds implicitly",
	}}
}

// checkImageRetention reports locally built runtime images that nothing
// references any more. It is a warning with a count, a size, and the command:
// deleting images on the user's behalf is never doctor's job, and the rollback
// target in particular is the one image nothing else can recover.
func checkImageRetention(facts dockerFacts, m *manifest.Manifest) []Result {
	if !facts.live {
		return nil
	}
	if facts.imagesErr != nil {
		return []Result{{Status: WARN, Message: fmt.Sprintf("runtime image retention: could not check (%v)", facts.imagesErr)}}
	}
	if facts.containerImagesErr != nil {
		return []Result{{Status: WARN, Message: fmt.Sprintf("runtime image retention: could not check (%v)", facts.containerImagesErr)}}
	}
	images, containers := facts.images, facts.containerImages
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
		return []Result{{Status: OK, Message: fmt.Sprintf("runtime image retention: %d unreferenced image(s) (%s)", len(plan.Removable), humanBytes(plan.Reclaimable)), Hint: "below the reporting threshold"}}
	}
	return []Result{{Status: WARN, Message: fmt.Sprintf(
		"runtime image retention: %d unreferenced runtime image(s), up to %s",
		len(plan.Removable), humanBytes(plan.Reclaimable)),
		Hint: "reclaim with 'piccolo image prune' (keeps the image in use and the rollback target); Piccolo never deletes images on its own"}}
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
		add(WARN, "generated-file provenance: unreadable %s: %v", generate.LockFileName, err)
		return results
	}
	if prov == nil {
		// A workspace created before the record was renamed has a perfectly good
		// piccolo.provenance.json sitting next to a missing piccolo.lock, and the
		// plain "no record" line would tell that user their provenance never existed
		// when it plainly does. Say which file was found, so the remedy is obvious
		// rather than a mystery to debug.
		if _, err := os.Stat(filepath.Join(dir, generate.LegacyFileName)); err == nil {
			results = append(results, Result{Status: OK,
				Message: fmt.Sprintf("generated-file provenance: record found under the old name %s", generate.LegacyFileName),
				Hint:    fmt.Sprintf("'piccolo setup' rewrites it as %s on the next change", generate.LockFileName),
			})
			return results
		}
		results = append(results, Result{Status: OK,
			Message: "generated-file provenance: no record (workspace generated before provenance existed)",
			Hint:    "'piccolo setup' writes it on the next change"})
		return results
	}

	var customised, stale, missing, current int
	for _, rel := range prov.Paths() {
		data, readErr := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
		if readErr != nil {
			missing++
			results = append(results, Result{Status: WARN,
				Message: fmt.Sprintf("generated file is missing: %s (source %s)", rel, prov.Source(rel)),
				Hint:    fmt.Sprintf("restore it or delete its entry from %s", generate.LockFileName),
			})
			continue
		}
		rendered, _ := generate.CurrentRender(prov.Source(rel))
		switch prov.Classify(rel, data, rendered) {
		case generate.StatusStale:
			stale++
			results = append(results, Result{Status: WARN,
				Message: fmt.Sprintf("generated file predates the current template: %s", rel),
				Hint:    fmt.Sprintf("refresh with 'piccolo setup' (fix belongs in %s, not in the copy)", prov.Source(rel)),
			})
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
	return append(results, Result{Status: WARN,
		Message: fmt.Sprintf("workspace .gitignore is missing %s", strings.Join(quoted, ", ")),
		Hint:    "run 'piccolo init' here (it appends missing entries and never overwrites your files) or add them by hand"})
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
		results = append(results, Result{Status: WARN,
			Message: "exec: tools.exec.allow_remote is true",
			Hint:    "remote chats can request shell commands inside the container subject to PicoClaw safety checks; enable only for trusted channels/users"})
	}
	// Absent means restricted; only an explicit false clears it. Piccolo generates
	// false, so this fires on a workspace hand-hardened or created by an older
	// Piccolo, and names the guard the agent will keep tripping.
	restricted := raw.Agents.Defaults.RestrictToWorkspace == nil || *raw.Agents.Defaults.RestrictToWorkspace
	if restricted {
		if raw.Tools.Exec.Enabled != nil && *raw.Tools.Exec.Enabled {
			results = append(results, Result{Status: WARN,
				Message: "exec: agents.defaults.restrict_to_workspace is true while tools.exec is enabled",
				Hint:    "PicoClaw blocks shell commands naming any path outside the agent's own workspace (\"Command blocked by safety guard (path outside working dir)\"), and allow_read_outside_workspace does NOT relax that exec check; turn it off with 'piccolo setup' → Tools → Workspace path guard unless you want the sandbox"})
		} else {
			results = append(results, Result{Status: WARN,
				Message: "agents.defaults.restrict_to_workspace is true",
				Hint:    "write/edit/append stay confined to each agent workspace (Piccolo now generates false; the container is the boundary). Change it with 'piccolo setup' → Tools → Workspace path guard"})
		}
	}
	return results
}

// checkChannels inspects channel_list entries for the classic half-configured
// state: enabled channel without a real token (empty/placeholder). A token in
// picoclaw/.security.yml counts, and the `[NOT_HERE]` sentinel in config.json is
// PicoClaw saying exactly that — it is not a placeholder to warn about.
//
// A DISABLED channel is never a finding. Upstream skips `!bc.Enabled` before a
// channel is constructed and before its HTTP handler is mounted
// (pkg/channels/manager.go:1074,1128,1136), so an off entry binds nothing and
// dials nothing — it is the absence of a configuration, not a broken one. What
// doctor does separate is *why* it is off: an entry that still holds a
// credential was configured and switched off by someone, while an empty one is
// almost certainly PicoClaw's own default (defaultChannels in
// pkg/config/defaults.go, merged into any file it loads and re-serialized on any
// save). Those two get different lines, and the empty ones get ONE line between
// them rather than fifteen, because they cannot be cleaned up — deleting them is
// undone by the next save through the gateway.
func checkChannels(configData []byte, secrets picoclaw.Secrets) []Result {
	var results []Result
	add := func(s Status, format string, args ...any) {
		results = append(results, Result{Status: s, Message: fmt.Sprintf(format, args...)})
	}
	list, ok := parseChannelList(configData)
	if !ok {
		return results // absent/other shape: not a failure, config is user-owned
	}
	if len(list) == 0 {
		return results
	}
	sidecar := sidecarCredentials(secrets)

	names := make([]string, 0, len(list))
	for n := range list {
		names = append(names, n)
	}
	sort.Strings(names)
	var stubs []string
	for _, name := range names {
		ch := list[name]
		if !ch.Enabled {
			cred := channelCredential(name, ch, sidecar)
			if !cred.Found {
				stubs = append(stubs, name)
				continue
			}
			// Parked, not stranded: the credential is there because turning the
			// channel back on is meant to work. Not a warning — and Piccolo cannot
			// fix it anyway, since .security.yml is PicoClaw's file to write.
			switch {
			case cred.InSidecar && cred.InJSON:
				add(OK, "channel: %s (disabled; credential parked in config.json and picoclaw/.security.yml)", name)
			case cred.InSidecar:
				add(OK, "channel: %s (disabled; credential parked in picoclaw/.security.yml)", name)
			default:
				add(OK, "channel: %s (disabled; credential parked in config.json)", name)
			}
			continue
		}
		tokenKey := ""
		for _, k := range picoclaw.SecretFieldName {
			if _, ok := ch.Settings[k]; ok {
				tokenKey = k
				break
			}
		}
		// config.json may carry the sentinel instead of the value, and the
		// sidecar may hold the value for a field config.json never names — PicoClaw
		// saved a token for a channel whose settings it stripped.
		if tokenKey == "" {
			for _, k := range picoclaw.SecretFieldName {
				if sc := secrets.ChannelSecret(name, k); sc.Matched {
					tokenKey = k
					break
				}
			}
		}
		if tokenKey == "" {
			add(WARN, "channel %q is enabled but has no token in settings", name)
			continue
		}
		tok, _ := ch.Settings[tokenKey].(string)
		// The question is which FILE governs this field, not which one looks better.
		// A sidecar that names the field at all replaces config.json's value for it —
		// including with an empty string, which upstream loads as an empty token while
		// config.json still displays a real one. Confirmed by driving the real loader
		// over each shape. Values are never printed: only the field name.
		sc := secrets.ChannelSecret(name, tokenKey)
		jsonUsable := picoclaw.HasUsableKey(tok)
		switch {
		case sc.Matched && !picoclaw.HasUsableKey(sc.Value) && jsonUsable:
			// The inverse of the shadow, and worse to debug: the channel looks keyed in
			// config.json and the gateway loads an empty token.
			add(WARN, "channel %q: config.json carries a token but picoclaw/.security.yml sets %q empty, so the gateway starts with no token", name, tokenKey)
			continue
		case sc.Matched && picoclaw.HasUsableKey(sc.Value) && jsonUsable && sc.Value != tok:
			results = append(results, Result{Status: WARN,
				Message: fmt.Sprintf("channel %q: config.json %q is ignored", name, tokenKey),
				Hint:    "picoclaw/.security.yml sets that field, so change the token there",
			})
			continue
		case sc.Matched && !picoclaw.HasUsableKey(sc.Value):
			// Sidecar governs and carries nothing usable, with nothing behind it in
			// config.json either.
			add(WARN, "channel %q is enabled but picoclaw/.security.yml %q is empty or a placeholder", name, tokenKey)
			continue
		case jsonUsable:
			// config.json decides: no sidecar value for this field.
		case picoclaw.HasUsableKey(sc.Value):
			// The value lives only in the sidecar; the OK line says so below.
		default:
			if picoclaw.IsSidecarSentinel(tok) {
				results = append(results, Result{Status: WARN,
					Message: fmt.Sprintf("channel %q: config.json marks %q as stored in picoclaw/.security.yml, but that file carries no usable value", name, tokenKey),
					Hint:    "the gateway starts with no token",
				})
			} else {
				add(WARN, "channel %q is enabled but %q is empty or a placeholder", name, tokenKey)
			}
			continue
		}
		if len(ch.AllowFrom) == 0 {
			results = append(results, Result{Status: WARN,
				Message: fmt.Sprintf("channel %q: allow_from is empty", name),
				Hint:    "PicoClaw accepts messages from ANYONE (set user IDs or use \"*\" to acknowledge)",
			})
			continue
		}
		if len(ch.AllowFrom) == 1 && ch.AllowFrom[0] == "*" {
			add(WARN, "channel %q: allow_from is \"*\" — open to everyone who can reach the bot", name)
			continue
		}
		if sc.Matched && !jsonUsable {
			add(OK, "channel: %s (token from picoclaw/.security.yml)", name)
		} else {
			add(OK, "channel: %s", name)
		}
	}
	if len(stubs) > 0 {
		// One line for all of them. Naming each individually would imply each is a
		// thing to look at, and the list is long precisely because it is upstream's
		// defaults rather than anyone's choices.
		results = append(results, Result{Status: OK,
			Message: fmt.Sprintf("channels: %d disabled with no credential anywhere (%s)", len(stubs), strings.Join(stubs, ", ")),
			Hint:    "PicoClaw writes a default entry for every channel type it knows, so these are not yours to clean up",
		})
	}
	// The mirror of the orphan model-key rule, and the state that rule cannot see:
	// a sidecar credential for a channel config.json never lists. PicoClaw merges
	// the sidecar over config.json per channel and CREATES the entry when it is
	// missing (ChannelsConfig.UnmarshalYAML), so deleting a channel from
	// config.json leaves its credential behind in a file Piccolo will not touch.
	for _, cred := range secrets.ChannelsWithUsableSecrets() {
		if _, present := list[cred.Channel]; present {
			continue
		}
		results = append(results, Result{Status: WARN, Section: SecCredentials,
			Message: fmt.Sprintf("picoclaw/.security.yml channel %q holds %s but config.json lists no such channel", cred.Channel, fieldList(cred.Fields)),
			Hint:    "PicoClaw re-adds the entry (disabled) when it merges the sidecar, so its credential survives the deletion",
		})
	}
	return results
}

// channelEntry is the part of a channel_list entry doctor reads. The document is
// parsed raw because channel_list is user-owned and absent from picoclaw.Config:
// a field Piccolo has never seen must not fail a check.
type channelEntry struct {
	Enabled   bool           `json:"enabled"`
	Type      string         `json:"type"`
	AllowFrom []string       `json:"allow_from"`
	Settings  map[string]any `json:"settings"`
}

// parseChannelList returns config.json's channel_list. The bool is false only
// when the document does not parse or the key is absent, which every caller
// reads as "no channel findings", never as "no channels".
func parseChannelList(configData []byte) (map[string]channelEntry, bool) {
	var raw struct {
		ChannelList map[string]channelEntry `json:"channel_list"`
	}
	if err := json.Unmarshal(configData, &raw); err != nil || raw.ChannelList == nil {
		return nil, false
	}
	return raw.ChannelList, true
}

// sidecarCredentials indexes the sidecar's channels that hold a usable value, by
// channel name. Channels the sidecar lists with nothing in them are absent from
// this map, which is what tells a parked credential apart from upstream noise.
func sidecarCredentials(secrets picoclaw.Secrets) map[string]picoclaw.ChannelCredential {
	out := map[string]picoclaw.ChannelCredential{}
	for _, cred := range secrets.ChannelsWithUsableSecrets() {
		out[cred.Channel] = cred
	}
	return out
}

// channelCred reports whether a channel entry has a usable credential in
// either file, and in which ones. It returns field names and file names, never
// values: an enabled-channel warning that carried a token would leak the secret
// into whatever the user pastes doctor output into.
type channelCred struct {
	Found     bool
	Field     string // one secure field name that carries a value
	InJSON    bool   // config.json settings hold it
	InSidecar bool   // picoclaw/.security.yml holds it
}

func channelCredential(name string, ch channelEntry, sidecar map[string]picoclaw.ChannelCredential) channelCred {
	var cred channelCred
	for _, k := range picoclaw.SecretFieldName {
		if tok, ok := ch.Settings[k].(string); ok && picoclaw.HasUsableKey(tok) {
			cred.Found, cred.Field, cred.InJSON = true, k, true
			break
		}
	}
	if sc, ok := sidecar[name]; ok {
		cred.Found, cred.InSidecar = true, true
		if cred.Field == "" && len(sc.Fields) > 0 {
			cred.Field = sc.Fields[0]
		}
	}
	return cred
}

// picoChannelType is the PicoClaw channel the launcher's dashboard chat rides
// on. Verified against v0.3.1: the launcher reverse-proxies /pico/ws to the
// gateway (web/backend/api/pico.go:26), the gateway's /pico/ handler is the pico
// channel itself (pkg/channels/pico/pico.go:277), and a channel mounts its
// handler only when it is enabled and decodes with a token
// (pkg/channels/manager.go:1001,1040,1074,1128).
const picoChannelType = "pico"

// checkConsoleChat covers the one console state the dashboard cannot report for
// itself: it opens, the login works, and the chat connects to nothing — because
// the channel that serves its websocket is off or keyless while everything Piccolo
// can see about the console (port published, launcher image, gateway healthy)
// still reads fine.
//
// A workspace with NO pico entry is deliberately silent. The launcher creates and
// enables one itself on startup (EnsurePicoChannel, web/backend/main.go:651), so
// warning about its absence would warn about a first-run state that fixes itself.
func checkConsoleChat(m *manifest.Manifest, configData []byte, secrets picoclaw.Secrets) []Result {
	var results []Result
	add := func(s Status, format string, args ...any) {
		results = append(results, Result{Status: s, Message: fmt.Sprintf(format, args...)})
	}
	// Console off is clean and never warned; a :18800 bound to a non-launcher
	// image is already a dead-port warning from checkWebConsole, and adding a
	// second finding about a chat that cannot exist would double-report one fault.
	if !publishesContainerPort(m.Container.Ports, 18800) || !isLauncherImage(m.Container.Image) {
		return results
	}
	list, ok := parseChannelList(configData)
	if !ok || len(list) == 0 {
		return results
	}
	name, ch, found := findPicoChannel(list)
	if !found {
		return results
	}
	switch cred := channelCredential(name, ch, sidecarCredentials(secrets)); {
	case !ch.Enabled:
		results = append(results, Result{Status: WARN,
			Message: fmt.Sprintf("web console chat: channel %q is disabled, so the gateway mounts no /pico/ handler", name),
			Hint:    "the dashboard opens and logs in but cannot chat; enable it ('piccolo setup' → Channels), or restart the console, whose launcher re-enables it and rewrites config.json",
		})
	case !cred.Found:
		results = append(results, Result{Status: WARN,
			Message: fmt.Sprintf("web console chat: channel %q is enabled but holds no token in config.json or picoclaw/.security.yml", name),
			Hint:    "PicoClaw never starts it and the dashboard cannot chat; set one with 'piccolo setup' → Channels",
		})
	default:
		add(OK, "web console chat: channel %q enabled", name)
	}
	return results
}

// findPicoChannel locates the pico-typed entry. Upstream resolves the type from
// `type` and falls back to the map key (pkg/channels/manager.go:1070), so the
// same rule is used here, and pico is a singleton upstream — the first match in
// sorted order is deterministic if that is ever violated.
func findPicoChannel(list map[string]channelEntry) (string, channelEntry, bool) {
	names := make([]string, 0, len(list))
	for name := range list {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		ch := list[name]
		typeName := ch.Type
		if typeName == "" {
			typeName = name
		}
		if typeName == picoChannelType {
			return name, ch, true
		}
	}
	return "", channelEntry{}, false
}

// fieldList names secure fields for a report. It is given names, never values.
func fieldList(fields []string) string {
	switch len(fields) {
	case 0:
		return "a credential"
	case 1:
		return fmt.Sprintf("%q", fields[0])
	default:
		quoted := make([]string, len(fields))
		for i, f := range fields {
			quoted[i] = fmt.Sprintf("%q", f)
		}
		return strings.Join(quoted, ", ")
	}
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
		results = append(results, Result{Status: WARN,
			Message: fmt.Sprintf(
				"piccolo.yaml publishes :18800 (web console) but image %q is not the launcher image", m.Container.Image),
			Hint: "the port would have nothing listening; use the launcher image or remove the binding"})
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

// checkModels reports the models the gateway will actually run with, which is
// not always what config.json shows: picoclaw/.security.yml overrides a
// model_list entry's api_keys whenever it carries a matching key, and it
// overrides in BOTH directions — a sidecar entry with a populated list replaces
// the config.json key (so a `piccolo setup` edit there is a silent no-op), and a
// sidecar entry with an empty list strips a key config.json still displays.
// Values are never printed: only which file decides, and which key matched.
func checkModels(cfg picoclaw.Config, secrets picoclaw.Secrets) []Result {
	var results []Result
	add := func(s Status, format string, args ...any) {
		results = append(results, Result{Status: s, Message: fmt.Sprintf(format, args...)})
	}

	if len(cfg.ModelList) == 0 {
		add(FAIL, "config.json model_list is empty")
		return results
	}
	names := knownModels(cfg)
	shield := secrets.ResolveModels(cfg.ModelAliases())
	for i, mdl := range cfg.ModelList {
		jsonKey := len(mdl.APIKeys) > 0 && picoclaw.HasUsableKey(mdl.APIKeys[0])
		switch {
		case shield[i].Matched && shield[i].HasKeys && jsonKey:
			// Both files carry a key and the sidecar's is the one loaded: the config.json
			// value the user can SEE is dead. Named by its sidecar key, because that is
			// what they have to edit — and without repeating it twice in one line.
			results = append(results, Result{Status: WARN,
				Message: fmt.Sprintf("model %q (%s): config.json api_keys is ignored", mdl.ModelName, mdl.Model),
				Hint:    fmt.Sprintf("picoclaw/.security.yml key %q overrides it, so this value is never loaded; edit the key there, or run 'piccolo secrets encrypt' to drop the dead copy", shield[i].Key),
			})
		case shield[i].Matched && shield[i].HasKeys:
			results = append(results, Result{Status: OK,
				Message: fmt.Sprintf("model: %s (%s)", mdl.ModelName, mdl.Model),
				Hint:    "key from picoclaw/.security.yml",
			})
		case shield[i].Matched && !shield[i].HasKeys && jsonKey:
			add(WARN, "model %q: config.json carries an api key but picoclaw/.security.yml key %s lists none, so the gateway runs this model with no key",
				mdl.ModelName, shield[i].Key)
		case shield[i].Matched && !shield[i].HasKeys:
			add(WARN, "model %q has a placeholder or empty API key", mdl.ModelName)
		case jsonKey:
			add(OK, "model: %s (%s)", mdl.ModelName, mdl.Model)
		default:
			add(WARN, "model %q has a placeholder or empty API key", mdl.ModelName)
		}
	}
	// A sidecar key no model_list entry claims is a stale guard, and the usual way
	// to make one is a rename: the key keeps protecting a model name that no longer
	// exists while the renamed model runs with whatever config.json says. Silent
	// either way, and invisible from config.json alone.
	for _, orphan := range secrets.OrphanKeys(cfg.ModelAliases()) {
		// The subject here is the sidecar entry, not the model, so this finding
		// belongs to the credentials section even though the helper that found it
		// is a model check. tag() fills only undeclared sections, so declaring it
		// here is what moves it.
		results = append(results, Result{Status: WARN, Section: SecCredentials,
			Message: fmt.Sprintf("picoclaw/.security.yml %s matches no model_list entry", orphan),
			Hint:    "a model renamed or deleted in config.json leaves its key behind here",
		})
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
		results = append(results, Result{Status: WARN,
			Message: "image understanding: agents.defaults.image_model is not configured",
			Hint:    "image attachments may be received but not visually interpreted; set one with 'piccolo setup' → Models"})
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
				results = append(results, Result{Status: WARN,
					Message: "image understanding: tools.load_image.enabled is false",
					Hint:    "agents cannot visually inspect local image attachments; enable it with 'piccolo setup' → Tools"})
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
func checkRunningImage(m *manifest.Manifest, facts dockerFacts) []Result {
	if !facts.live || m == nil {
		return nil
	}
	state, err := facts.state, facts.stateErr
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
	return []Result{{Status: WARN, Message: fmt.Sprintf("container %q %s image %q, but piccolo.yaml names %q", m.Container.Name, verb, state.Image, m.Container.Image),
		Hint: "the running gateway holds the OLD integration binaries while config.json is re-read immediately; run 'piccolo restart' before enabling anything the new image provides"}}
}

// checkCredentialEncryption reports whether an encrypted workspace can actually be
// decrypted by the gateway it will start.
//
// Warnings, never failures: unlike an unparseable sidecar, an undeliverable
// passphrase is a state the user is entitled to be in between commands (the
// variable is exported by whoever runs serve, not stored by Piccolo), and making it
// a FAIL would turn every daemon-free `doctor` run in a fresh shell into a red
// board. `serve` is where it becomes fatal, and that guard already exists.
// readableBeyondOwner reports the permission bits a file grants past its owner.
func readableBeyondOwner(path string) (os.FileMode, bool) {
	fi, err := os.Stat(path)
	if err != nil {
		return 0, false
	}
	perm := fi.Mode().Perm()
	return perm, perm&0o077 != 0
}

// relPath keeps a warning readable: an absolute /Users/name/... path in a status line
// says nothing a relative path does not.
func relPath(workspaceDir, path string) string {
	if r, err := filepath.Rel(workspaceDir, path); err == nil {
		return r
	}
	return path
}

// jsonHoldsPlaintextCredential reports whether a config.json-shaped file still carries
// a real credential as readable text.
//
// Field NAMES decide, not value shape: walking every string would flag assistant names
// and model ids. The names come from picoclaw.SecretFieldName, verified against the
// pinned release to appear only on SecureString fields, and picoclaw.HasUsableKey then
// filters out empty and upstream-placeholder values, so an untouched example is not a
// leak. enc:// is skipped by construction — it is the state we are asking to reach.
func jsonHoldsPlaintextCredential(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var root any
	if err := json.Unmarshal(data, &root); err != nil {
		return false
	}
	var found bool
	var walk func(any)
	walk = func(v any) {
		if found {
			return
		}
		switch t := v.(type) {
		case map[string]any:
			for key, child := range t {
				if picoclaw.IsSecretFieldName(key) && plaintextCredential(child) {
					found = true
					return
				}
				walk(child)
			}
		case []any:
			for _, child := range t {
				walk(child)
			}
		}
	}
	walk(root)
	return found
}

// plaintextCredential is true for a non-empty value that is neither ciphertext nor an
// unfilled example.
func plaintextCredential(v any) bool {
	switch t := v.(type) {
	case string:
		return !credential.IsEncrypted(t) && picoclaw.HasUsableKey(t)
	case []any:
		for _, child := range t {
			if c, ok := child.(string); ok && !credential.IsEncrypted(c) && picoclaw.HasUsableKey(c) {
				return true
			}
		}
	}
	return false
}

// configPath2 is the workspace's piccolo.yaml; named to keep the local variable
// configPath (config.json) unambiguous.
func configPath2(workspaceDir string) string {
	return filepath.Join(workspaceDir, workspace.ManifestName)
}

func checkCredentialEncryption(workspaceDir, configPath string, configData []byte, m *manifest.Manifest, secrets picoclaw.Secrets) []Result {
	var results []Result

	// Counted across BOTH files. A credential that lives only in the overlay is
	// still encrypted, and counting config.json alone made an overlay-only
	// workspace look like it stores plaintext — which is worse than a missing
	// warning, because it is a confidently wrong one.
	configJSONCount := countEncryptedValues(configData)
	overlayCount := len(secrets.EncryptedSecureValues())
	n := configJSONCount + overlayCount
	plaintext := secrets.PlaintextSecureValues()
	keyPath := filepath.Join(workspaceDir, workspace.PicoclawDir, workspace.KeysDirName, workspace.KeyFileName)
	_, keyErr := os.Stat(keyPath)

	// config.json holds credentials as readable text whenever the workspace is not
	// encrypted, and config.json.bak holds whatever the version before it held. Both
	// are reported only when they CONTAIN a usable plaintext credential: an encrypted
	// workspace's config.json has nothing left to hide, and a permission warning there
	// would be noise — noise is what gets the real warnings ignored.
	for _, f := range []string{configPath, configPath + ".bak"} {
		if perm, ok := readableBeyondOwner(f); ok && jsonHoldsPlaintextCredential(f) {
			rel := relPath(workspaceDir, f)
			results = append(results, Result{Status: WARN,
				Message: fmt.Sprintf("%s is readable by group/others (%o) and holds a plaintext credential", rel, perm),
				Hint:    fmt.Sprintf("chmod 600 %s, or delete it and run 'piccolo secrets encrypt'", rel),
			})
		}
	}

	// The passphrase lives in piccolo.yaml, so that file's permissions are part of
	// the security story. Reported rather than silently fixed: tightening a file the
	// user owns, on a read-only command like doctor, is a side effect nobody asked
	// for — but leaving it invisible is a claim of protection that is not true.
	if m.Passphrase() != "" {
		if fi, err := os.Stat(configPath2(workspaceDir)); err == nil && fi.Mode().Perm()&0o077 != 0 {
			results = append(results, Result{Status: WARN,
				Message: fmt.Sprintf("piccolo.yaml is readable by group/others (%o) and holds the enc:// passphrase", fi.Mode().Perm()),
				Hint:    fmt.Sprintf("chmod 600 %s", workspace.ManifestName),
			})
		}
	}

	if n == 0 && m.Passphrase() == "" && len(plaintext) == 0 {
		// Silent by design for a workspace that never opted in. It was NOT silent
		// when a passphrase was recorded and no ciphertext existed — that state is
		// "encryption configured, not happening", and it used to fall through here
		// with nothing to say, which is how a plaintext Discord token survived in a
		// workspace whose own piccolo.yaml promised better.
		return results
	}
	if n == 0 && m.Passphrase() != "" {
		results = append(results, Result{Status: WARN,
			Message: "piccolo.yaml records secrets.passphrase but config.json holds no enc:// credential",
			Hint:    fmt.Sprintf("the workspace is configured for encryption and is storing plaintext; run 'piccolo secrets encrypt' to seal it (a missing or deleted %s causes this)", filepath.Join(workspace.PicoclawDir, workspace.KeysDirName, workspace.KeyFileName)),
		})
	}
	if m.Passphrase() == "" {
		results = append(results, Result{Status: WARN,
			Message: fmt.Sprintf("config.json holds %d enc:// credential(s) but piccolo.yaml has no secrets.passphrase", n),
			Hint:    "serve cannot deliver a passphrase and the gateway will not start",
		})
	}
	// Checked whether or not the passphrase is present: a workspace missing both
	// needs two fixes, and reporting only the first would send the user back to
	// doctor after each one.
	if keyErr != nil {
		results = append(results, Result{Status: WARN,
			Message: fmt.Sprintf("%d enc:// credential(s) are held but the key file %s is missing or unreadable", n, workspace.KeysDirName+"/"+workspace.KeyFileName),
			Hint:    "PicoClaw derives its AES key from that file's bytes, so no credential can be decrypted without it",
		})
		return results
	}
	if m.Passphrase() == "" {
		return results
	}
	// The state the heal exists for: the overlay overrides config.json per field, so
	// a plaintext credential anywhere in it means the gateway is loading plaintext
	// no matter what config.json says. Reporting "encrypted" without this would be
	// the false green light in its purest form.
	if len(plaintext) > 0 {
		sort.Strings(plaintext)
		results = append(results, Result{Status: WARN,
			Message: fmt.Sprintf("picoclaw/%s still holds %d plaintext credential value(s) (%s) that override config.json on load", picoclaw.SecurityFileName, len(plaintext), strings.Join(plaintext, ", ")),
			Hint:    "the gateway reads these, not the enc:// values; run 'piccolo secrets encrypt' to seal them",
		})
	}

	// Credentials are counted across both files, and the split is named: with the
	// overlay as the intended home, "in config.json" alone would read as though a
	// model still had a key there.
	switch {
	case overlayCount > 0 && configJSONCount > 0:
		results = append(results, Result{Status: OK,
			Message: fmt.Sprintf("credential encryption: %d enc:// value(s), %d in config.json and %d in picoclaw/%s, passphrase in piccolo.yaml, key present", n, configJSONCount, overlayCount, picoclaw.SecurityFileName),
			Hint:    "note: piccolo.yaml is committed, so a copy of this workspace can still be decrypted",
		})
	case overlayCount > 0:
		results = append(results, Result{Status: OK,
			Message: fmt.Sprintf("credential encryption: %d enc:// value(s), all in picoclaw/%s, passphrase in piccolo.yaml, key present", n, picoclaw.SecurityFileName),
			Hint:    "note: piccolo.yaml is committed, so a copy of this workspace can still be decrypted",
		})
	default:
		results = append(results, Result{Status: OK,
			Message: fmt.Sprintf("credential encryption: %d enc:// value(s) in config.json, passphrase in piccolo.yaml, key present", n),
			Hint:    "note: piccolo.yaml is committed, so a copy of this workspace can still be decrypted",
		})
	}
	return results
}

// countEncryptedValues counts enc:// values across the whole document, not the
// typed config: channel_list is user-owned and absent from picoclaw.Config, and a
// channel token is exactly as undecryptable as a model key.
func countEncryptedValues(configData []byte) int {
	doc, err := configdoc.Parse(configData)
	if err != nil {
		return 0
	}
	n := 0
	var walk func(any)
	walk = func(v any) {
		switch t := v.(type) {
		case string:
			if credential.IsEncrypted(t) {
				n++
			}
		case []configdoc.Value:
			for _, e := range t {
				walk(e)
			}
		case *configdoc.Doc:
			for _, k := range t.Keys() {
				if child, ok := t.Get(k); ok {
					walk(child)
				}
			}
		}
	}
	for _, k := range doc.Keys() {
		if v, ok := doc.Get(k); ok {
			walk(v)
		}
	}
	return n
}
