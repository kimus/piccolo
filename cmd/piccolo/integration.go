package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/kimus/piccolo/internal/docker"
	"github.com/kimus/piccolo/internal/generate"
	picintegration "github.com/kimus/piccolo/internal/integration"
	"github.com/kimus/piccolo/internal/manifest"
	"github.com/kimus/piccolo/internal/runtimeimage"
	"github.com/kimus/piccolo/internal/runtimeplan"
	"github.com/kimus/piccolo/internal/workspace"
)

type integrationFlags struct {
	yes   bool
	pull  bool
	tag   string
	force bool // rebuild even when the resolved release/image already matches
}

type integrationUpgradeFlags struct {
	integrationFlags
	all bool
}

type integrationStatusFlags struct {
	all  bool
	json bool
}

type integrationRunner interface {
	Ping(context.Context) error
	BuildImage(context.Context, io.Reader, docker.ImageBuildOptions, io.Writer) error
	Run(context.Context, docker.Spec) (docker.Process, error)
	RemoveByPrefix(context.Context, string) ([]string, error)
	// Exec runs a command inside an already-running one-shot container, which
	// is how Piccolo delivers a captured browser callback back onto the
	// container's own loopback (the container's loopback is unreachable from
	// the host, but its network namespace is reachable this way).
	Exec(context.Context, string, []string, bool) (*docker.ExecSession, error)
}

var newIntegrationRunner = func() integrationRunner { return docker.New() }

func newIntegrationCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "integration", Short: "Install and set up containerized integrations"}
	cmd.AddCommand(newIntegrationAddCmd(), newIntegrationListCmd(), newIntegrationSetupCmd(), newIntegrationUpgradeCmd(), newIntegrationStatusCmd(), newIntegrationRollbackCmd(), newIntegrationRemoveCmd(), newRetiredIntegrationBuildCmd())
	return rejectUnknownArgs(cmd)
}

// newRetiredIntegrationBuildCmd keeps `integration build` failing loudly instead
// of silently printing help with exit 0. The command went away with the
// Devbox/Nix toolchain because it resolved a Nix flake reference, and muscle
// memory plus a zero exit code in a script is the worst possible outcome.
func newRetiredIntegrationBuildCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "build <package-ref>",
		Short:  "Removed: source-built integrations are no longer supported",
		Hidden: true,
		Args:   cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			target := "<owner/repo>"
			if len(args) > 0 {
				target = args[0]
			}
			return fmt.Errorf("'piccolo integration build' has been removed: source builds resolved a Devbox/Nix flake reference, and the runtime image installs its toolchain with the base distribution's package manager. Install from a published release instead: 'piccolo integration add %s'", target)
		},
	}
}

func newIntegrationAddCmd() *cobra.Command {
	var f integrationFlags
	cmd := &cobra.Command{
		Use:   "add <package-ref>",
		Short: "Install or update an integration from a release artifact",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runIntegrationAdd(cmd, args[0], f)
		},
	}
	cmd.Flags().BoolVar(&f.yes, "yes", false, "non-interactive: accept prompts")
	cmd.Flags().BoolVar(&f.pull, "pull", false, "ask Docker to pull a newer base image before building")
	cmd.Flags().StringVar(&f.tag, "tag", "", "local image tag to build (default: deterministic content-derived tag)")
	cmd.Flags().BoolVar(&f.force, "force", false, "rebuild even if the resolved release or runtime image already matches")
	return cmd
}

func newIntegrationListCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List installed integrations",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runIntegrationList(cmd, jsonOut)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit JSON output")
	return cmd
}

func newIntegrationSetupCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "setup <name>",
		Short: "Run an installed integration's setup command in a temporary container",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runIntegrationSetup(cmd, args[0])
		},
	}
	return cmd
}

