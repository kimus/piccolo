package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/kimus/piccolo/internal/configdoc"
	"github.com/kimus/piccolo/internal/docker"
	"github.com/kimus/piccolo/internal/manifest"
	"github.com/kimus/piccolo/internal/picoclaw"
	"github.com/kimus/piccolo/internal/runtimeimage"
	"github.com/kimus/piccolo/internal/runtimeplan"
	"github.com/kimus/piccolo/internal/workspace"
)

// stopGrace is the wait for a graceful container stop before SIGKILL.
const stopGrace = 10 * time.Second

// healthTimeout bounds the wait for the gateway to become healthy.
const healthTimeout = 90 * time.Second

func newServeCmd() *cobra.Command {
	var foreground bool
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the PicoClaw gateway container in the background",
		Long: "Starts the gateway container detached, publishes the health port on " +
			"127.0.0.1, and waits until /health answers. Use --foreground to attach " +
			"stdio and stop the container with Ctrl-C instead.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if foreground {
				return runServeForeground(cmd)
			}
			return runServeDetached(cmd)
		},
	}
	cmd.Flags().BoolVarP(&foreground, "foreground", "f", false, "attach stdio in the foreground (Ctrl-C stops the container)")
	return cmd
}

// serveContext holds everything the serve paths share after preflight.
type serveContext struct {
	runner       *docker.Runner
	spec         docker.Spec
	health       string // host-side health URL, e.g. http://127.0.0.1:18790/health
	gwPort       int
	webURL       string // non-empty in launcher/web mode
	dir          string
	layout       workspace.Layout
	manifestPath string
	stdout       io.Writer
	manifest     *manifest.Manifest
	toolchain    manifest.Toolchain
	swap         *gatewaySwap
}

