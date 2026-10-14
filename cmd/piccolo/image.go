package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
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

// refreshInstalledIdentity aligns the recorded integration metadata with the
// artifacts that were actually placed in the image.
func refreshInstalledIdentity(m *manifest.Manifest, bundles []runtimeimage.IntegrationBundle) {
	byPackage := make(map[string]runtimeimage.IntegrationBundle, len(bundles))
	for _, bundle := range bundles {
		byPackage[bundle.Package] = bundle
	}
	for i := range m.Runtime.Integrations {
		item := m.Runtime.Integrations[i]
		if item.Source != runtimeplan.SourceRelease {
			continue
		}
		bundle, ok := byPackage[item.Package]
		if !ok {
			continue
		}
		if bundle.Version != "" {
			item.ResolvedVersion = bundle.Version
		}
		if bundle.Digest != "" {
			item.ArtifactDigest = bundle.Digest
		}
		if len(bundle.Commands) > 0 {
			item.Commands = append([]string(nil), bundle.Commands...)
		}
		m.Runtime.Integrations[i] = item
	}
}

func newImageBuildCmd() *cobra.Command {
	var f imageBuildFlags
	cmd := &cobra.Command{
		Use:   "build",
		Short: "Rebuild this workspace's runtime image: toolchain and integrations baked in",
		Long: "Builds a local, pinned Docker image from the workspace's configured base plus its " +
			"runtime toolchain and every installed integration. This is the command a stale-image " +
			"warning points at: Piccolo never rebuilds implicitly, so an image edit stays user-initiated. " +
			"The generated build context is temporary/in-memory; Piccolo only edits piccolo.yaml when " +
			"--set is used or when an interactive prompt is accepted.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runImageBuild(cmd, f)
		},
	}
	cmd.Flags().StringVar(&f.base, "base", "", "base PicoClaw image (default: the workspace's official base, preserving the launcher flavor)")
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
	layout := workspace.NewLayout(dir)
	manifestPath := layout.Manifest()
	m, err := manifest.Load(manifestPath)
	if err != nil {
		return err
	}

	base := strings.TrimSpace(f.base)
	if base == "" {
		base = integrationBuildBase(m)
	}
	// An image build must reproduce the WHOLE workspace image: toolchain plus
	// every installed integration. Building only the toolchain would produce an
	// image that silently drops integrations, which is worse than not having a
	// rebuild command at all.
	tc := effectiveToolchain(m)
	packages := tc.Packages
	if len(f.packageList) > 0 {
		packages = f.packageList
	}
	ctxWithTimeout := ctx
	var bundles []runtimeimage.IntegrationBundle
	if len(runtimeplan.Bundles(m.Runtime.Integrations)) > 0 {
		fmt.Fprintf(out, "Expanding installed integration releases (needed in the build context)...\n")
		fetched, err := fetchReleaseBundlesForManifest(ctxWithTimeout, newReleaseFetcher(), m.Runtime.Integrations)
		if err != nil {
			return err
		}
		bundles = fetched
	}
	plan, err := runtimeimage.BuildPlan(runtimeimage.Options{
		BaseImage:    base,
		Tag:          f.tag,
		Packages:     packages,
		Commands:     tc.Commands,
		Integrations: sourceManifestIntegrationsForBuild(m.Runtime.Integrations),
		Bundles:      bundles,
	})
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
	fmt.Fprintf(out, "  toolchain: %d packages, %d commands (baked into the image)\n", len(plan.Packages), len(plan.Commands))
	for _, identity := range plan.Identity {
		fmt.Fprintf(out, "  integration: %s\n", identity)
	}
	if len(plan.Integrations) > 0 {
		for _, in := range plan.Integrations {
			fmt.Fprintf(out, "  integration (source build): %s\n", in.Ref)
		}
	}
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
		// Record what the image actually contains. The build used the expanded
		// artifacts' version, digest and command list, so those are the values the
		// drift check must compare against later — otherwise a rebuild moves the tag
		// without moving the record, and the workspace stays "stale" forever.
		refreshInstalledIdentity(m, bundles)
		if err := m.Write(manifestPath); err != nil {
			return fmt.Errorf("update %s: %w", workspace.ManifestName, err)
		}
		fmt.Fprintf(out, "\nUpdated %s: container.image: %s\n", workspace.ManifestName, plan.Tag)
	} else {
		fmt.Fprintf(out, "\n%s unchanged. To adopt manually, set:\n  container.image: %s\n", workspace.ManifestName, plan.Tag)
	}

	fmt.Fprintln(out, "\nNext steps:")
	fmt.Fprintln(out, "  piccolo restart       # promote the new image (gated, reverts on failure)")
	fmt.Fprintln(out, "  piccolo runtime status  # confirm the toolchain is available in the container")
	fmt.Fprintln(out, "  piccolo chat -m 'Run file --version && python3 --version && zbarimg --version'")
	fmt.Fprintln(out, "\nNote: the toolchain and the integration binaries are baked into this image.")
	fmt.Fprintln(out, "nothing installs when the gateway starts, and /nix is never mounted over.")
	return nil
}