func newIntegrationStatusCmd() *cobra.Command {
	var f integrationStatusFlags
	cmd := &cobra.Command{
		Use:   "status [name]",
		Short: "Run installed integration connectivity checks",
		Args: func(cmd *cobra.Command, args []string) error {
			if f.all {
				if len(args) != 0 {
					return fmt.Errorf("use either 'piccolo integration status <name>' or 'piccolo integration status --all', not both")
				}
				return nil
			}
			if len(args) != 1 {
				return fmt.Errorf("use either 'piccolo integration status <name>' or 'piccolo integration status --all'")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			name := ""
			if len(args) > 0 {
				name = args[0]
			}
			return runIntegrationStatus(cmd, name, f)
		},
	}
	cmd.Flags().BoolVar(&f.all, "all", false, "check all installed integrations")
	cmd.Flags().BoolVar(&f.json, "json", false, "emit JSON status output")
	return cmd
}

func newIntegrationUpgradeCmd() *cobra.Command {
	var f integrationUpgradeFlags
	cmd := &cobra.Command{
		Use:   "upgrade [name]",
		Short: "Rebuild installed integrations and refresh descriptor metadata",
		Args: func(cmd *cobra.Command, args []string) error {
			if f.all && len(args) != 0 {
				return fmt.Errorf("use either 'piccolo integration upgrade <package>' or 'piccolo integration upgrade --all', not both")
			}
			if len(args) > 1 {
				return fmt.Errorf("use 'piccolo integration upgrade [package]' or 'piccolo integration upgrade --all'")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			name := ""
			if len(args) > 0 {
				name = args[0]
			}
			return runIntegrationUpgrade(cmd, name, f)
		},
	}
	cmd.Flags().BoolVar(&f.yes, "yes", false, "non-interactive: accept prompts")
	cmd.Flags().BoolVar(&f.pull, "pull", false, "ask Docker to pull a newer base image before building")
	cmd.Flags().StringVar(&f.tag, "tag", "", "local image tag to build (default: deterministic content-derived tag)")
	cmd.Flags().BoolVar(&f.force, "force", false, "rebuild even if the resolved release or runtime image already matches")
	cmd.Flags().BoolVar(&f.all, "all", false, "upgrade all installed latest-channel integrations")
	return cmd
}

func runIntegrationAdd(cmd *cobra.Command, input string, f integrationFlags) error {
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
	pkg, err := picintegration.ParsePackageRef(input)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "Integration package: %s\n", pkg.Short())
	fmt.Fprintf(out, "Provider:            %s\n", pkg.Provider)
	fmt.Fprintf(out, "Selector:            %s\n", pkg.EffectiveSelector())
	if pkg.Channel() == picintegration.ChannelLatest {
		fmt.Fprintf(out, "[WARN] integration tracks latest; future upgrades may resolve newer releases\n")
	}
	fetcher := newReleaseFetcher()
	res, err := fetcher.Resolve(ctx, pkg)
	if err != nil {
		return err
	}

	// Already installed at this exact release? Then there is nothing to
	// download, nothing to build, and nothing to ask.
	if installed, ok := manifestIntegration(m, pkg.Package()); ok && !f.force &&
		installed.ResolvedVersion == res.Version && installed.ArtifactDigest == res.Digest {
		fmt.Fprintf(out, "\n✓ Already up to date: %s %s (sha256:%s)\n", pkg.Short(), res.Version, shortDigest(res.Digest))
		fmt.Fprintf(out, "  image: %s\n", m.Container.Image)
		fmt.Fprintf(out, "  state: %s (preserved)\n", installed.StatePath)
		fmt.Fprintf(out, "\nUse --force to rebuild anyway, or 'piccolo integration add %s@<selector>' to change version.\n", pkg.Short())
		return nil
	}

	printIntegrationPlan(out, m, pkg, res, installedOf(m, pkg.Package()))
	if !f.yes {
		fmt.Fprintf(out, "\nInstall or update this integration release? [y/N] ")
		line, readErr := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
		answer := strings.ToLower(strings.TrimSpace(line))
		if readErr != nil && answer == "" {
			// No answer available at all: refuse rather than silently installing.
			return errors.New("integration add needs confirmation; rerun with --yes to accept non-interactively")
		}
		if answer != "y" && answer != "yes" {
			return errors.New("integration add cancelled")
		}
	}

	runner := newIntegrationRunner()
	pingCtx, cancel := context.WithTimeout(ctx, defaultImageBuildProbeTimeout)
	defer cancel()
	if err := runner.Ping(pingCtx); err != nil {
		return fmt.Errorf("docker daemon not reachable: %w", err)
	}
	bundles, err := fetchReleaseBundlesForManifest(ctx, fetcher, m.Runtime.Integrations)
	if err != nil {
		return err
	}
	fetched, err := fetcher.Download(ctx, res)
	if err != nil {
		return err
	}
	base := integrationBuildBase(m)
	bundles = upsertBundle(bundles, fetched.Bundle)
	if err := rejectSourceBuilds(m, pkg.Package()); err != nil {
		return err
	}
	plan, err := runtimeimage.BuildPlan(runtimeimage.Options{BaseImage: base, Tag: f.tag, Packages: effectiveToolchain(m).Packages, Commands: effectiveToolchain(m).Commands, Bundles: bundles})
	if err != nil {
		return err
	}
	// Authoritative no-op catch: the tag is a digest of the exact image inputs,
	// so equality means a build would reproduce the image we already point at.
	if !f.force && !f.yes && plan.Tag == m.Container.Image {
		fmt.Fprintf(out, "\n✓ Runtime image already matches this configuration (%s); nothing to rebuild. Use --force to rebuild anyway.\n", plan.Tag)
		return nil
	}
	buildCtx, err := plan.ContextTar()
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "\nBuilding Piccolo runtime image with integration release\n")
	fmt.Fprintf(out, "  base:    %s\n", plan.BaseImage)
	fmt.Fprintf(out, "  tag:     %s\n", plan.Tag)
	fmt.Fprintf(out, "  package: %s\n", pkg.Short())
	fmt.Fprintf(out, "  version: %s\n\n", fetched.Version)
	if err := runner.BuildImage(ctx, buildCtx, docker.ImageBuildOptions{Tag: plan.Tag, Dockerfile: "Dockerfile", Pull: f.pull, Remove: true}, out); err != nil {
		return fmt.Errorf("build image: %w", err)
	}
	if err := layout.EnsureDirs(); err != nil {
		return err
	}
	descCmd := pkg.DescriptorCommand()
	descEntrypoint, descCommand := splitEntrypointCommand(descCmd)
	descData, err := runContainerCapture(ctx, runner, docker.Spec{Image: plan.Tag, Source: layout.PicoclawHome(), Volumes: integrationSetupBinds(layout), Entrypoint: descEntrypoint, Command: descCommand, Env: nil, Labels: map[string]string{docker.LabelWorkspace: dir}})
	if err != nil {
		return fmt.Errorf("describe integration: %w", err)
	}
	desc, err := picintegration.ParseDescriptor(descData)
	if err != nil {
		return err
	}
	statePath := pkg.StatePath()
	containerState := pkg.ContainerStatePath()
	item := manifest.Integration{
		Package:         pkg.Package(),
		Requested:       input,
		Source:          string(picintegration.SourceRelease),
		Channel:         string(pkg.Channel()),
		Selector:        pkg.EffectiveSelector(),
		ResolvedVersion: fetched.Version,
		ArtifactDigest:  fetched.Digest,
		StatePath:       statePath,
		Name:            desc.Name,
		DisplayName:     desc.DisplayName,
		// Record the commands the IMAGE verifies, which is the artifact release
		// manifest's list. Recording the descriptor's list instead makes the
		// recorded identity differ from what was built, so the drift check would
		// report a difference forever and a rebuild could never clear it.
		Commands:          installedCommands(fetched.Bundle.Commands, desc.Commands),
		DescriptorCommand: descCmd,
		SetupCommand:      rewriteStateArgs(desc.Setup.Command, containerState),
		StatusCommand:     rewriteStateArgs(desc.StatusCommand, containerState),
		SetupRequiresTTY:  desc.Setup.RequiresTTY,
		SetupPorts:        desc.Setup.Ports,
		MCPServerKey:      desc.MCPServerKey,
	}
	m.Container.Image = plan.Tag
	m.Runtime.Integrations = upsertManifestIntegration(m.Runtime.Integrations, item)
	if err := m.Write(manifestPath); err != nil {
		return fmt.Errorf("update %s: %w", workspace.ManifestName, err)
	}
	fmt.Fprintf(out, "\n✓ Added integration: %s\n", desc.DisplayName)
	fmt.Fprintf(out, "  package: %s\n", pkg.Short())
	fmt.Fprintf(out, "  version: %s\n", fetched.Version)
	fmt.Fprintf(out, "  state:   %s\n", statePath)
	fmt.Fprintf(out, "  image:   %s\n", plan.Tag)
	fmt.Fprintf(out, "\nNext:\n  piccolo integration setup %s\n  piccolo restart\n", pkg.Short())
	return nil
}