// preflight resolves the workspace, checks the daemon, and composes the
// launch spec (including the auto-published health port).
func preflight(cmd *cobra.Command) (*serveContext, error) {
	out := cmd.OutOrStdout()
	dir, err := workspace.Require(dirFlag)
	if err != nil {
		return nil, err
	}
	// Credential delivery is checked from the workspace alone, before the daemon
	// ping. It needs nothing from Docker, and running it after the ping meant a
	// user with an undecryptable workspace and a stopped daemon was told about the
	// daemon — the one thing that is not their problem here.
	if err := checkCredentialDelivery(dir); err != nil {
		return nil, err
	}

	runner := docker.New()
	ctx := cmd.Context()
	pingCtx, cancelPing := context.WithTimeout(ctx, runner.ProbeTimeout)
	defer cancelPing()
	if err := runner.Ping(pingCtx); err != nil {
		return nil, fmt.Errorf("docker daemon not reachable: %w", err)
	}

	layout := workspace.NewLayout(dir)
	m, err := manifest.Load(layout.Manifest())
	if err != nil {
		return nil, err
	}
	if workspace.IsLegacyFlat(dir) {
		return nil, fmt.Errorf("legacy flat workspace layout detected — run 'piccolo workspace migrate' first")
	}
	if err := layout.EnsureDirs(); err != nil {
		return nil, err
	}
	cfgData, err := os.ReadFile(layout.Config())
	if err != nil {
		return nil, fmt.Errorf("read config.json: %w (run 'piccolo init' first?)", err)
	}
	var cfg picoclaw.Config
	if err := json.Unmarshal(cfgData, &cfg); err != nil {
		return nil, fmt.Errorf("config.json does not parse: %w", err)
	}
	if cfg.Version != picoclaw.SchemaVersion {
		return nil, fmt.Errorf("config.json schema version %d (want %d)", cfg.Version, picoclaw.SchemaVersion)
	}
	// PicoClaw merges picoclaw/.security.yml over config.json during the same load,
	// and a sidecar it cannot parse fails that load outright — the gateway would
	// start, answer /health at worst, and never serve a turn. Catch it here rather
	// than letting the health gate time out on a mistake in a file serve never read.
	if sec := picoclaw.LoadSecrets(layout.Config()); sec.Err != nil {
		return nil, fmt.Errorf("picoclaw/%s does not parse: %w — PicoClaw will not load config.json while it cannot read that file",
			picoclaw.SecurityFileName, sec.Err)
	}
	// An enc:// credential is unusable without both halves of its unlock: the
	// passphrase and the exact key file whose bytes feed the derivation. PicoClaw
	// aborts the config load on either, so the container exits and the health gate
	// reports a failure naming config.json while the missing thing is a host
	// variable or a file on the host. Checked here, before any container is created,
	// because serve parks the running gateway later in this routine and a candidate
	// that cannot decrypt should never have been allowed to displace it.
	env, err := m.ResolveEnv(os.LookupEnv)
	if err != nil {
		var ref *manifest.ErrPlaceholderEnv
		if errors.As(err, &ref) {
			return nil, fmt.Errorf("%s — set the host variable or edit piccolo.yaml", ref)
		}
		return nil, err
	}

	gwPort := cfg.HealthPort()
	ports := composePorts(m, gwPort)
	// Publish the gateway/health port on loopback so host-side checks and
	// status can reach it, and bind the gateway to 0.0.0.0 INSIDE the
	// container — a process on the container's lo interface is unreachable
	// through published ports. Skipped for host networking (the default
	// localhost bind is already reachable from the host, and publishing is
	// a no-op).
	if m.Container.Network != "host" {
		env = withGatewayHost(env)
	}
	// Credential delivery for an encrypted workspace. PicoClaw needs BOTH the
	// passphrase and the key location: the derivation hashes the key file's bytes,
	// so having one without the other is a gateway that exits at startup. Nothing
	// is injected for a workspace that never opted in. The passphrase is injected
	// here because preflight already proved it is set; passing the manifest value
	// keeps one source of truth rather than a second lookup.
	if m.Passphrase() != "" {
		env = withCredentialEnv(env, layout, m.Passphrase())
	}

	volumes := []string{layout.IntegrationBind()}
	if runtimeimage.IsLocalRuntimeImage(m.Container.Image) {
		// Only the media mirror is bound. The toolchain is image content, and
		// integration bundles unpack their own /nix/store closure: binding a
		// host cache over /nix hid the content the
		// image baked and made its own binaries unexecutable.
		volumes = append(volumes, layout.RuntimeBinds()...)
		volumes = append(volumes, agentMediaMirrorMounts(layout, cfg)...)
	}

	spec := docker.Spec{
		Name:    m.Container.Name,
		Image:   m.Container.Image,
		Network: m.Container.Network,
		Source:  layout.PicoclawHome(),
		Ports:   ports,
		Env:     env,
		Volumes: volumes,
		Labels:  map[string]string{docker.LabelWorkspace: dir},
	}

	webURL := webConsoleURL(m)

	return &serveContext{
		runner:       runner,
		spec:         spec,
		health:       fmt.Sprintf("http://127.0.0.1:%d/health", gwPort),
		gwPort:       gwPort,
		webURL:       webURL,
		dir:          dir,
		layout:       layout,
		manifestPath: layout.Manifest(),
		stdout:       out,
		manifest:     m,
		toolchain:    effectiveToolchain(m),
	}, nil
}

// composePorts returns the published port bindings for the container: the
// manifest's own ports (which include the web console's :18800 binding only
// when the console is enabled) plus the auto-published gateway/health port.
// Publishing is skipped for host networking, where the container's localhost
// bind is already reachable from the host.
func composePorts(m *manifest.Manifest, gwPort int) []string {
	ports := append([]string(nil), m.Container.Ports...)
	if m.Container.Network == "host" {
		return ports
	}
	if !hasContainerPort(ports, gwPort) {
		ports = append(ports, fmt.Sprintf("127.0.0.1:%d:%d", gwPort, gwPort))
	}
	return ports
}

