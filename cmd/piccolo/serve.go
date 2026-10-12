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
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/kimus/piccolo/internal/docker"
	"github.com/kimus/piccolo/internal/manifest"
	"github.com/kimus/piccolo/internal/picoclaw"
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
	runner *docker.Runner
	spec   docker.Spec
	health string // host-side health URL, e.g. http://127.0.0.1:18790/health
	gwPort int
	webURL string // non-empty in launcher/web mode
	dir    string
	stdout io.Writer
}

// preflight resolves the workspace, checks the daemon, and composes the
// launch spec (including the auto-published health port).
func preflight(cmd *cobra.Command) (*serveContext, error) {
	out := cmd.OutOrStdout()
	dir, err := workspace.Require(dirFlag)
	if err != nil {
		return nil, err
	}
	runner := docker.New()
	ctx := cmd.Context()
	pingCtx, cancelPing := context.WithTimeout(ctx, runner.ProbeTimeout)
	defer cancelPing()
	if err := runner.Ping(pingCtx); err != nil {
		return nil, fmt.Errorf("docker daemon not reachable: %w", err)
	}

	m, err := manifest.Load(filepath.Join(dir, workspace.ManifestName))
	if err != nil {
		return nil, err
	}
	cfgData, err := os.ReadFile(filepath.Join(dir, "config.json"))
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

	env, err := m.ResolveEnv(os.LookupEnv)
	if err != nil {
		var ref *manifest.ErrPlaceholderEnv
		if errors.As(err, &ref) {
			return nil, fmt.Errorf("%s — set the host variable or edit piccolo.yaml", ref)
		}
		return nil, err
	}

	gwPort := cfg.HealthPort()
	ports := append([]string(nil), m.Container.Ports...)
	// Publish the gateway/health port on loopback so host-side checks and
	// status can reach it, and bind the gateway to 0.0.0.0 INSIDE the
	// container — a process on the container's lo interface is unreachable
	// through published ports. Skipped for host networking (the default
	// localhost bind is already reachable from the host, and publishing is
	// a no-op).
	if m.Container.Network != "host" {
		if !hasContainerPort(ports, gwPort) {
			ports = append(ports, fmt.Sprintf("127.0.0.1:%d:%d", gwPort, gwPort))
		}
		env = withGatewayHost(env)
	}

	spec := docker.Spec{
		Name:    m.Container.Name,
		Image:   m.Container.Image,
		Network: m.Container.Network,
		Source:  dir,
		Ports:   ports,
		Env:     env,
		Labels:  map[string]string{docker.LabelWorkspace: dir},
	}

	webURL := webConsoleURL(m)

	return &serveContext{
		runner: runner,
		spec:   spec,
		health: fmt.Sprintf("http://127.0.0.1:%d/health", gwPort),
		gwPort: gwPort,
		webURL: webURL,
		dir:    dir,
		stdout: out,
	}, nil
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

	fmt.Fprintf(sc.stdout, "Starting PicoClaw gateway container %q from workspace %s ...\n", sc.spec.Name, sc.dir)
	id, err := sc.runner.StartDetached(ctx, sc.spec)
	if err != nil {
		return err
	}
	_ = id

	if err := waitForHealthy(ctx, sc); err != nil {
		// Dump recent logs to explain the failure, keep the container for
		// inspection (piccolo logs / piccolo stop).
		fmt.Fprintf(os.Stderr, "\nerror: %v\n\nRecent container logs:\n", err)
		dumpLogs(ctx, sc, "30")
		fmt.Fprintf(os.Stderr, "\nThe container %q was left in place for debugging.\n"+
			"Inspect: piccolo logs    Retry after fix: piccolo stop && piccolo serve\n", sc.spec.Name)
		cmd.SilenceErrors = true
		return fmt.Errorf("gateway did not become healthy")
	}

	fmt.Fprintf(sc.stdout, "\n\u2713 piccolo-gateway is running (%s)\n", sc.spec.Name)
	fmt.Fprintf(sc.stdout, "  health: %s\n", sc.health)
	if sc.webURL != "" {
		fmt.Fprintf(sc.stdout, "  web:    %s\n", sc.webURL)
	}
	fmt.Fprintf(sc.stdout, "\nNext:\n")
	fmt.Fprintf(sc.stdout, "  piccolo chat        interactive terminal chat\n")
	fmt.Fprintf(sc.stdout, "  piccolo chat -m …   one-shot question\n")
	fmt.Fprintf(sc.stdout, "  piccolo logs -f     follow gateway logs\n")
	fmt.Fprintf(sc.stdout, "  piccolo status      container + health state\n")
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

func runServeForeground(cmd *cobra.Command) error {
	sc, err := preflight(cmd)
	if err != nil {
		return err
	}
	ctx := cmd.Context()

	fmt.Fprintf(sc.stdout, "Starting PicoClaw gateway container %q from workspace %s ...\n", sc.spec.Name, sc.dir)
	proc, err := sc.runner.Run(ctx, sc.spec)
	if err != nil {
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

	select {
	case <-exited:
	case <-sigCtx.Done():
		fmt.Fprintln(sc.stdout, "\nStopping gateway container ...")
		cancelWait()
	}

	if err := proc.Shutdown(stopGrace); err != nil {
		fmt.Fprintf(os.Stderr, "warning: container cleanup: %v\n", err)
	}
	if code := proc.ExitCode(); code > 0 {
		exitCode = code
	}
	<-pumpDone
	if exitCode != 0 {
		cmd.SilenceErrors = true
		os.Exit(exitCode)
	}
	return nil
}