func runIntegrationList(cmd *cobra.Command, jsonOut bool) error {
	out := cmd.OutOrStdout()
	dir, err := workspace.Require(dirFlag)
	if err != nil {
		return err
	}
	m, err := manifest.Load(workspace.NewLayout(dir).Manifest())
	if err != nil {
		return err
	}
	if jsonOut {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(m.Runtime.Integrations)
	}
	if len(m.Runtime.Integrations) == 0 {
		fmt.Fprintln(out, "No integrations installed.")
		return nil
	}
	fmt.Fprintf(out, "%-28s %-12s %-32s %-10s %-8s\n", "PACKAGE", "NAME", "DISPLAY", "VERSION", "CHANNEL")
	for _, in := range m.Runtime.Integrations {
		pkg := displayPackage(in)
		fmt.Fprintf(out, "%-28s %-12s %-32s %-10s %-8s\n", pkg, in.Name, displayIntegration(in), valueOrDash(in.ResolvedVersion), valueOrDash(in.Channel))
	}
	return nil
}

func runIntegrationStatus(cmd *cobra.Command, name string, f integrationStatusFlags) error {
	out := cmd.OutOrStdout()
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	if f.all && strings.TrimSpace(name) != "" {
		return fmt.Errorf("use either 'piccolo integration status <name>' or 'piccolo integration status --all', not both")
	}
	if !f.all && strings.TrimSpace(name) == "" {
		return fmt.Errorf("use either 'piccolo integration status <name>' or 'piccolo integration status --all'")
	}
	dir, err := workspace.Require(dirFlag)
	if err != nil {
		return err
	}
	layout := workspace.NewLayout(dir)
	m, err := manifest.Load(layout.Manifest())
	if err != nil {
		return err
	}
	if len(m.Runtime.Integrations) == 0 {
		return fmt.Errorf("no integrations installed — run 'piccolo integration add <owner/repo>' first")
	}
	selected := []manifest.Integration{}
	if f.all {
		selected = append(selected, m.Runtime.Integrations...)
	} else {
		for _, item := range m.Runtime.Integrations {
			if integrationMatches(item, name) {
				selected = append(selected, item)
				break
			}
		}
		if len(selected) == 0 {
			return fmt.Errorf("integration %q is not installed — run 'piccolo integration add <owner/repo>' first", name)
		}
	}
	if err := layout.EnsureDirs(); err != nil {
		return err
	}
	runner := newIntegrationRunner()
	pingCtx, cancel := context.WithTimeout(ctx, defaultImageBuildProbeTimeout)
	defer cancel()
	if err := runner.Ping(pingCtx); err != nil {
		return fmt.Errorf("docker daemon not reachable: %w", err)
	}
	results := make([]picintegration.StatusResult, 0, len(selected))
	for _, item := range selected {
		res, err := integrationStatusResult(ctx, runner, layout, dir, m.Container.Image, item)
		if err != nil {
			return err
		}
		results = append(results, res)
	}
	if f.json {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		if f.all {
			if err := enc.Encode(map[string]any{"schema": "piccolo.integration.status.aggregate.v1", "integrations": results}); err != nil {
				return err
			}
		} else {
			if err := enc.Encode(results[0]); err != nil {
				return err
			}
		}
	} else {
		renderIntegrationStatuses(out, results)
	}
	for _, res := range results {
		if res.Status == "fail" {
			cmd.SilenceErrors = true
			return fmt.Errorf("one or more integration status checks failed")
		}
	}
	return nil
}