// credentialEnvVars are the two PicoClaw reads to unlock enc:// credentials.
const (
	passphraseEnvVar = "PICOCLAW_KEY_PASSPHRASE"
	keyPathEnvVar    = "PICOCLAW_SSH_KEY_PATH"
)

// countEncryptedCredentials counts enc:// values in the raw config bytes. Done on
// the document rather than picoclaw.Config because channel_list is user-owned and
// absent from the typed struct, and a channel token is exactly as undecryptable as
// a model key.
func countEncryptedCredentials(configData []byte) int {
	doc, err := configdoc.Parse(configData)
	if err != nil {
		return 0 // preflight already refused unparseable config.json
	}
	return encryptedCredentialCount(doc)
}

// withCredentialEnv delivers the passphrase and names the key's in-container
// path for a workspace whose config.json holds enc:// values.
//
// The key path is set explicitly rather than left to PicoClaw's own search because
// that search targets ~/.ssh, and inside this container that is image content:
// HOME=/root, Piccolo never sets PICOCLAW_HOME, and nothing mounts /root/.ssh. A
// key generated there would vanish with the container and every credential would
// become undecryptable on the next restart. The keys directory lives under the
// already-mounted picoclaw/ home and is named through PICOCLAW_SSH_KEY_PATH, the
// one location rule PicoClaw accepts for a path outside ~/.ssh.
//
// Like withGatewayHost, a value the user set in piccolo.yaml wins: an explicit
// container.env entry is respected rather than overwritten.
func withCredentialEnv(env []string, layout workspace.Layout, passphrase string) []string {
	env = appendEnvUnlessSet(env, passphraseEnvVar, passphrase)
	return appendEnvUnlessSet(env, keyPathEnvVar, layout.ContainerKeyFile())
}

// appendEnvUnlessSet adds KEY=value when no earlier entry already sets KEY. Order
// matters for the container runtime, so the entries stay in the order appended.
func appendEnvUnlessSet(env []string, key, value string) []string {
	if value == "" {
		return env
	}
	prefix := key + "="
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			return env
		}
	}
	return append(env, prefix+value)
}

// withGatewayHost appends PICOCLAW_GATEWAY_HOST=0.0.0.0 unless the user
// already set that variable in piccolo.yaml. The gateway defaults to
// localhost, which published ports cannot reach inside the container.
func withGatewayHost(env []string) []string {
	for _, kv := range env {
		if strings.HasPrefix(kv, "PICOCLAW_GATEWAY_HOST=") {
			return env
		}
	}
	return append(env, "PICOCLAW_GATEWAY_HOST=0.0.0.0")
}

// hasContainerPort reports whether any published binding already maps the
// given container port.
func agentMediaMirrorMounts(layout workspace.Layout, cfg picoclaw.Config) []string {
	seen := map[string]bool{}
	var mounts []string
	defaultWS := cfg.Agents.Defaults.Workspace
	for _, agent := range cfg.Agents.List {
		ws := agent.Workspace
		if ws == "" {
			ws = defaultWS
		}
		containerWS, ok := containerWorkspacePath(ws)
		if !ok {
			continue
		}
		target := strings.TrimRight(containerWS, "/") + "/media"
		if seen[target] {
			continue
		}
		seen[target] = true
		mounts = append(mounts, layout.RuntimeMedia()+":"+target+":rw")
	}
	return mounts
}

func containerWorkspacePath(path string) (string, bool) {
	p := strings.TrimSpace(path)
	if rest, ok := strings.CutPrefix(p, "~/.picoclaw/"); ok {
		return "/root/.picoclaw/" + rest, true
	}
	if strings.HasPrefix(p, "/root/.picoclaw/") {
		return p, true
	}
	return "", false
}

func effectiveToolchain(m *manifest.Manifest) manifest.Toolchain {
	return runtimeplan.Toolchain(m)
}

