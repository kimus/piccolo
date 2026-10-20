package doctor

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/kimus/piccolo/internal/docker"
	"github.com/kimus/piccolo/internal/manifest"
	"github.com/kimus/piccolo/internal/picoclaw"
	"github.com/kimus/piccolo/internal/workspace"
)

// Run executes every check and returns the results in report order: grouped by
// section, sections in the order Sections() declares.
//
// The shape is three phases. (1) Read the workspace's own files: nothing can be
// said without the manifest, and these are the fail-fast gates. (2) Ask the
// daemon everything it can answer, concurrently, in ONE snapshot. (3) Run the
// checks as pure functions over that data.
//
// A daemon that will not answer is a finding, not a reason to stop: the previous
// behaviour printed `docker daemon not reachable` and nothing else, which hid the
// useful half of the answer — whether the workspace itself is intact. That is the
// difference between "start Docker" and "start Docker, and by the way your
// research agent's AGENT.md is missing".
func Run(ctx context.Context, opts Options) []Result {
	return GroupBySection(run(ctx, opts))
}

func run(ctx context.Context, opts Options) []Result {
	var results []Result
	// add records one finding. hint carries the remedy or the consequence and may
	// be empty; it is separate from message so the renderer can put the actionable
	// half on its own line instead of appending it to a 300-character sentence.
	add := func(sec Section, s Status, message, hint string) {
		results = append(results, Result{Status: s, Section: sec, Message: message, Hint: hint})
	}

	// The daemon ping needs nothing from the workspace, so it runs while the files
	// are read instead of after them. It is still REPORTED even when a workspace
	// gate below returns early: the answer was asked for, and a user cannot tell
	// "Docker is fine" from "we never looked".
	ping := make(chan error, 1)
	if opts.Engine != nil {
		go func() { ping <- opts.Engine.Ping(ctx) }()
	}
	awaitDaemon := func() error {
		if opts.Engine == nil {
			return nil
		}
		err := <-ping
		if err != nil {
			add(SecEnvironment, FAIL, fmt.Sprintf("docker daemon not reachable: %v", err),
				"workspace and config checks below still ran; every Docker check was skipped")
			return err
		}
		add(SecEnvironment, OK, "docker daemon reachable", "")
		return nil
	}

	// 1. Workspace + manifest. These are read while the ping is in flight because
	// the snapshot needs both the image name and the container name out of
	// piccolo.yaml, and a workspace without one has nothing for the daemon to be
	// checked against.
	if _, err := os.Stat(opts.Workspace); err != nil {
		add(SecWorkspace, FAIL, fmt.Sprintf("workspace missing: %s", opts.Workspace), "")
		awaitDaemon()
		return results
	}
	add(SecWorkspace, OK, fmt.Sprintf("workspace: %s", opts.Workspace), "")

	manifestPath := filepath.Join(opts.Workspace, "piccolo.yaml")
	m, err := manifest.Load(manifestPath)
	if err != nil {
		add(SecWorkspace, FAIL, fmt.Sprintf("manifest: %v", err), "")
		awaitDaemon()
		return results
	}
	add(SecWorkspace, OK, "manifest: piccolo.yaml", "")

	// 2. One concurrent read of everything else the daemon can say. When there is
	// no Engine (offline mode: tests, serve's config-only preflight) or the daemon
	// did not answer, live is false and every engine-dependent check stays silent
	// rather than reporting an absence it never observed.
	pingErr := awaitDaemon()
	facts := fetchDockerFacts(ctx, opts.Engine, m, pingErr)

	// 3. Layout.
	if workspace.IsLegacyFlat(opts.Workspace) {
		add(SecWorkspace, FAIL, "workspace layout: legacy flat layout detected", "run 'piccolo workspace migrate'")
	} else if _, err := os.Stat(layoutOf(opts.Workspace).PicoclawHome()); err != nil {
		add(SecWorkspace, FAIL, "workspace layout: missing picoclaw/ directory", "run 'piccolo init' or 'piccolo workspace migrate'")
	} else {
		missing := []string{}
		for _, dir := range []struct{ label, path string }{
			{"integrations/", layoutOf(opts.Workspace).Integrations()},
			{"runtime/media/", layoutOf(opts.Workspace).RuntimeMedia()},
		} {
			if st, err := os.Stat(dir.path); err != nil || !st.IsDir() {
				missing = append(missing, dir.label)
			}
		}
		if len(missing) > 0 {
			add(SecWorkspace, WARN, fmt.Sprintf("workspace layout: missing %s", strings.Join(missing, ", ")),
				"run 'piccolo workspace migrate --yes' or 'piccolo serve' to create runtime dirs")
		} else {
			add(SecWorkspace, OK, "workspace layout: structured", "")
		}
	}

	results = append(results, tag(SecWorkspace, checkGeneratedProvenance(opts.Workspace))...)
	results = append(results, tag(SecWorkspace, checkGitignoreHygiene(opts.Workspace))...)

	// 4. Image presence (warning only), then currency and retention. All three read
	// the snapshot; imagePresent is threaded through because when the image is
	// simply absent, the presence check owns that message and repeating it as a
	// content complaint would be two warnings for one problem.
	imagePresent := true
	if facts.live {
		imagePresent = facts.imagePresent && facts.imageErr == nil
		switch {
		case facts.imageErr != nil:
			add(SecEnvironment, WARN, fmt.Sprintf("image check failed for %s: %v", m.Container.Image, facts.imageErr), "")
		case !facts.imagePresent:
			if isPiccoloLocalRuntimeImage(m.Container.Image) {
				add(SecEnvironment, WARN, fmt.Sprintf("local Piccolo runtime image not present: %s", m.Container.Image),
					"build it with 'piccolo image build --set'")
			} else {
				add(SecEnvironment, WARN, fmt.Sprintf("image not present locally: %s", m.Container.Image),
					"'piccolo serve' pulls it once, on first start")
			}
		default:
			add(SecEnvironment, OK, fmt.Sprintf("image present: %s", m.Container.Image), "")
		}
	}

	// Runtime image currency. Doctor reports the drift and names the rebuild; it
	// never starts one, because adopting a rebuild is a user decision.
	results = append(results, tag(SecEnvironment, checkImageDrift(ctx, facts, m, imagePresent))...)
	results = append(results, tag(SecEnvironment, checkImageRetention(facts, m))...)

	// An interrupted promotion leaves the previous gateway parked under the
	// reserved name. The canonical name always belongs to what should be running,
	// so this is the one abnormal state to look for.
	if facts.live && facts.parkedExists && facts.parkedErr == nil {
		parked := docker.PreviousName(m.Container.Name)
		add(SecEnvironment, WARN, fmt.Sprintf("parked promotion target %q exists", parked),
			"a previous restart was interrupted; 'piccolo serve' restores it")
	}

	results = append(results, tag(SecCapabilities, checkWebConsole(m))...)
	results = append(results, tag(SecEnvironment, checkRuntimeToolchain(m))...)
	results = append(results, tag(SecCapabilities, checkIntegrations(m))...)

	// 5. Env references.
	lookup := opts.LookupEnv
	if lookup == nil {
		lookup = os.LookupEnv
	}
	for _, ref := range m.EnvRefs() {
		if _, found := lookup(ref); !found {
			add(SecWorkspace, WARN, fmt.Sprintf("piccolo.yaml env reference %s: host variable not set", "env:"+ref),
				"'piccolo serve' cannot resolve it and will stop before starting the container")
		}
	}

	// 6. Container state. A stale container is one that exists but is NOT running:
	// a running container built from the current image is the service itself, not a
	// leftover, and calling it stale made every healthy workspace carry a permanent
	// false warning. A running container built from a DIFFERENT image is reported by
	// checkRunningImage with the accurate verb (restart), so it is not
	// double-reported here.
	if facts.live {
		if facts.containerErr == nil && facts.containerExists && !(facts.stateErr == nil && facts.state.Found && facts.state.Running) {
			add(SecEnvironment, WARN, fmt.Sprintf("stale container %q exists", m.Container.Name),
				"'piccolo serve' replaces it; 'piccolo stop' removes it")
		}
		results = append(results, tag(SecEnvironment, checkRunningImage(m, facts))...)
		// One-shot setup/status containers are reclaimed automatically before
		// the next run, but a leftover means a previous attempt was interrupted.
		if facts.oneShotsErr == nil && len(facts.oneShots) > 0 {
			add(SecEnvironment, WARN, fmt.Sprintf("%d abandoned integration one-shot container(s): %s", len(facts.oneShots), strings.Join(facts.oneShots, ", ")),
				"removed automatically on the next setup/status run")
		}
	}

	// 7. PicoClaw config.
	layout := layoutOf(opts.Workspace)
	configPath := layout.Config()
	data, err := os.ReadFile(configPath)
	if err != nil {
		add(SecAssistant, FAIL, fmt.Sprintf("picoclaw config: %v", err), "run 'piccolo init', or 'piccolo workspace migrate' for a flat-layout workspace")
		return results
	}
	var cfg picoclaw.Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		add(SecAssistant, FAIL, fmt.Sprintf("config.json does not parse: %v", err), "")
		return results
	}
	if cfg.Version != picoclaw.SchemaVersion {
		add(SecAssistant, FAIL, fmt.Sprintf("config.json schema version %d (want %d)", cfg.Version, picoclaw.SchemaVersion), "")
		return results
	}
	add(SecAssistant, OK, fmt.Sprintf("picoclaw config: config.json (schema v%d)", cfg.Version), "")

	// PicoClaw's secret sidecar. Anything the gateway has saved once (the web
	// console, `picoclaw model add`, a version migration) moves every secure field
	// out of config.json and into picoclaw/.security.yml, and that file then WINS
	// on load — so config.json alone cannot say what the gateway will actually
	// use. Read it before the checks that depend on it. A file that does not parse
	// is fatal: PicoClaw's LoadConfig refuses to start the gateway over it.
	secrets := picoclaw.LoadSecrets(configPath)
	if secrets.Err != nil {
		add(SecCredentials, FAIL, fmt.Sprintf("picoclaw/.security.yml does not parse: %v", secrets.Err),
			"PicoClaw will not load config.json while it cannot read the sidecar, so the gateway will not start")
		secrets = picoclaw.Secrets{Present: true}
	} else if secrets.Present {
		add(SecCredentials, OK, "secret sidecar: picoclaw/.security.yml",
			"secure fields here override config.json")
	}

	// Credential encryption. An enc:// value is only as good as the two things
	// that unlock it, and both are checked before the gateway ever starts: a
	// passphrase that reaches the container, and the exact key file whose bytes feed
	// the derivation. Neither failure is visible from config.json, and both end the
	// same way — the gateway exits on a config error that names config.json.
	results = append(results, tag(SecCredentials, checkCredentialEncryption(opts.Workspace, configPath, data, m, secrets))...)

	// 8. Agents, models, and tool posture.
	results = append(results, tag(SecAssistant, checkAgents(opts.Workspace, cfg))...)
	results = append(results, tag(SecAssistant, checkModels(cfg, secrets))...)
	results = append(results, tag(SecAssistant, checkLegacyAgentModelNames(data))...)

	// Image understanding. PicoClaw can receive channel attachments; Piccolo
	// recommends routing them through a vision-capable image_model and the
	// load_image tool.
	results = append(results, tag(SecAssistant, checkImageUnderstanding(opts.Workspace, cfg, data))...)
	results = append(results, tag(SecCredentials, checkExecSafety(data))...)

	// 9. Channels (raw parse — setup manages channel_list; unknown fields
	// are user-owned and ignored here), the console's chat, then MCP reachability:
	// a registered integration server is not a callable tool.
	results = append(results, tag(SecCapabilities, checkChannels(data, secrets))...)
	results = append(results, tag(SecCapabilities, checkConsoleChat(m, data, secrets))...)
	results = append(results, tag(SecCapabilities, checkIntegrationRegistration(m, data))...)
	results = append(results, tag(SecCapabilities, checkOrphanMCPServers(m, data))...)
	results = append(results, tag(SecCapabilities, checkMCPAccess(opts.Workspace, cfg, data))...)

	return results
}

// layoutOf keeps the layout resolution in one call: it is derived from the
// workspace dir, and repeating the constructor at six sites invites one of them
// to drift.
func layoutOf(dir string) workspace.Layout {
	return workspace.NewLayout(dir)
}

// tag stamps sec onto every result that did not declare a section of its own.
// Tagging at the helper boundary rather than at each of its ~90 add sites keeps
// a check's grouping in one place; a helper that genuinely spans two sections
// sets Section itself and is left alone here.
func tag(sec Section, results []Result) []Result {
	for i := range results {
		if results[i].Section == "" {
			results[i].Section = sec
		}
	}
	return results
}
