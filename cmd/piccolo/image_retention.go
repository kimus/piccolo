package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/kimus/piccolo/internal/docker"
	"github.com/kimus/piccolo/internal/manifest"
	"github.com/kimus/piccolo/internal/runtimeimage"
	"github.com/kimus/piccolo/internal/runtimeplan"
	"github.com/kimus/piccolo/internal/workspace"
)

// retentionRunner is the daemon surface image retention needs. Tag existence is
// not evidence of reachability — only the container list says whether an image
// is still in use — so the seam includes it.
type retentionRunner interface {
	ListImages(context.Context) ([]docker.ImageRecord, error)
	ContainerImages(context.Context) (map[string][]string, error)
	RemoveImage(context.Context, string) error
}

var newRetentionRunner = func() retentionRunner { return docker.New() }

func newImageListCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List Piccolo runtime images and what still references them",
		Long: "Shows the locally built runtime images with size and age, marking which are the\n" +
			"image in use, the recorded rollback target, or referenced by a container (running or\n" +
			"stopped). Anything unmarked is unreferenced and reclaimable with 'piccolo image prune'.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runImageList(cmd, asJSON)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "machine-readable output")
	return cmd
}

func newImagePruneCmd() *cobra.Command {
	var f struct {
		yes bool
	}
	cmd := &cobra.Command{
		Use:   "prune",
		Short: "Remove Piccolo runtime images nothing references anymore",
		Long: "Deletes locally built runtime images that are not the image in use, not the recorded\n" +
			"rollback target, and not referenced by any container in any state. Prints what it kept\n" +
			"and why, so the floor can be checked rather than trusted. Nothing is removed without\n" +
			"confirmation; --yes accepts non-interactively.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runImagePrune(cmd, f.yes)
		},
	}
	cmd.Flags().BoolVar(&f.yes, "yes", false, "do not ask for confirmation")
	return cmd
}

// collectRetention asks the daemon what is on disk and what references it, then
// applies the retention floor from internal/runtimeplan.
func collectRetention(ctx context.Context, runner retentionRunner, m *manifest.Manifest) (runtimeplan.Retention, error) {
	images, err := runner.ListImages(ctx)
	if err != nil {
		return runtimeplan.Retention{}, err
	}
	containers, err := runner.ContainerImages(ctx)
	if err != nil {
		return runtimeplan.Retention{}, err
	}
	// A record can carry several tags; each becomes its own decision, because
	// protection is per reference (the manifest names a tag, not a digest).
	candidates := make([]runtimeplan.ImageInfo, 0, len(images))
	for _, img := range images {
		for _, ref := range img.RepoTags {
			if !runtimeplan.RuntimeImagePrefix(ref, runtimeimage.DefaultTagPrefix) {
				continue
			}
			if ref == "<none>:<none>" || strings.HasPrefix(ref, "<none>") {
				continue // dangling: no tag to report, and docker prune owns those
			}
			candidates = append(candidates, runtimeplan.ImageInfo{
				Ref:        ref,
				ID:         img.ID,
				Size:       img.Size,
				Created:    img.Created,
				Containers: append([]string(nil), containers[ref]...),
			})
		}
	}
	return runtimeplan.Retain(candidates, m.Container.Image, m.Container.PreviousImage), nil
}

func runImageList(cmd *cobra.Command, asJSON bool) error {
	out := cmd.OutOrStdout()
	ctx := cmd.Context()
	dir, m, err := loadWorkspaceManifest()
	if err != nil {
		return err
	}
	runner := newRetentionRunner()
	if err := pingDaemon(ctx, runner); err != nil {
		return err
	}
	plan, err := collectRetention(ctx, runner, m)
	if err != nil {
		return err
	}

	if asJSON {
		payload := map[string]any{
			"workspace":   dir,
			"current":     m.Container.Image,
			"rollback":    m.Container.PreviousImage,
			"scanned":     plan.Scanned,
			"reclaimable": plan.Reclaimable,
			"removable":   plan.Removable,
			"kept":        plan.Kept,
			"note":        "sizes are an upper bound; layers are shared between tags",
		}
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(payload)
	}

	fmt.Fprintf(out, "Piccolo runtime images in %s\n", dir)
	if len(plan.Kept) == 0 && len(plan.Removable) == 0 {
		fmt.Fprintln(out, "  (none)")
		return nil
	}
	for _, k := range plan.Kept {
		fmt.Fprintf(out, "  %-46s %8s  %s  kept: %s\n", k.Info.Ref, formatBytes(k.Info.Size), ageOf(k.Info.Created), k.Reason)
	}
	for _, r := range plan.Removable {
		fmt.Fprintf(out, "  %-46s %8s  %s  unreferenced\n", r.Ref, formatBytes(r.Size), ageOf(r.Created))
	}
	fmt.Fprintf(out, "\n  %d image(s), %s unreferenced (upper bound; shared layers mean a removal frees less)\n",
		plan.Scanned, formatBytes(plan.Reclaimable))
	if len(plan.Removable) > 0 {
		fmt.Fprintln(out, "  Reclaim with: piccolo image prune")
	}
	return nil
}