func hasContainerPort(ports []string, containerPort int) bool {
	want := fmt.Sprintf(":%d", containerPort)
	for _, p := range ports {
		portion := p
		if i := strings.LastIndex(portion, "/"); i >= 0 {
			portion = portion[:i]
		}
		if strings.HasSuffix(portion, want) {
			return true
		}
	}
	return false
}

// webConsoleURL detects the launcher flavor via a published 18800 binding.
func webConsoleURL(m *manifest.Manifest) string {
	for _, p := range m.Container.Ports {
		parts := strings.Split(strings.TrimSuffix(p, "/tcp"), ":")
		if len(parts) >= 2 && parts[len(parts)-1] == "18800" {
			host := "127.0.0.1"
			hostPort := parts[0]
			if len(parts) == 3 {
				host, hostPort = parts[0], parts[1]
				if host == "" || host == "0.0.0.0" {
					host = "127.0.0.1"
				}
			}
			return fmt.Sprintf("http://%s:%s", host, hostPort)
		}
	}
	return ""
}

func runServeDetached(cmd *cobra.Command) error {
	sc, err := preflight(cmd)
	if err != nil {
		return err
	}
	ctx := cmd.Context()
	sc.swap = newGatewaySwap(sc.runner, sc.stdout, sc.spec.Name)

	// Repair a gateway left mid-promotion by an interrupted previous run before
	// anything else touches the container set.
	if err := sc.swap.recover(ctx); err != nil {
		return err
	}
	// Drift is reported, never repaired here: a config edit must not silently
	// cost minutes of image building.
	warnImageDrift(ctx, sc.stdout, sc.runner, sc.manifest)

	current, err := sc.runner.StateByName(ctx, sc.spec.Name)
	if err != nil {
		return err
	}
	promoting := current.Found && current.Image != sc.spec.Image
	if promoting {
		fmt.Fprintf(sc.stdout, "Promoting the gateway to %s (previous gateway parked as %s) ...\n", sc.spec.Image, sc.swap.previous)
		if err := sc.swap.park(ctx); err != nil {
			return err
		}
	}

	fmt.Fprintf(sc.stdout, "Starting PicoClaw gateway container %q from workspace %s ...\n", sc.spec.Name, sc.dir)
	if _, err := sc.runner.StartDetached(ctx, sc.spec); err != nil {
		return failGatewayStart(cmd, ctx, sc, gateFailure{Stage: "start", Reason: err.Error()})
	}

	if err := waitForHealthy(ctx, sc); err != nil {
		return failGatewayStart(cmd, ctx, sc, gateFailure{Stage: "health", Reason: err.Error()})
	}
	// Readiness is not only a health answer: the gateway must also be able to
	// EXECUTE the integration binaries. Presence checks lie — `command -v
	// piccolo-gws` succeeded in a container where running it reported "not
	// found", because the store paths its interpreter lives in were hidden.
	if details := verifyIntegrationsExecutable(ctx, sc); len(details) > 0 {
		return failGatewayStart(cmd, ctx, sc, gateFailure{
			Stage:   "integration",
			Reason:  "an installed integration is not executable in this gateway",
			Details: details,
		})
	}

	if err := sc.swap.commit(ctx); err != nil {
		fmt.Fprintf(sc.stdout, "  [WARN] %v\n", err)
	}
	if promoting {
		sc.recordPreviousImage(current.Image)
	}

	fmt.Fprintf(sc.stdout, "\n\u2713 piccolo-gateway is running (%s)\n", sc.spec.Name)
	fmt.Fprintf(sc.stdout, "  health: %s\n", sc.health)
	if sc.webURL != "" {
		fmt.Fprintf(sc.stdout, "  web console: %s\n", sc.webURL)
		fmt.Fprintf(sc.stdout, "               first run: open %s/launcher-setup to create the dashboard password\n", sc.webURL)
	}
	fmt.Fprintf(sc.stdout, "\nNext:\n")
	fmt.Fprintf(sc.stdout, "  piccolo chat        interactive terminal chat\n")
	fmt.Fprintf(sc.stdout, "  piccolo chat -m …   one-shot question\n")
	fmt.Fprintf(sc.stdout, "  piccolo logs -f     follow gateway logs\n")
	fmt.Fprintf(sc.stdout, "  piccolo status      container + health state\n")
	fmt.Fprintf(sc.stdout, "  piccolo reload      apply config.json changes in place\n")
	fmt.Fprintf(sc.stdout, "  piccolo restart     restart the container (piccolo.yaml changes)\n")
	fmt.Fprintf(sc.stdout, "  piccolo stop        stop and remove the container\n")
	return nil
}

