package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/kimus/piccolo/internal/docker"
	"github.com/kimus/piccolo/internal/generate"
	"github.com/kimus/piccolo/internal/manifest"
	"github.com/kimus/piccolo/internal/runtimeimage"
	"github.com/kimus/piccolo/internal/workspace"
)

const defaultImageBuildProbeTimeout = 3 * time.Second

type imageBuildFlags struct {
	base        string
	tag         string
	setManifest bool
	yes         bool
	pull        bool
	packageList []string
}

type imageRunner interface {
	Ping(context.Context) error
	BuildImage(context.Context, io.Reader, docker.ImageBuildOptions, io.Writer) error
}

var newImageRunner = func() imageRunner { return docker.New() }

func newImageCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "image",
		Short: "Build and manage Piccolo runtime images",
	}
	cmd.AddCommand(newImageBuildCmd())
	return cmd
}

func newImageBuildCmd() *cobra.Command {
	var f imageBuildFlags
	cmd := &cobra.Command{
		Use:   "build",
		Short: "Build a local PicoClaw runtime image with Devbox file-handling tools",
		Long: "Builds a local, pinned Docker image derived from the workspace's configured " +
			"PicoClaw image (or --base). The generated build context is temporary/in-memory; " +
			"Piccolo only edits piccolo.yaml when --set is used or when an interactive prompt is accepted.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runImageBuild(cmd, f)
		},
	}
	cmd.Flags().StringVar(&f.base, "base", "", "base PicoClaw image (default: container.image from piccolo.yaml)")
	cmd.Flags().StringVar(&f.tag, "tag", "", "local image tag to build (default: deterministic content-derived tag)")
	cmd.Flags().BoolVar(&f.setManifest, "set", false, "update piccolo.yaml container.image to the built tag after a successful build")
	cmd.Flags().BoolVar(&f.yes, "yes", false, "non-interactive: do not prompt; requires --set to modify piccolo.yaml")
	cmd.Flags().BoolVar(&f.pull, "pull", false, "ask Docker to pull a newer base image before building")
	cmd.Flags().StringArrayVar(&f.packageList, "package", nil, "additional or replacement Devbox package list entry (repeatable; replaces defaults when provided)")
	return cmd
}

func runImageBuild(cmd *cobra.Command, f imageBuildFlags) error {
	out := cmd.OutOrStdout()
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	dir, err := workspace.Require(dirFlag)
	if err != nil {
		return err
	}
	manifestPath := filepath.Join(dir, workspace.ManifestName)
	m, err := manifest.Load(manifestPath)
	if err != nil {
		return err
	}

	base := strings.TrimSpace(f.base)
	if base == "" {
		base = m.Container.Image
		// Never stack a rebuild on top of a previous Piccolo runtime image: that
		// would inherit its (possibly stale) layers. Rebuild from the official
		// PicoClaw base instead, preserving the launcher flavor for dashboards.
		if runtimeimage.IsLocalRuntimeImage(base) {
			base = runtimeimage.DefaultBaseImage
			for _, p := range m.Container.Ports {
				if strings.Contains(p, ":18800") {
					base = generate.LauncherImage
					break
				}
			}
		}
	}
	plan, err := runtimeimage.BuildPlan(runtimeimage.Options{BaseImage: base, Tag: f.tag, Packages: f.packageList})
	if err != nil {
		return err
	}
	if !manifest.IsPinnedImage(plan.Tag) {
		return fmt.Errorf("built image tag %q must be pinned and not latest", plan.Tag)
	}

	runner := newImageRunner()
	pingCtx, cancel := context.WithTimeout(ctx, defaultImageBuildProbeTimeout)
	defer cancel()
	if err := runner.Ping(pingCtx); err != nil {
		return fmt.Errorf("docker daemon not reachable: %w", err)
	}

	buildCtx, err := plan.ContextTar()
	if err != nil {
		return err
	}

	fmt.Fprintf(out, "Building Piccolo runtime image\n")
	fmt.Fprintf(out, "  base: %s\n", plan.BaseImage)
	fmt.Fprintf(out, "  tag:  %s\n", plan.Tag)
	fmt.Fprintf(out, "  devbox global: %s\n", runtimeimage.DevboxGlobalDir)
	fmt.Fprintln(out, "")
	if err := runner.BuildImage(ctx, buildCtx, docker.ImageBuildOptions{Tag: plan.Tag, Dockerfile: "Dockerfile", Pull: f.pull, Remove: true}, out); err != nil {
		return fmt.Errorf("build image: %w", err)
	}

	adopt := f.setManifest
	if !adopt && !f.yes && term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Fprintf(out, "\nUpdate %s container.image to %s? [y/N] ", workspace.ManifestName, plan.Tag)
		line, _ := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
		adopt = strings.EqualFold(strings.TrimSpace(line), "y") || strings.EqualFold(strings.TrimSpace(line), "yes")
	}
	if adopt {
		m.Container.Image = plan.Tag
		if err := m.Write(manifestPath); err != nil {
			return fmt.Errorf("update %s: %w", workspace.ManifestName, err)
		}
		fmt.Fprintf(out, "\nUpdated %s: container.image: %s\n", workspace.ManifestName, plan.Tag)
	} else {
		fmt.Fprintf(out, "\n%s unchanged. To adopt manually, set:\n  container.image: %s\n", workspace.ManifestName, plan.Tag)
	}

	fmt.Fprintln(out, "\nNext steps:")
	fmt.Fprintln(out, "  piccolo setup tools   # ensure agents have the file-handling skill")
	fmt.Fprintln(out, "  piccolo restart       # recreate the gateway; serve installs the Devbox tools once")
	fmt.Fprintln(out, "  piccolo chat -m 'Run file --version && python3 --version && zbarimg --version'")
	fmt.Fprintln(out, "\nNote: the image is lean \u2014 tools install into a persisted Devbox global volume on first 'piccolo serve' (or restart). Uploaded media under /tmp/picoclaw_media is readable by the agent.")
	return nil
}