func runImagePrune(cmd *cobra.Command, assumeYes bool) error {
	out := cmd.OutOrStdout()
	ctx := cmd.Context()
	_, m, err := loadWorkspaceManifest()
	if err != nil {
		return err
	}
	runner := newRetentionRunner()
	if err := pingDaemon(ctx, runner); err != nil {
		return err
	}
	plan, err := collectRetention(ctx, runner, m)
	if err != nil {
		return err
	}
	if len(plan.Removable) == 0 {
		fmt.Fprintln(out, "Nothing to reclaim: every Piccolo runtime image is in use, is the rollback target, or is referenced by a container.")
		for _, k := range plan.Kept {
			fmt.Fprintf(out, "  kept: %s (%s)\n", k.Info.Ref, k.Reason)
		}
		return nil
	}

	fmt.Fprintf(out, "Would remove %d Piccolo runtime image(s), up to %s:\n", len(plan.Removable), formatBytes(plan.Reclaimable))
	for _, r := range plan.Removable {
		fmt.Fprintf(out, "  %s  %s  %s\n", r.Ref, formatBytes(r.Size), ageOf(r.Created))
	}
	fmt.Fprintln(out, "\nKept (retention floor):")
	for _, k := range plan.Kept {
		fmt.Fprintf(out, "  %s  %s\n", k.Info.Ref, k.Reason)
	}
	fmt.Fprintf(out, "\nSizes are an upper bound: tags share layers, so the space actually reclaimed is less.\n")

	if !assumeYes {
		if !term.IsTerminal(int(os.Stdin.Fd())) {
			return errors.New("image prune needs confirmation; rerun with --yes to accept non-interactively")
		}
		fmt.Fprintf(out, "\nRemove these images? [y/N] ")
		line, readErr := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
		answer := strings.ToLower(strings.TrimSpace(line))
		if readErr != nil && answer == "" {
			return errors.New("image prune needs confirmation; rerun with --yes to accept non-interactively")
		}
		if answer != "y" && answer != "yes" {
			return errors.New("image prune cancelled")
		}
	}

	removed, failed := 0, 0
	for _, r := range plan.Removable {
		if err := runner.RemoveImage(ctx, r.Ref); err != nil {
			// One refusal must not abandon the rest, and must not be silent.
			fmt.Fprintf(out, "  [WARN] %v\n", err)
			failed++
			continue
		}
		fmt.Fprintf(out, "  removed %s\n", r.Ref)
		removed++
	}
	fmt.Fprintf(out, "\n\u2713 removed %d image(s)", removed)
	if failed > 0 {
		fmt.Fprintf(out, ", %d could not be removed", failed)
	}
	fmt.Fprintln(out)
	if failed > 0 {
		return fmt.Errorf("image prune incomplete: %d image(s) remained", failed)
	}
	return nil
}

// pingDaemon keeps the daemon check inside the seam's own timeout so a missing
// socket says "docker daemon not reachable" rather than hanging a listing.
func pingDaemon(ctx context.Context, runner retentionRunner) error {
	if pinger, ok := runner.(interface {
		Ping(context.Context) error
	}); ok {
		probeCtx, cancel := context.WithTimeout(ctx, defaultImageBuildProbeTimeout)
		defer cancel()
		if err := pinger.Ping(probeCtx); err != nil {
			return fmt.Errorf("docker daemon not reachable: %w", err)
		}
	}
	return nil
}

func loadWorkspaceManifest() (string, *manifest.Manifest, error) {
	dir, err := workspace.Require(dirFlag)
	if err != nil {
		return "", nil, err
	}
	m, err := manifest.Load(workspace.NewLayout(dir).Manifest())
	if err != nil {
		return "", nil, err
	}
	return dir, m, nil
}

func formatBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%c", float64(n)/float64(div), "KMGTPE"[exp])
}

func ageOf(unix int64) string {
	if unix <= 0 {
		return "age unknown"
	}
	d := time.Since(time.Unix(unix, 0))
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}