// waitForHealthy polls the gateway /health endpoint, failing early when the
// container exits.
func waitForHealthy(ctx context.Context, sc *serveContext) error {
	deadline := time.Now().Add(healthTimeout)
	client := &http.Client{Timeout: 2 * time.Second}
	for attempt := 0; ; attempt++ {
		if time.Now().After(deadline) {
			return fmt.Errorf("gateway not healthy after %s (%s did not answer)", healthTimeout, sc.health)
		}
		// Fail early when the container died.
		state, err := sc.runner.StateByName(ctx, sc.spec.Name)
		if err == nil && state.Found && !state.Running {
			return fmt.Errorf("container exited early (status %q, exit code %d)", state.Status, state.ExitCode)
		}
		if attempt%10 == 0 {
			fmt.Fprintf(sc.stdout, "  waiting for %s ...\n", sc.health)
		}
		resp, err := client.Get(sc.health)
		if err == nil {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				if msg := healthSummary(body); msg != "" {
					fmt.Fprintf(sc.stdout, "  %s\n", msg)
				}
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

// healthSummary extracts a short status message from the /health JSON body.
func healthSummary(body []byte) string {
	var payload struct {
		Status string `json:"status"`
	}
	if json.Unmarshal(body, &payload) == nil && payload.Status != "" {
		return "health: " + payload.Status
	}
	return ""
}

// failGatewayStart reports a gateway that did not come up. When a promotion
// parked a revert target, the previous gateway is restored — but only after the
// failed attempt's diagnostics are printed, because the removed container is
// otherwise the only record of why the promote failed.
func failGatewayStart(cmd *cobra.Command, ctx context.Context, sc *serveContext, failure gateFailure) error {
	cmd.SilenceErrors = true
	if sc.swap != nil && sc.swap.parked() {
		restored, err := sc.swap.revert(ctx, failure, func(ctx context.Context, tail string) string {
			return captureLogs(ctx, sc, tail)
		})
		if err != nil {
			// Both the new and the previous gateway are down. Saying that plainly
			// is better than a "rolled back" line that is not true.
			return err
		}
		fmt.Fprintf(sc.stdout, "\n\u2713 previous gateway restored (%s) — the promoted image was not adopted\n", restored)
		fmt.Fprintf(sc.stdout, "  Inspect: piccolo logs    Rebuild the runtime image: piccolo image build --set\n")
		return fmt.Errorf("gateway promotion failed")
	}

	// Nothing to revert to: keep the container for inspection, as before.
	fmt.Fprintf(os.Stderr, "\nerror: %v\n\nRecent container logs:\n", failure)
	dumpLogs(ctx, sc, "30")
	fmt.Fprintf(os.Stderr, "\nThe container %q was left in place for debugging.\n"+
		"Inspect: piccolo logs    Retry after fix: piccolo stop && piccolo serve\n", sc.spec.Name)
	return fmt.Errorf("gateway did not become healthy")
}

// captureLogs returns the last lines of the gateway container as a string, for
// embedding in a promotion failure report before the container is removed.
func captureLogs(ctx context.Context, sc *serveContext, tail string) string {
	rc, err := sc.runner.Logs(ctx, sc.spec.Name, false, tail)
	if err != nil {
		return fmt.Sprintf("(logs unavailable: %v)", err)
	}
	defer rc.Close()
	var buf strings.Builder
	_, _ = io.Copy(&buf, rc)
	return buf.String()
}

// execInGateway is the seam for running a command inside the gateway container.
// Production uses the Docker exec API; unit tests substitute a stub so the
// readiness gate is daemon-free.
var execInGateway = func(ctx context.Context, runner *docker.Runner, name string, argv []string) (string, int, error) {
	stdout, stderr, code, err := runtimeExecCapture(ctx, runner, name, argv)
	if err != nil {
		return "", code, err
	}
	return stdout + stderr, code, nil
}

// verifyIntegrationsExecutable runs each installed integration's own status
// command inside the gateway and reports the ones that cannot execute.
//
// Executing, not finding: a PATH lookup passed in a container where the binary
// could not run, because its ELF interpreter lived in store paths hidden by the
// /nix bind. An integration that runs and reports "not configured" is a PASS —
// that is a setup gap, not a broken image. Only the inability to execute is a
// promotion failure.
func verifyIntegrationsExecutable(ctx context.Context, sc *serveContext) []string {
	if sc.manifest == nil {
		return nil
	}
	var failures []string
	for _, item := range sc.manifest.Runtime.Integrations {
		argv := integrationProbeArgv(item)
		if len(argv) == 0 {
			continue
		}
		out, code, err := execInGateway(ctx, sc.runner, sc.spec.Name, argv)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", displayIntegration(item), err))
			continue
		}
		if !probeExecuted(out, code) {
			failures = append(failures, fmt.Sprintf("%s: exit %d — %s (command: %s)", displayIntegration(item), code, firstLine(out), strings.Join(argv, " ")))
		}
	}
	return failures
}

// integrationProbeArgv prefers the integration's recorded status command; a
// source-built entry without one still names its binary, so `describe --json`
// is the fallback probe.
func integrationProbeArgv(item manifest.Integration) []string {
	if len(item.StatusCommand) > 0 {
		return append([]string(nil), item.StatusCommand...)
	}
	if len(item.DescriptorCommand) > 0 {
		return append([]string(nil), item.DescriptorCommand...)
	}
	if len(item.Commands) > 0 {
		return []string{item.Commands[0], "describe", "--json"}
	}
	return nil
}

// probeExecuted classifies a probe. 126/127 are the shell's "not found" and
// "not executable" codes; docker reports a missing binary as an exec error,
// which the caller already handled. Anything else means the program ran.
// probeExecuted classifies a probe. It answers one question — did the program
// start? — and deliberately does NOT treat a non-zero exit as failure: an
// integration that runs and reports "no credentials" has a setup gap, not a
// broken image, and reverting a promotion over that would be wrong.
func probeExecuted(out string, code int) bool {
	// 126/127 are the shell's "not executable" and "not found". A binary that is
	// missing entirely surfaces as an exec error from the daemon, which the
	// caller already handled.
	if code == 126 || code == 127 {
		return false
	}
	// Silence plus a failure means nothing ran: a working integration binary
	// prints its status payload either way.
	return strings.TrimSpace(out) != "" || code == 0
}

func firstLine(s string) string {
	line := strings.TrimSpace(strings.SplitN(strings.TrimSpace(s), "\n", 2)[0])
	if len(line) > 160 {
		return line[:157] + "..."
	}
	return line
}

// recordPreviousImage keeps the single rollback slot after a completed
// promotion. piccolo.yaml is Piccolo-owned, so this is a state write, not an
// edit of user-owned runtime config.
func (sc *serveContext) recordPreviousImage(image string) {
	if image == "" || image == sc.spec.Image {
		return
	}
	if sc.manifest.Container.PreviousImage == image {
		return
	}
	sc.manifest.Container.PreviousImage = image
	if err := sc.manifest.Write(sc.manifestPath); err != nil {
		fmt.Fprintf(sc.stdout, "  [WARN] could not record the previous image for rollback: %v\n", err)
		return
	}
	fmt.Fprintf(sc.stdout, "  previous image recorded for rollback: %s\n", image)
}

type setupProgressWriter struct{}

// drainPump gives the stdio pump a moment to finish, then stops waiting for it.
// The pump blocks reading os.Stdin and nothing here closes that descriptor, so a
// session whose stdin stays open -- a pipe from `tail -f`, a supervisor, a
// terminal with no Ctrl-D -- could otherwise keep `restart --foreground` running
// forever after the gateway had already been reverted or stopped. A hung
// command after a successful revert is strictly worse than an unflushed line.
func drainPump(done <-chan struct{}) {
	select {
	case <-done:
	case <-time.After(2 * time.Second):
	}
}

// dumpLogs prints the last N log lines of the container to stderr.
func dumpLogs(ctx context.Context, sc *serveContext, tail string) {
	rc, err := sc.runner.Logs(ctx, sc.spec.Name, false, tail)
	if err != nil {
		fmt.Fprintf(os.Stderr, "(logs unavailable: %v)\n", err)
		return
	}
	defer rc.Close()
	_ = docker.Demux(rc, os.Stdout, os.Stderr)
}

// runServeForeground attaches the gateway's stdio to the terminal (Ctrl-C stops
// it). It promotes with the same park / gate / revert sequence as the detached
// path — the gate runs while the container is attached, so a replacement that
// cannot execute its integrations still hands the workspace back to the
// previous gateway instead of ending with nothing running.
func runServeForeground(cmd *cobra.Command) error {
	sc, err := preflight(cmd)
	if err != nil {
		return err
	}
	ctx := cmd.Context()
	sc.swap = newGatewaySwap(sc.runner, sc.stdout, sc.spec.Name)
	if err := sc.swap.recover(ctx); err != nil {
		return err
	}
	warnImageDrift(ctx, sc.stdout, sc.runner, sc.manifest)

	current, err := sc.runner.StateByName(ctx, sc.spec.Name)
	if err != nil {
		return err
	}
	promoting := current.Found && current.Image != sc.spec.Image
	if promoting {
		fmt.Fprintf(sc.stdout, "Promoting the gateway to %s (previous gateway parked as %s) ...\n", sc.spec.Image, sc.swap.previous)
		if err := sc.swap.park(ctx); err != nil {
			return err
		}
	}

	fmt.Fprintf(sc.stdout, "Starting PicoClaw gateway container %q from workspace %s ...\n", sc.spec.Name, sc.dir)
	proc, err := sc.runner.Run(ctx, sc.spec)
	if err != nil {
		if promoting {
			if _, rerr := sc.swap.revert(ctx, gateFailure{Stage: "start", Reason: err.Error()}, func(context.Context, string) string { return "" }); rerr != nil {
				return rerr
			}
			fmt.Fprintf(sc.stdout, "\n\u2713 previous gateway restored (%s) — the promoted image was not adopted\n", current.Image)
		}
		return err
	}

	pumpDone := make(chan struct{})
	go func() {
		defer close(pumpDone)
		go func() { _, _ = io.Copy(os.Stdout, proc.Stdout()) }()
		go func() { _, _ = io.Copy(os.Stderr, proc.Stderr()) }()
		_, _ = io.Copy(proc.Stdin(), os.Stdin)
		_ = proc.Stdin().Close()
	}()

	sigCtx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()

	waitCtx, cancelWait := context.WithCancel(ctx)
	defer cancelWait()

	exitCode := 0
	exited := make(chan struct{})
	go func() {
		defer close(exited)
		if code, err := proc.Wait(waitCtx); err == nil {
			exitCode = code
		}
	}()

	// Readiness gate, running while stdio is attached.
	gated := make(chan gateFailure, 1)
	go func() {
		if err := waitForHealthy(ctx, sc); err != nil {
			gated <- gateFailure{Stage: "health", Reason: err.Error()}
			return
		}
		if details := verifyIntegrationsExecutable(ctx, sc); len(details) > 0 {
			gated <- gateFailure{Stage: "integration", Reason: "an installed integration is not executable in this gateway", Details: details}
			return
		}
		gated <- gateFailure{Stage: "ok"}
	}()

	select {
	case <-exited:
	case <-sigCtx.Done():
		fmt.Fprintln(sc.stdout, "\nStopping gateway container ...")
		cancelWait()
	case failure := <-gated:
		if failure.Stage == "ok" {
			if err := sc.swap.commit(ctx); err != nil {
				fmt.Fprintf(sc.stdout, "  [WARN] %v\n", err)
			}
			if promoting {
				sc.recordPreviousImage(current.Image)
			}
			fmt.Fprintf(sc.stdout, "\n\u2713 piccolo-gateway is running (%s)\n", sc.spec.Name)
			// Keep the container running: the user is attached, so wait for the
			// exit or a signal like before.
			select {
			case <-exited:
			case <-sigCtx.Done():
				fmt.Fprintln(sc.stdout, "\nStopping gateway container ...")
				cancelWait()
			}
			break
		}
		// Deliberately NOT proc.Shutdown here: Shutdown removes the container,
		// and revert prints the candidate's logs before deleting it. Stopping it
		// first would leave the failure report saying "logs unavailable" and
		// destroy the only evidence of why the promotion failed. revert removes
		// the candidate itself, after capturing.
		restored, rerr := sc.swap.revert(ctx, failure, func(ctx context.Context, tail string) string {
			return captureLogs(ctx, sc, tail)
		})
		if rerr != nil {
			return rerr
		}
		fmt.Fprintf(sc.stdout, "\n\u2713 previous gateway restored (%s) — the promoted image was not adopted\n", restored)
		drainPump(pumpDone)
		cmd.SilenceErrors = true
		return fmt.Errorf("gateway promotion failed")
	}

	if err := proc.Shutdown(stopGrace); err != nil {
		fmt.Fprintf(os.Stderr, "warning: container cleanup: %v\n", err)
	}
	if code := proc.ExitCode(); code > 0 {
		exitCode = code
	}
	drainPump(pumpDone)
	if exitCode != 0 {
		cmd.SilenceErrors = true
		os.Exit(exitCode)
	}
	return nil
}

// checkCredentialDelivery refuses a workspace whose enc:// credentials cannot be
// unlocked: no passphrase recorded, or no key file to derive with. Purely
// workspace-local, so it can run before Docker is even asked about it.
func checkCredentialDelivery(dir string) error {
	layout := workspace.NewLayout(dir)
	m, err := manifest.Load(layout.Manifest())
	if err != nil {
		return err
	}
	cfgData, err := os.ReadFile(layout.Config())
	if err != nil {
		return nil // the existing preflight read reports this with the right message
	}
	n := countEncryptedCredentials(cfgData)
	if n == 0 {
		return nil
	}
	if m.Passphrase() == "" {
		return fmt.Errorf("config.json holds %d enc:// credential(s) but piccolo.yaml has no secrets.passphrase, so nothing can decrypt them — set the field, or rewrite the credentials with 'piccolo secrets encrypt'", n)
	}
	if _, err := os.Stat(layout.KeyFile()); err != nil {
		return fmt.Errorf("config.json holds %d enc:// credential(s) but the key file %s is missing or unreadable — PicoClaw derives its AES key from that file's bytes, so nothing can decrypt", n, rel(dir, layout.KeyFile()))
	}
	return nil
}
