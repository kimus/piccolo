package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/kimus/piccolo/internal/docker"
	"github.com/kimus/piccolo/internal/manifest"
	"github.com/kimus/piccolo/internal/runtimeimage"
	"github.com/kimus/piccolo/internal/runtimeplan"
	"github.com/kimus/piccolo/internal/workspace"
)

type integrationRollbackFlags struct {
	to     string
	yes    bool
	dryRun bool
}

// promoteGateway is the serve entry point a rollback reuses. One sequencing path
// for serve, restart and rollback means a rollback inherits the readiness gate
// and the automatic revert instead of becoming a second, weaker implementation.
var promoteGateway = runServeDetached

func newIntegrationRollbackCmd() *cobra.Command {
	var f integrationRollbackFlags
	cmd := &cobra.Command{
		Use:   "rollback",
		Short: "Put the gateway back on the image it ran before the last promotion",
		Long: "Switches the workspace back to the previously running runtime image and promotes it\n" +
			"through the same readiness gate as any other promotion, so a rollback that does not\n" +
			"work restores the current gateway instead of leaving nothing running.\n\n" +
			"Rollback restores SERVICE, not configuration: the workspace keeps recording the\n" +
			"integration versions it has now, so the image-in-use warning will legitimately\n" +
			"report a difference until you re-add the version you want to keep.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runIntegrationRollback(cmd, f, promoteGateway)
		},
	}
	cmd.Flags().StringVar(&f.to, "to", "", "roll back to a specific local image (default: the previously running one)")
	cmd.Flags().BoolVar(&f.yes, "yes", false, "do not ask for confirmation")
	cmd.Flags().BoolVar(&f.dryRun, "dry-run", false, "report the target without stopping anything")
	return cmd
}

// rollbackRunner is the daemon surface rollback needs before it touches the
// gateway: existence and labels only.
type rollbackRunner interface {
	ImageExists(ctx context.Context, image string) (bool, error)
	ImageLabels(ctx context.Context, image string) (map[string]string, bool, error)
}

// resolveRollbackTarget decides which image a rollback would adopt. It is pure so
// the refusal paths are tested without a daemon.
func resolveRollbackTarget(m *manifest.Manifest, requested string) (current, target string, err error) {
	current = strings.TrimSpace(m.Container.Image)
	target = strings.TrimSpace(requested)
	if target == "" {
		target = strings.TrimSpace(m.Container.PreviousImage)
	}
	if target == "" {
		return current, "", errors.New("nothing to roll back to: no previous image recorded for this workspace")
	}
	if target == current {
		return current, "", fmt.Errorf("already running %s — nothing to roll back", target)
	}
	return current, target, nil
}

// describeImageContents turns an image's content labels into the lines a user
// needs to decide whether this is the image they meant to go back to. Labels are
// metadata, not proof: the promote gate still executes the binaries.
func describeImageContents(out io.Writer, labels map[string]string) {
	if base := labels[runtimeimage.LabelBaseImage]; base != "" {
		fmt.Fprintf(out, "  base:      %s\n", base)
	}
	if tc := labels[runtimeimage.LabelToolchain]; tc != "" {
		fmt.Fprintf(out, "  toolchain: %s\n", tc)
	}
	var integrations []string
	for key, value := range labels {
		if strings.HasPrefix(key, runtimeimage.LabelIntegrationPrefix) {
			integrations = append(integrations, fmt.Sprintf("  %-10s %s", "integration:", strings.TrimPrefix(key, runtimeimage.LabelIntegrationPrefix)+" "+value))
		}
	}
	sort.Strings(integrations)
	for _, line := range integrations {
		fmt.Fprintln(out, line)
	}
	if len(integrations) == 0 {
		fmt.Fprintln(out, "  integrations: none recorded")
	}
}

