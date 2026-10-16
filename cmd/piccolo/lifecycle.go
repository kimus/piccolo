package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/kimus/piccolo/internal/configdoc"
	"github.com/kimus/piccolo/internal/docker"
	"github.com/kimus/piccolo/internal/manifest"
	"github.com/kimus/piccolo/internal/picoclaw"
	"github.com/kimus/piccolo/internal/workspace"
)

func newStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Stop and remove the gateway container",
		RunE: func(cmd *cobra.Command, args []string) error {
			name, _, runner, ctx, err := containerContext(cmd)
			if err != nil {
				return err
			}
			stopCtx, cancel := context.WithTimeout(ctx, stopGrace+20*time.Second)
			defer cancel()
			found, err := runner.StopByName(stopCtx, name, stopGrace)
			if err != nil {
				return err
			}
			if !found {
				fmt.Fprintf(cmd.OutOrStdout(), "gateway container %q is not running\n", name)
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "\u2713 stopped and removed %s\n", name)
			return nil
		},
	}
}

func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show container and gateway health state",
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			name, dir, runner, ctx, err := containerContext(cmd)
			if err != nil {
				return err
			}
			state, err := runner.StateByName(ctx, name)
			if err != nil {
				return err
			}
			if !state.Found {
				fmt.Fprintf(out, "container: %s — not created (run 'piccolo serve')\n", name)
				return nil
			}
			running := "no"
			if state.Running {
				running = "yes"
			}
			fmt.Fprintf(out, "container: %s (%s, running: %s, exit code: %d)\n",
				name, state.Status, running, state.ExitCode)
			if state.Image != "" {
				fmt.Fprintf(out, "image:     %s\n", state.Image)
			}

			// A parked revert target means a previous promotion was interrupted.
			if parked, perr := runner.StateByName(ctx, docker.PreviousName(name)); perr == nil && parked.Found {
				fmt.Fprintf(out, "parked:    %s (%s, image %s) — a promotion was interrupted; 'piccolo serve' restores it\n",
					docker.PreviousName(name), orUnknown(parked.Status), orUnknown(parked.Image))
			}

			if !state.Running {
				return nil
			}
			healthURL, herr := healthURLFor(cmd)
			if herr != nil {
				return herr
			}
			status, body := probeHealth(ctx, healthURL)
			fmt.Fprintf(out, "health:    %s → %s\n", healthURL, status)
			if body != "" {
				fmt.Fprintf(out, "           %s\n", body)
			}
			// Report the web console only when it is enabled (a :18800 binding).
			if m, merr := manifest.Load(workspace.NewLayout(dir).Manifest()); merr == nil {
				if webURL := webConsoleURL(m); webURL != "" {
					fmt.Fprintf(out, "web:       %s\n", webURL)
				}
				// Staleness is reported here too: status is where a user looks when
				// something is not as expected.
				warnImageDrift(ctx, out, runner, m)
				// The image the running container was created from can differ from the one
				// piccolo.yaml names: `integration add`/`upgrade` bakes a new image and
				// rewrites the manifest, but only a restart swaps the running binaries —
				// while bind-mounted config.json is re-read immediately. Worth a line here,
				// because the symptom a user sees is chat failing, not an image error.
				if state.Image != "" && state.Image != m.Container.Image {
					fmt.Fprintf(out, "stale:     container runs %s but piccolo.yaml names %s — run 'piccolo restart' to swap the binaries\n",
						state.Image, m.Container.Image)
				}
				if m.Container.PreviousImage != "" {
					fmt.Fprintf(out, "rollback:  %s (piccolo integration rollback)\n", m.Container.PreviousImage)
				}
				// A passphrase the running container never received is a gateway that
				// could not decrypt its own config, and the symptom is chat failing, not
				// an image error — same reasoning as the stale line above. Only reported
				// when the workspace is encrypted at all.
				// The enc:// half of the posture lives in the workspace, so it can be
				// checked from the workspace alone: a config.json with ciphertext and
				// no passphrase (or no key file) is a gateway that will not boot.
				if credentialDeliveryGap(dir, m) {
					fmt.Fprintln(out, "encrypt:   config.json holds enc:// credentials but piccolo.yaml has no secrets.passphrase — the gateway will refuse to start")
				}
				if keyGap := credentialKeyGap(dir, m); keyGap != "" {
					fmt.Fprintf(out, "encrypt:   %s\n", keyGap)
				}
			}
			return nil
		},
	}
}