func integrationStatusResult(ctx context.Context, runner integrationRunner, layout workspace.Layout, workspaceRoot, image string, item manifest.Integration) (picintegration.StatusResult, error) {
	if len(item.StatusCommand) == 0 {
		return picintegration.StatusResult{
			Schema:  picintegration.StatusSchemaV1,
			Name:    item.Name,
			Status:  "unknown",
			Summary: "status is not supported by this installed integration",
			Checks: []picintegration.StatusCheck{{
				ID:          "status-command",
				Label:       "Status command",
				Status:      "unknown",
				Message:     "integration descriptor did not declare a status command",
				Remediation: "Run 'piccolo integration upgrade " + upgradeHint(item) + "' after installing a version that supports status.",
			}},
		}, nil
	}
	entrypoint, command := splitEntrypointCommand(item.StatusCommand)
	stdout, stderr, code, err := runContainerCaptureWithExitCode(ctx, runner, docker.Spec{Name: statusContainerName(item.Name), Image: image, Source: layout.PicoclawHome(), Volumes: integrationSetupBinds(layout), Entrypoint: entrypoint, Command: command, Env: nil, Labels: map[string]string{docker.LabelWorkspace: workspaceRoot}})
	if err != nil {
		return picintegration.StatusResult{}, fmt.Errorf("status integration %s: %w", item.Name, err)
	}
	// An integration signals unhealthy checks by exiting non-zero while printing
	// a valid report. Prefer the report; only treat it as an invocation failure
	// when stdout is not a parseable status payload.
	res, parseErr := picintegration.ParseStatus(stdout)
	if parseErr != nil {
		if code != 0 {
			return picintegration.StatusResult{}, fmt.Errorf("status integration %s: command exited %d: %s", item.Name, code, strings.TrimSpace(string(stderr)))
		}
		return picintegration.StatusResult{}, fmt.Errorf("parse integration status for %s: %w", item.Name, parseErr)
	}
	if res.Name != item.Name {
		return picintegration.StatusResult{}, fmt.Errorf("integration %q status reported name %q", item.Name, res.Name)
	}
	if code != 0 && res.Status != "fail" {
		// Keep the unhealthy exit visible in both human and JSON output.
		res.Checks = append(res.Checks, picintegration.StatusCheck{
			ID:          "exit-code",
			Label:       "Status command",
			Status:      "fail",
			Message:     fmt.Sprintf("status command exited %d", code),
			Remediation: "Inspect the failing checks above; the integration reported an unhealthy state.",
		})
		res.Status = "fail"
	}
	return res, nil
}

func renderIntegrationStatuses(out io.Writer, results []picintegration.StatusResult) {
	for i, res := range results {
		if i > 0 {
			fmt.Fprintln(out)
		}
		label := res.Name
		fmt.Fprintf(out, "%s: %s", label, strings.ToUpper(res.Status))
		if strings.TrimSpace(res.Summary) != "" {
			fmt.Fprintf(out, " — %s", res.Summary)
		}
		fmt.Fprintln(out)
		for _, check := range res.Checks {
			fmt.Fprintf(out, "  [%s] %s", strings.ToUpper(check.Status), check.Label)
			if check.Message != "" {
				fmt.Fprintf(out, ": %s", check.Message)
			}
			fmt.Fprintln(out)
			if check.Remediation != "" {
				fmt.Fprintf(out, "       %s\n", check.Remediation)
			}
			// The link was already parsed from the payload and printed by the
			// setup flow; status silently dropped it, so a provider-supplied fix
			// ("Enable it at <url>") arrived as prose the user must search for.
			if check.RemediationURL != "" {
				label := strings.TrimSpace(check.RemediationLabel)
				if label == "" {
					label = "More info"
				}
				fmt.Fprintf(out, "       %s: %s\n", label, check.RemediationURL)
			}
		}
	}
}