func runIntegrationRollback(cmd *cobra.Command, f integrationRollbackFlags, serve func(*cobra.Command) error) error {
	out := cmd.OutOrStdout()
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	dir, err := workspace.Require(dirFlag)
	if err != nil {
		return err
	}
	layout := workspace.NewLayout(dir)
	path := layout.Manifest()
	m, err := manifest.Load(path)
	if err != nil {
		return err
	}

	current, target, err := resolveRollbackTarget(m, f.to)
	if err != nil {
		return err
	}

	runner := docker.New()
	pingCtx, cancel := context.WithTimeout(ctx, defaultImageBuildProbeTimeout)
	defer cancel()
	if err := runner.Ping(pingCtx); err != nil {
		return fmt.Errorf("docker daemon not reachable: %w", err)
	}

	// Verify the target BEFORE anything is stopped. Discovering mid-promotion
	// that the image was pruned would leave the workspace with no gateway.
	present, err := runner.ImageExists(ctx, target)
	if err != nil {
		return err
	}
	if !present {
		return fmt.Errorf("rollback target %s is not present locally — nothing was stopped\n"+
			"  Rebuild it first: piccolo image build --tag %s --set", target, target)
	}

	fmt.Fprintf(out, "Rolling back the gateway\n")
	fmt.Fprintf(out, "  current:  %s\n", current)
	fmt.Fprintf(out, "  target:   %s\n", target)
	if labels, found, lerr := runner.ImageLabels(ctx, target); lerr == nil && found {
		describeImageContents(out, labels)
	} else if lerr != nil {
		fmt.Fprintf(out, "  [WARN] could not read the target image's content labels: %v\n", lerr)
	}
	if f.dryRun {
		fmt.Fprintf(out, "\ndry run: nothing changed.\n")
		return nil
	}

	if !f.yes {
		fmt.Fprintf(out, "\nPromote %s and restart the gateway? [y/N] ", target)
		line, readErr := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
		answer := strings.ToLower(strings.TrimSpace(line))
		if readErr != nil && answer == "" {
			return errors.New("rollback needs confirmation; rerun with --yes to accept non-interactively")
		}
		if answer != "y" && answer != "yes" {
			return errors.New("rollback cancelled")
		}
	}

	// Record the swap before promoting, so an interrupted rollback can still be
	// rolled back forward, and so the promote path sees the target as configured.
	m.Container.PreviousImage = current
	m.Container.Image = target
	if err := m.Write(path); err != nil {
		return fmt.Errorf("update %s: %w", workspace.ManifestName, err)
	}

	promoteErr := serve(cmd)
	if promoteErr != nil {
		// The gate failed. The promote path restores the parked gateway when it
		// can, so find out what is actually running before claiming anything — and
		// put the manifest back either way, or piccolo.yaml would keep pointing at
		// an image the gate already rejected and the next serve would retry it.
		restored, rerr := manifest.Load(path)
		if rerr == nil {
			restored.Container.Image = current
			restored.Container.PreviousImage = target
			if werr := restored.Write(path); werr != nil {
				fmt.Fprintf(out, "  [WARN] could not restore the image setting after a failed rollback: %v\n", werr)
			}
		}
		fmt.Fprintf(out, "\nRollback to %s was rejected by the readiness gate.\n", target)
		if st, serr := docker.New().StateByName(ctx, m.Container.Name); serr == nil && st.Found && st.Running {
			fmt.Fprintf(out, "  gateway running %s again; piccolo.yaml points at it\n", orUnknown(st.Image))
		} else {
			fmt.Fprintf(out, "  no gateway is running; piccolo.yaml points back at %s\n", current)
			fmt.Fprintf(out, "  Restore service with: piccolo serve\n")
		}
		return promoteErr
	}

	fmt.Fprintf(out, "\n\u2713 rolled back to %s\n", target)
	fmt.Fprintf(out, "  piccolo.yaml records %s as the rollback target if you want forward again.\n", current)
	if plan, perr := runtimeplan.Plan(m); perr == nil && plan.Tag != target {
		fmt.Fprintf(out, "\nNote: the workspace still describes %s.\n", plan.Tag)
		fmt.Fprintf(out, "  The image-in-use warning is expected until you make this version durable:\n")
		fmt.Fprintf(out, "    piccolo integration add <owner/repo>@<selector>   # pin the version you want\n")
		fmt.Fprintf(out, "    piccolo image build --set                        # or rebuild for what is recorded\n")
	}
	return nil
}
