package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/kimus/piccolo/internal/docker"
	"github.com/kimus/piccolo/internal/manifest"
	"github.com/kimus/piccolo/internal/picoclaw"
	"github.com/kimus/piccolo/internal/workspace"
)

// stopGrace is the wait for a natural exit before forcing container stop.
const stopGrace = 10 * time.Second

func newServeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Run the PicoClaw gateway container (foreground)",
		RunE: func(cmd *cobra.Command, args []string) error {
			code, err := runServe(cmd)
			if code != 0 && err == nil {
				err = fmt.Errorf("gateway exited with code %d", code)
			}
			if err != nil {
				cmd.SilenceErrors = false
				return err
			}
			return nil
		},
	}
}

func runServe(cmd *cobra.Command) (int, error) {
	out := cmd.OutOrStdout()
	dir, err := workspace.Require(dirFlag)
	if err != nil {
		return 0, err
	}

	// Preflight: daemon reachable + both config files present/parseable.
	runner := docker.New()
	ctx := cmd.Context()
	pingCtx, cancelPing := context.WithTimeout(ctx, runner.ProbeTimeout)
	defer cancelPing()
	if err := runner.Ping(pingCtx); err != nil {
		return 0, fmt.Errorf("docker daemon not reachable: %w", err)
	}

	m, err := manifest.Load(filepath.Join(dir, workspace.ManifestName))
	if err != nil {
		return 0, err
	}
	cfgData, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		return 0, fmt.Errorf("read config.json: %w (run 'piccolo init' first?)", err)
	}
	var cfg picoclaw.Config
	if err := json.Unmarshal(cfgData, &cfg); err != nil {
		return 0, fmt.Errorf("config.json does not parse: %w", err)
	}
	if cfg.Version != picoclaw.SchemaVersion {
		return 0, fmt.Errorf("config.json schema version %d (want %d)", cfg.Version, picoclaw.SchemaVersion)
	}

	env, err := m.ResolveEnv(os.LookupEnv)
	if err != nil {
		var ref *manifest.ErrPlaceholderEnv
		if errors.As(err, &ref) {
			return 0, fmt.Errorf("%s — set the host variable or edit piccolo.yaml", ref)
		}
		return 0, err
	}

	spec := docker.Spec{
		Name:    m.Container.Name,
		Image:   m.Container.Image,
		Network: m.Container.Network,
		Source:  dir,
		Ports:   m.Container.Ports,
		Env:     env,
		Labels:  map[string]string{docker.LabelWorkspace: dir},
	}

	fmt.Fprintf(out, "Starting PicoClaw gateway container %q from workspace %s ...\n", spec.Name, dir)

	proc, err := runner.Run(ctx, spec)
	if err != nil {
		return 0, err
	}

	// Pump stdio both ways.
	pumpDone := make(chan struct{})
	go func() {
		defer close(pumpDone)
		go func() { _, _ = io.Copy(os.Stdout, proc.Stdout()) }()
		go func() { _, _ = io.Copy(os.Stderr, proc.Stderr()) }()
		_, _ = io.Copy(proc.Stdin(), os.Stdin)
		_ = proc.Stdin().Close()
	}()

	// Host signals trigger a graceful stop.
	sigCtx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()

	waitCtx, cancelWait := context.WithCancel(ctx)
	defer cancelWait()

	exitCode := 0
	exited := make(chan struct{})
	go func() {
		defer close(exited)
		code, err := proc.Wait(waitCtx)
		if err == nil {
			exitCode = code
		}
	}()

	select {
	case <-exited:
		// Container exited naturally; clean it up.
	case <-sigCtx.Done():
		fmt.Fprintln(out, "\nStopping gateway container ...")
		cancelWait()
	}

	if err := proc.Shutdown(stopGrace); err != nil {
		fmt.Fprintf(os.Stderr, "warning: container cleanup: %v\n", err)
	}
	if code := proc.ExitCode(); code > 0 {
		exitCode = code
	}
	<-pumpDone
	return exitCode, nil
}