func runIntegrationUpgrade(cmd *cobra.Command, name string, f integrationUpgradeFlags) error {
	out := cmd.OutOrStdout()
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	if f.all && strings.TrimSpace(name) != "" {
		return fmt.Errorf("use either 'piccolo integration upgrade <package>' or 'piccolo integration upgrade --all', not both")
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
	if len(m.Runtime.Integrations) == 0 {
		return fmt.Errorf("no integrations installed — run 'piccolo integration add <owner/repo>' first")
	}
	indexes := []int{}
	if strings.TrimSpace(name) == "" || f.all {
		for i, item := range m.Runtime.Integrations {
			if item.Source == string(picintegration.SourceRelease) && item.Channel == string(picintegration.ChannelLatest) {
				indexes = append(indexes, i)
			}
		}
	} else {
		for i, item := range m.Runtime.Integrations {
			if integrationMatches(item, name) {
				if item.Source != string(picintegration.SourceRelease) {
					return fmt.Errorf("integration %s was built from source, which the runtime image no longer installs; replace it with a published release using 'piccolo integration add %s'", name, displayPackage(item))
				}
				if item.Channel != string(picintegration.ChannelLatest) {
					return fmt.Errorf("%s is pinned to %s. Use 'piccolo integration add %s@<selector>' to change versions, or 'piccolo integration add %s' to track latest", displayPackage(item), item.Selector, displayPackage(item), displayPackage(item))
				}
				indexes = append(indexes, i)
				break
			}
		}
		if len(indexes) == 0 {
			return fmt.Errorf("integration %q is not installed — run 'piccolo integration add <owner/repo>' first", name)
		}
	}
	if len(indexes) == 0 {
		fmt.Fprintln(out, "No latest-channel integrations to upgrade.")
		return nil
	}
	// Resolve first (metadata only): if nothing actually changed there is no
	// prompt to ask, nothing to download, and no image to rebuild.
	fetcher := newReleaseFetcher()
	resolutions := make(map[int]releaseResolution, len(indexes))
	var changed bool
	fmt.Fprintf(out, "Resolving latest releases:\n")
	for _, i := range indexes {
		item := m.Runtime.Integrations[i]
		pkg, err := packageRefForManifest(item)
		if err != nil {
			return err
		}
		pkg.Selector = ""
		res, err := fetcher.Resolve(ctx, pkg)
		if err != nil {
			return err
		}
		resolutions[i] = res
		if item.ResolvedVersion != res.Version || item.ArtifactDigest != res.Digest {
			changed = true
		}
		fmt.Fprintf(out, "  %s: %s -> %s\n", pkg.Short(), valueOrDash(item.ResolvedVersion), res.Version)
	}
	if !changed && !f.force {
		fmt.Fprintf(out, "\n\u2713 Already up to date (image %s); nothing to rebuild.\n", m.Container.Image)
		fmt.Fprintln(out, "Use --force to rebuild anyway, or 'piccolo integration add <owner/repo>@<selector>' to pin a version.")
		return nil
	}
	if !f.yes && term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Fprintf(out, "Upgrade %d latest-channel integration(s)? [y/N] ", len(indexes))
		line, _ := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
		if !strings.EqualFold(strings.TrimSpace(line), "y") && !strings.EqualFold(strings.TrimSpace(line), "yes") {
			return errors.New("integration upgrade cancelled")
		}
	}
	runner := newIntegrationRunner()
	pingCtx, cancel := context.WithTimeout(ctx, defaultImageBuildProbeTimeout)
	defer cancel()
	if err := runner.Ping(pingCtx); err != nil {
		return fmt.Errorf("docker daemon not reachable: %w", err)
	}
	planned := append([]manifest.Integration(nil), m.Runtime.Integrations...)
	fetches := map[int]fetchedRelease{}
	bundles, err := fetchReleaseBundlesForManifest(ctx, fetcher, planned)
	if err != nil {
		return err
	}
	for _, i := range indexes {
		res := resolutions[i]
		fetched, err := fetcher.Download(ctx, res)
		if err != nil {
			return err
		}
		fetches[i] = fetched
		bundles = upsertBundle(bundles, fetched.Bundle)
	}
	base := integrationBuildBase(m)
	if err := rejectSourceBuilds(m, ""); err != nil {
		return err
	}
	plan, err := runtimeimage.BuildPlan(runtimeimage.Options{BaseImage: base, Tag: f.tag, Packages: effectiveToolchain(m).Packages, Commands: effectiveToolchain(m).Commands, Bundles: bundles})
	if err != nil {
		return err
	}
	buildCtx, err := plan.ContextTar()
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "\nBuilding Piccolo runtime image with upgraded integration releases\n")
	fmt.Fprintf(out, "  base: %s\n", plan.BaseImage)
	fmt.Fprintf(out, "  tag:  %s\n\n", plan.Tag)
	if err := runner.BuildImage(ctx, buildCtx, docker.ImageBuildOptions{Tag: plan.Tag, Dockerfile: "Dockerfile", Pull: f.pull, Remove: true}, out); err != nil {
		return fmt.Errorf("build image: %w", err)
	}
	if err := layout.EnsureDirs(); err != nil {
		return err
	}
	for i, fetched := range fetches {
		pkg := fetched.Ref
		descCmd := pkg.DescriptorCommand()
		descEntrypoint, descCommand := splitEntrypointCommand(descCmd)
		fmt.Fprintf(out, "Validating descriptor for %s...\n", pkg.Short())
		descData, err := runContainerCapture(ctx, runner, docker.Spec{Image: plan.Tag, Source: layout.PicoclawHome(), Volumes: integrationSetupBinds(layout), Entrypoint: descEntrypoint, Command: descCommand, Env: nil, Labels: map[string]string{docker.LabelWorkspace: dir}})
		if err != nil {
			return fmt.Errorf("describe integration %s: %w", pkg.Short(), err)
		}
		desc, err := picintegration.ParseDescriptor(descData)
		if err != nil {
			return fmt.Errorf("parse integration descriptor for %s: %w", pkg.Short(), err)
		}
		containerState := pkg.ContainerStatePath()
		planned[i] = manifest.Integration{
			Package:           pkg.Package(),
			Requested:         planned[i].Requested,
			Source:            string(picintegration.SourceRelease),
			Channel:           string(picintegration.ChannelLatest),
			Selector:          "latest",
			ResolvedVersion:   fetched.Version,
			ArtifactDigest:    fetched.Digest,
			StatePath:         pkg.StatePath(),
			Name:              desc.Name,
			DisplayName:       desc.DisplayName,
			Commands:          installedCommands(fetched.Bundle.Commands, desc.Commands),
			DescriptorCommand: descCmd,
			SetupCommand:      rewriteStateArgs(desc.Setup.Command, containerState),
			StatusCommand:     rewriteStateArgs(desc.StatusCommand, containerState),
			SetupRequiresTTY:  desc.Setup.RequiresTTY,
			SetupPorts:        desc.Setup.Ports,
			MCPServerKey:      desc.MCPServerKey,
		}
	}
	m.Container.Image = plan.Tag
	m.Runtime.Integrations = planned
	if err := m.Write(manifestPath); err != nil {
		return fmt.Errorf("update %s: %w", workspace.ManifestName, err)
	}
	fmt.Fprintf(out, "\n✓ Upgraded integrations\n")
	fmt.Fprintf(out, "  image: %s\n", plan.Tag)
	fmt.Fprintf(out, "\nNext:\n  piccolo restart\n")
	return nil
}