func newLogsCmd() *cobra.Command {
	var follow bool
	var tail string
	cmd := &cobra.Command{
		Use:   "logs",
		Short: "Show gateway container logs",
		RunE: func(cmd *cobra.Command, args []string) error {
			name, _, runner, ctx, err := containerContext(cmd)
			if err != nil {
				return err
			}
			rc, err := runner.Logs(ctx, name, follow, tail)
			if err != nil {
				return err
			}
			defer rc.Close()
			if follow {
				// Stop streaming on Ctrl-C.
				streamCtx, stop := signalContext()
				defer stop()
				done := make(chan error, 1)
				go func() { done <- docker.Demux(rc, os.Stdout, os.Stderr) }()
				select {
				case <-streamCtx.Done():
					fmt.Fprintln(os.Stderr)
					return nil
				case err := <-done:
					return err
				}
			}
			return docker.Demux(rc, os.Stdout, os.Stderr)
		},
	}
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "stream new log lines")
	cmd.Flags().StringVar(&tail, "tail", "all", "number of trailing lines (or \"all\")")
	return cmd
}

// containerContext resolves workspace + manifest and returns the container
// name, workspace dir, a docker runner, and the command context.
func containerContext(cmd *cobra.Command) (string, string, *docker.Runner, context.Context, error) {
	dir, err := workspace.Require(dirFlag)
	if err != nil {
		return "", "", nil, nil, err
	}
	m, err := manifest.Load(workspace.NewLayout(dir).Manifest())
	if err != nil {
		return "", "", nil, nil, err
	}
	return m.Container.Name, dir, docker.New(), cmd.Context(), nil
}

// healthURLFor derives the host-side health URL from the workspace config.
func healthURLFor(cmd *cobra.Command) (string, error) {
	dir, err := workspace.Require(dirFlag)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(workspace.NewLayout(dir).Config())
	if err != nil {
		return "", err
	}
	var cfg picoclaw.Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return "", fmt.Errorf("config.json does not parse: %w", err)
	}
	return fmt.Sprintf("http://127.0.0.1:%d/health", cfg.HealthPort()), nil
}

// credentialDeliveryGap reports an encrypted workspace that cannot decrypt its own
// config: ciphertext present and no passphrase recorded. Checked from the
// workspace alone, because both halves now live in it.
func credentialDeliveryGap(dir string, m *manifest.Manifest) bool {
	if m.Passphrase() != "" {
		return false
	}
	doc, err := configdoc.Load(workspace.NewLayout(dir).Config())
	if err != nil {
		return false // status already surfaces an unreadable config through healthURLFor
	}
	return encryptedCredentialCount(doc) > 0
}

// credentialKeyGap returns a sentence when an encrypted workspace cannot reach its
// key file. Empty string means nothing to report.
func credentialKeyGap(dir string, m *manifest.Manifest) string {
	layout := workspace.NewLayout(dir)
	doc, err := configdoc.Load(layout.Config())
	if err != nil || encryptedCredentialCount(doc) == 0 {
		return ""
	}
	if _, err := os.Stat(layout.KeyFile()); err != nil {
		return fmt.Sprintf("%s holds enc:// credentials but the key file %s is missing — PicoClaw derives its AES key from that file's bytes, so nothing can decrypt", filepath.Join("picoclaw", "config.json"), rel(dir, layout.KeyFile()))
	}
	return ""
}