func runIntegrationSetup(cmd *cobra.Command, name string) error {
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
	m, err := manifest.Load(layout.Manifest())
	if err != nil {
		return err
	}
	var found *manifest.Integration
	for i := range m.Runtime.Integrations {
		if integrationMatches(m.Runtime.Integrations[i], name) {
			found = &m.Runtime.Integrations[i]
			break
		}
	}
	if found == nil {
		return fmt.Errorf("integration %q not installed — run 'piccolo integration add <owner/repo>' first", name)
	}
	if len(found.SetupCommand) == 0 {
		return fmt.Errorf("integration %q has no setup command", name)
	}
	if err := layout.EnsureDirs(); err != nil {
		return err
	}
	env, err := m.ResolveEnv(os.LookupEnv)
	if err != nil {
		return err
	}
	runner := newIntegrationRunner()
	pingCtx, cancel := context.WithTimeout(ctx, defaultImageBuildProbeTimeout)
	defer cancel()
	if err := runner.Ping(pingCtx); err != nil {
		return fmt.Errorf("docker daemon not reachable: %w", err)
	}
	// Declarative integrations are driven by Piccolo's own UI: describe first,
	// then dispatch on whether the integration declares actions. Presence of
	// actions is the selector, so an integration without them takes exactly the
	// legacy TTY path below, unchanged.
	if len(found.DescriptorCommand) > 0 {
		descEntrypoint, descCommand := splitEntrypointCommand(found.DescriptorCommand)
		descData, derr := runContainerCapture(ctx, runner, docker.Spec{
			Image:      m.Container.Image,
			Source:     layout.PicoclawHome(),
			Volumes:    integrationSetupBinds(layout),
			Entrypoint: descEntrypoint,
			Command:    descCommand,
			Labels:     map[string]string{docker.LabelWorkspace: dir},
		})
		if derr == nil {
			if desc, perr := picintegration.ParseDescriptor(descData); perr == nil && desc.Declarative() {
				return runDeclarativeSetup(ctx, out, runner, layout, dir, m, found, env, desc)
			}
		}
	}
	fmt.Fprintf(out, "Running setup for %s in a temporary container...\n", displayIntegration(*found))
	// Only request a container PTY when the host really has a terminal. With a
	// PTY the wizard reads from /dev/tty, so closing the attach stream on piped
	// stdin does NOT deliver EOF: the one-shot would block forever and leak the
	// container (holding its published ports). Without a host TTY we run the
	// integration's non-interactive path instead, which exits promptly.
	wantTTY := found.SetupRequiresTTY && term.IsTerminal(int(os.Stdin.Fd()))
	if found.SetupRequiresTTY && !wantTTY {
		fmt.Fprintf(out, "No terminal attached; running setup non-interactively.\n")
	}
	if removed, err := runner.RemoveByPrefix(ctx, docker.SetupContainerPrefix+sanitizeForName(found.Name)); err == nil && len(removed) > 0 {
		fmt.Fprintf(out, "Removed %d abandoned setup container(s): %s\n", len(removed), strings.Join(removed, ", "))
	}
	setupEntrypoint, setupCommand := splitEntrypointCommand(found.SetupCommand)
	spec := docker.Spec{
		Name:       setupContainerName(found.Name),
		Image:      m.Container.Image,
		Network:    m.Container.Network,
		Source:     layout.PicoclawHome(),
		Ports:      found.SetupPorts,
		Env:        env,
		Volumes:    integrationSetupBinds(layout),
		Entrypoint: setupEntrypoint,
		Command:    setupCommand,
		TTY:        wantTTY,
		Labels:     map[string]string{docker.LabelWorkspace: dir},
	}
	tty, err := enterInteractiveTerminal(wantTTY)
	if err != nil {
		return fmt.Errorf("prepare terminal for interactive setup: %w", err)
	}
	defer tty.Restore()

	proc, err := runner.Run(ctx, spec)
	if err != nil {
		return err
	}
	stopSync := syncPTYSize(ctx, proc, tty)
	defer stopSync()
	// Interrupts must cancel the wait so the one-shot is stopped and removed
	// rather than left running (it would otherwise squat on its ports forever).
	sigCtx, stopSignals := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	go func() { _, _ = io.Copy(proc.Stdin(), cmd.InOrStdin()); _ = proc.Stdin().Close() }()
	go func() { _, _ = io.Copy(out, proc.Stdout()) }()
	go func() { _, _ = io.Copy(cmd.ErrOrStderr(), proc.Stderr()) }()
	code, waitErr := proc.Wait(sigCtx)
	// Restore the host terminal before printing anything else, so the
	// follow-up hints render through the normal line discipline.
	tty.Restore()
	stopSync()
	shutdownErr := proc.Shutdown(0)
	if waitErr != nil {
		return waitErr
	}
	if shutdownErr != nil {
		return shutdownErr
	}
	if code != 0 {
		return fmt.Errorf("integration setup exited %d", code)
	}
	fmt.Fprintf(out, "\n✓ Integration setup complete\n\nNext:\n  piccolo restart\n")
	return nil
}

func runContainerCapture(ctx context.Context, runner integrationRunner, spec docker.Spec) ([]byte, error) {
	stdout, stderr, code, err := runContainerCaptureWithExitCode(ctx, runner, spec)
	if err != nil {
		return nil, err
	}
	if code != 0 {
		return nil, fmt.Errorf("command exited %d: %s", code, strings.TrimSpace(string(stderr)))
	}
	return stdout, nil
}

// runContainerCaptureWithExitCode captures a one-shot container's output and its
// exit code. A non-zero exit is NOT an error here: integrations legitimately
// exit non-zero to report failing checks (see 'piccolo integration status').
// Only transport-level failures (run/wait/shutdown) return an error.
func runContainerCaptureWithExitCode(ctx context.Context, runner integrationRunner, spec docker.Spec) ([]byte, []byte, int, error) {
	proc, err := runner.Run(ctx, spec)
	if err != nil {
		return nil, nil, 0, err
	}
	var stdout, stderr bytes.Buffer
	stdoutDone := make(chan struct{})
	stderrDone := make(chan struct{})
	// Cancel the wait on interrupt so the one-shot is always removed.
	sigCtx, stopSignals := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	go func() { _, _ = io.Copy(&stdout, proc.Stdout()); close(stdoutDone) }()
	go func() { _, _ = io.Copy(&stderr, proc.Stderr()); close(stderrDone) }()
	_ = proc.Stdin().Close()
	code, waitErr := proc.Wait(sigCtx)
	<-stdoutDone
	<-stderrDone
	shutdownErr := proc.Shutdown(0)
	if waitErr != nil {
		return nil, nil, 0, waitErr
	}
	if shutdownErr != nil {
		return nil, nil, 0, shutdownErr
	}
	return stdout.Bytes(), stderr.Bytes(), code, nil
}

func integrationSetupBinds(layout workspace.Layout) []string {
	// Integration binaries are installed into the image at build time through
	// release bundle's own runtime closure. Do not mount a host cache over /nix
	// for describe/setup one-shots: doing so hides the closure the image copied
	// in and breaks wrapper shebangs such as /nix/store/.../bin/bash. The
	// one-shot containers only need PicoClaw home (Spec.Source) plus the
	// integration state root.
	return []string{layout.IntegrationBind()}
}

// rejectSourceBuilds fails commands that would compose an image. Source-built
// integrations were Nix flakes; the image has no Nix any more, so the entry must
// be replaced rather than silently dropped from the plan.
func rejectSourceBuilds(m *manifest.Manifest, excludePackage string) error {
	return runtimeplan.RejectSourceBuilds(m.Runtime.Integrations, excludePackage)
}

func splitEntrypointCommand(cmd []string) ([]string, []string) {
	if len(cmd) == 0 {
		return nil, nil
	}
	return []string{cmd[0]}, append([]string(nil), cmd[1:]...)
}

func integrationBuildBase(m *manifest.Manifest) string {
	base := m.Container.Image
	if runtimeimage.IsLocalRuntimeImage(base) {
		base = runtimeimage.DefaultBaseImage
		for _, p := range m.Container.Ports {
			if strings.Contains(p, ":18800") {
				base = generate.LauncherImage
				break
			}
		}
	}
	return base
}

func descriptorCommandFor(item manifest.Integration) []string {
	if len(item.DescriptorCommand) > 0 {
		return append([]string(nil), item.DescriptorCommand...)
	}
	if len(item.Commands) > 0 {
		return []string{item.Commands[0], "describe", "--json"}
	}
	return nil
}

func fetchReleaseBundlesForManifest(ctx context.Context, fetcher releaseFetcher, in []manifest.Integration) ([]runtimeimage.IntegrationBundle, error) {
	bundles := []runtimeimage.IntegrationBundle{}
	for _, item := range in {
		if item.Source != string(picintegration.SourceRelease) {
			continue
		}
		pkg, err := packageRefForManifest(item)
		if err != nil {
			return nil, err
		}
		fetched, err := downloadResolvedRelease(ctx, fetcher, pkg)
		if err != nil {
			return nil, err
		}
		bundles = upsertBundle(bundles, fetched.Bundle)
	}
	return bundles, nil
}

// setupContainerName is deterministic per integration (no per-run timestamp).
// A stable name is what lets Piccolo detect and reclaim a one-shot container
// orphaned by an interrupted previous run instead of failing on resource
// conflicts forever.
func setupContainerName(name string) string {
	return docker.SetupContainerPrefix + sanitizeForName(name)
}

// statusContainerName follows the same deterministic rule for status one-shots,
// under its own sub-prefix so a status run never reclaims a setup run.
func statusContainerName(name string) string {
	return docker.SetupContainerPrefix + "status-" + sanitizeForName(name)
}

func sanitizeForName(name string) string {
	sanitized := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			return r
		}
		return '-'
	}, strings.TrimSpace(name))
	if sanitized == "" {
		return "integration"
	}
	return sanitized
}

// installedCommands returns the command list to record for an installed release
// integration. The image verifies the artifact release manifest's list, so that
// is the list the record must carry — anything else leaves the drift check
// comparing a plan the build could never produce, and no rebuild would clear it.
// The descriptor's list is the fallback for artifacts that declare none.
func installedCommands(bundleCommands []string, descriptorCommands picintegration.CommandList) []string {
	if len(bundleCommands) > 0 {
		return append([]string(nil), bundleCommands...)
	}
	return append([]string(nil), descriptorCommands...)
}

// upgradeHint names an integration the way every other user-facing command does
// -- by package reference, never by its short internal name -- so a remediation
// line cannot suggest a form Piccolo's own docs rule out.
func upgradeHint(in manifest.Integration) string {
	if pkg := strings.TrimSpace(in.Package); pkg != "" {
		return strings.TrimPrefix(pkg, "github:")
	}
	if ref := strings.TrimSpace(in.Ref); ref != "" {
		return ref
	}
	return in.Name
}

func displayIntegration(in manifest.Integration) string {
	if strings.TrimSpace(in.DisplayName) != "" {
		return in.DisplayName
	}
	return in.Name
}

func displayPackage(in manifest.Integration) string {
	if strings.HasPrefix(in.Package, "github:") {
		return strings.TrimPrefix(in.Package, "github:")
	}
	if in.Package != "" {
		return in.Package
	}
	if strings.HasPrefix(in.Ref, "github:") {
		ref := strings.TrimPrefix(in.Ref, "github:")
		if i := strings.IndexAny(ref, "?#"); i >= 0 {
			ref = ref[:i]
		}
		return ref
	}
	return in.Name
}

func valueOrDash(v string) string {
	if strings.TrimSpace(v) == "" {
		return "-"
	}
	return v
}

func integrationMatches(in manifest.Integration, input string) bool {
	input = strings.TrimSpace(input)
	if input == "" {
		return false
	}
	return input == in.Name || input == in.Package || input == displayPackage(in)
}

func upsertBundle(in []runtimeimage.IntegrationBundle, item runtimeimage.IntegrationBundle) []runtimeimage.IntegrationBundle {
	for i := range in {
		if in[i].Package == item.Package {
			in[i] = item
			return in
		}
	}
	return append(in, item)
}

func upsertManifestIntegration(in []manifest.Integration, item manifest.Integration) []manifest.Integration {
	itemKey := integrationPackageKey(item)
	for i := range in {
		if (itemKey != "" && integrationPackageKey(in[i]) == itemKey) || (itemKey == "" && item.Package == "" && in[i].Name == item.Name) {
			in[i] = item
			return in
		}
	}
	return append(in, item)
}

func integrationPackageKey(in manifest.Integration) string {
	if strings.TrimSpace(in.Package) != "" {
		pkg := strings.TrimSpace(in.Package)
		if i := strings.Index(pkg, "@"); i >= 0 {
			pkg = pkg[:i]
		}
		return pkg
	}
	if strings.HasPrefix(in.Ref, "github:") {
		ref := strings.TrimPrefix(in.Ref, "github:")
		if i := strings.IndexAny(ref, "?#@"); i >= 0 {
			ref = ref[:i]
		}
		if strings.Count(ref, "/") == 1 {
			return "github:" + ref
		}
	}
	return ""
}

func packageRefForManifest(in manifest.Integration) (picintegration.PackageRef, error) {
	if strings.TrimSpace(in.Package) != "" {
		ref := in.Package
		if in.Channel == string(picintegration.ChannelFixed) && in.Selector != "" && in.Selector != "latest" {
			ref += "@" + in.Selector
		}
		return picintegration.ParsePackageRef(ref)
	}
	return picintegration.ParsePackageRef(displayPackage(in))
}

func rewriteStateArgs(args []string, containerState string) []string {
	out := append([]string(nil), args...)
	for i, arg := range out {
		if strings.HasPrefix(arg, "/root/.piccolo/integrations/") {
			out[i] = containerState
		}
	}
	return out
}
