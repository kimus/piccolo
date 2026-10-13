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
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/kimus/piccolo/internal/docker"
	"github.com/kimus/piccolo/internal/generate"
	picintegration "github.com/kimus/piccolo/internal/integration"
	"github.com/kimus/piccolo/internal/manifest"
	"github.com/kimus/piccolo/internal/runtimeimage"
	"github.com/kimus/piccolo/internal/workspace"
)

type integrationFlags struct {
	yes  bool
	pull bool
	tag  string
}

type integrationUpgradeFlags struct {
	integrationFlags
	all bool
	to  string
}

type integrationStatusFlags struct {
	all  bool
	json bool
}

type integrationRunner interface {
	Ping(context.Context) error
	BuildImage(context.Context, io.Reader, docker.ImageBuildOptions, io.Writer) error
	Run(context.Context, docker.Spec) (docker.Process, error)
}

var newIntegrationRunner = func() integrationRunner { return docker.New() }

func newIntegrationCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "integration", Short: "Install and set up containerized integrations"}
	cmd.AddCommand(newIntegrationAddCmd(), newIntegrationSetupCmd(), newIntegrationUpgradeCmd(), newIntegrationStatusCmd())
	return cmd
}

func newIntegrationAddCmd() *cobra.Command {
	var f integrationFlags
	cmd := &cobra.Command{
		Use:   "add <alias-or-ref>",
		Short: "Build a gateway image with an integration installed",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runIntegrationAdd(cmd, args[0], f)
		},
	}
	cmd.Flags().BoolVar(&f.yes, "yes", false, "non-interactive: accept prompts")
	cmd.Flags().BoolVar(&f.pull, "pull", false, "ask Docker to pull a newer base image before building")
	cmd.Flags().StringVar(&f.tag, "tag", "", "local image tag to build (default: deterministic content-derived tag)")
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
			if f.all {
				if len(args) != 0 {
					return fmt.Errorf("use either 'piccolo integration upgrade <name>' or 'piccolo integration upgrade --all', not both")
				}
				if strings.TrimSpace(f.to) != "" {
					return fmt.Errorf("--to can only be used with a single integration name")
				}
				return nil
			}
			if len(args) != 1 {
				return fmt.Errorf("use either 'piccolo integration upgrade <name>' or 'piccolo integration upgrade --all'")
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
	cmd.Flags().BoolVar(&f.all, "all", false, "upgrade all installed integrations")
	cmd.Flags().StringVar(&f.to, "to", "", "alias or ref to use for the selected integration")
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
	resolved, err := picintegration.Resolve(input)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "Integration: %s\n", resolved.Name)
	fmt.Fprintf(out, "Source:      %s\n", resolved.Ref)
	if picintegration.IsUnpinned(resolved.Ref) {
		fmt.Fprintf(out, "[WARN] integration ref is unpinned; future rebuilds may resolve different code\n")
	}
	if !f.yes && term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Fprintf(out, "Build a new gateway image including this integration? [y/N] ")
		line, _ := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
		if !strings.EqualFold(strings.TrimSpace(line), "y") && !strings.EqualFold(strings.TrimSpace(line), "yes") {
			return errors.New("integration add cancelled")
		}
	}
	runner := newIntegrationRunner()
	pingCtx, cancel := context.WithTimeout(ctx, defaultImageBuildProbeTimeout)
	defer cancel()
	if err := runner.Ping(pingCtx); err != nil {
		return fmt.Errorf("docker daemon not reachable: %w", err)
	}

	base := integrationBuildBase(m)
	integrations := manifestIntegrationsForBuild(m.Runtime.Integrations)
	integrations = upsertBuildIntegration(integrations, runtimeimage.Integration{Ref: resolved.Ref, Commands: []string{resolved.DescriptorCommand[0]}})
	plan, err := runtimeimage.BuildPlan(runtimeimage.Options{BaseImage: base, Tag: f.tag, Packages: effectiveToolchain(m).Packages, Commands: effectiveToolchain(m).Commands, Integrations: integrations})
	if err != nil {
		return err
	}
	buildCtx, err := plan.ContextTar()
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "\nBuilding Piccolo runtime image with integrations\n")
	fmt.Fprintf(out, "  base: %s\n", plan.BaseImage)
	fmt.Fprintf(out, "  tag:  %s\n", plan.Tag)
	fmt.Fprintf(out, "  integration: %s\n\n", resolved.Ref)
	noCache := buildHasUnpinnedIntegrations(integrations)
	if noCache {
		fmt.Fprintf(out, "[WARN] disabling Docker build cache for unpinned integration refs so latest code is resolved\n")
	}
	if err := runner.BuildImage(ctx, buildCtx, docker.ImageBuildOptions{Tag: plan.Tag, Dockerfile: "Dockerfile", Pull: f.pull, Remove: true, NoCache: noCache}, out); err != nil {
		return fmt.Errorf("build image: %w", err)
	}
	if err := layout.EnsureDirs(); err != nil {
		return err
	}
	descEntrypoint, descCommand := splitEntrypointCommand(resolved.DescriptorCommand)
	descData, err := runContainerCapture(ctx, runner, docker.Spec{Image: plan.Tag, Source: layout.PicoclawHome(), Volumes: integrationSetupBinds(layout), Entrypoint: descEntrypoint, Command: descCommand, Env: nil, Labels: map[string]string{docker.LabelWorkspace: dir}})
	if err != nil {
		return fmt.Errorf("describe integration: %w", err)
	}
	desc, err := picintegration.ParseDescriptor(descData)
	if err != nil {
		return err
	}
	m.Container.Image = plan.Tag
	m.Runtime.Integrations = upsertManifestIntegration(m.Runtime.Integrations, manifest.Integration{
		Name:              desc.Name,
		DisplayName:       desc.DisplayName,
		Ref:               resolved.Ref,
		Commands:          []string(desc.Commands),
		DescriptorCommand: resolved.DescriptorCommand,
		SetupCommand:      desc.Setup.Command,
		StatusCommand:     desc.StatusCommand,
		SetupRequiresTTY:  desc.Setup.RequiresTTY,
		SetupPorts:        desc.Setup.Ports,
	})
	if err := m.Write(manifestPath); err != nil {
		return fmt.Errorf("update %s: %w", workspace.ManifestName, err)
	}
	fmt.Fprintf(out, "\n✓ Added integration: %s\n", desc.DisplayName)
	fmt.Fprintf(out, "  source: %s\n", resolved.Ref)
	fmt.Fprintf(out, "  image:  %s\n", plan.Tag)
	fmt.Fprintf(out, "\nNext:\n  piccolo integration setup %s\n  piccolo restart\n", desc.Name)
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
		return fmt.Errorf("no integrations installed — run 'piccolo integration add <alias-or-ref>' first")
	}
	selected := []manifest.Integration{}
	if f.all {
		selected = append(selected, m.Runtime.Integrations...)
	} else {
		for _, item := range m.Runtime.Integrations {
			if item.Name == name {
				selected = append(selected, item)
				break
			}
		}
		if len(selected) == 0 {
			return fmt.Errorf("integration %q is not installed — run 'piccolo integration add %s' first", name, name)
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
				Remediation: "Run 'piccolo integration upgrade " + item.Name + "' after installing a version that supports status.",
			}},
		}, nil
	}
	entrypoint, command := splitEntrypointCommand(item.StatusCommand)
	data, err := runContainerCapture(ctx, runner, docker.Spec{Image: image, Source: layout.PicoclawHome(), Volumes: integrationSetupBinds(layout), Entrypoint: entrypoint, Command: command, Env: nil, Labels: map[string]string{docker.LabelWorkspace: workspaceRoot}})
	if err != nil {
		return picintegration.StatusResult{}, fmt.Errorf("status integration %s: %w", item.Name, err)
	}
	res, err := picintegration.ParseStatus(data)
	if err != nil {
		return picintegration.StatusResult{}, fmt.Errorf("parse integration status for %s: %w", item.Name, err)
	}
	if res.Name != item.Name {
		return picintegration.StatusResult{}, fmt.Errorf("integration %q status reported name %q", item.Name, res.Name)
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
		return fmt.Errorf("use either 'piccolo integration upgrade <name>' or 'piccolo integration upgrade --all', not both")
	}
	if !f.all && strings.TrimSpace(name) == "" {
		return fmt.Errorf("use either 'piccolo integration upgrade <name>' or 'piccolo integration upgrade --all'")
	}
	if f.all && strings.TrimSpace(f.to) != "" {
		return fmt.Errorf("--to can only be used with a single integration name")
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
		return fmt.Errorf("no integrations installed — run 'piccolo integration add <alias-or-ref>' first")
	}

	upgradeAll := f.all
	targetIndex := -1
	var targetResolved picintegration.Resolved
	if !upgradeAll {
		for i := range m.Runtime.Integrations {
			if m.Runtime.Integrations[i].Name == name {
				targetIndex = i
				break
			}
		}
		if targetIndex < 0 {
			return fmt.Errorf("integration %q is not installed — run 'piccolo integration add %s' first", name, name)
		}
		if strings.TrimSpace(f.to) != "" {
			targetResolved, err = picintegration.Resolve(f.to)
			if err != nil {
				return err
			}
		}
	}

	planned := append([]manifest.Integration(nil), m.Runtime.Integrations...)
	if targetIndex >= 0 && strings.TrimSpace(f.to) != "" {
		planned[targetIndex].Ref = targetResolved.Ref
		planned[targetIndex].DescriptorCommand = targetResolved.DescriptorCommand
		planned[targetIndex].Commands = []string{targetResolved.DescriptorCommand[0]}
	}

	fmt.Fprintf(out, "Upgrading Piccolo integrations\n")
	if upgradeAll {
		fmt.Fprintf(out, "  target: all (%d installed)\n", len(planned))
	} else {
		fmt.Fprintf(out, "  target: %s\n", name)
	}
	for _, item := range planned {
		if picintegration.IsUnpinned(item.Ref) {
			fmt.Fprintf(out, "[WARN] integration ref for %s is unpinned; rebuild may resolve different code (%s)\n", item.Name, item.Ref)
		}
	}
	if !f.yes && term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Fprintf(out, "Build a new gateway image for this upgrade? [y/N] ")
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

	base := integrationBuildBase(m)
	plan, err := runtimeimage.BuildPlan(runtimeimage.Options{BaseImage: base, Tag: f.tag, Packages: effectiveToolchain(m).Packages, Commands: effectiveToolchain(m).Commands, Integrations: manifestIntegrationsForBuild(planned)})
	if err != nil {
		return err
	}
	buildCtx, err := plan.ContextTar()
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "\nBuilding Piccolo runtime image with upgraded integrations\n")
	fmt.Fprintf(out, "  base: %s\n", plan.BaseImage)
	fmt.Fprintf(out, "  tag:  %s\n", plan.Tag)
	for _, item := range planned {
		fmt.Fprintf(out, "  integration: %s (%s)\n", item.Name, item.Ref)
	}
	fmt.Fprintln(out)
	buildIntegrations := manifestIntegrationsForBuild(planned)
	noCache := buildHasUnpinnedIntegrations(buildIntegrations)
	if noCache {
		fmt.Fprintf(out, "[WARN] disabling Docker build cache for unpinned integration refs so latest code is resolved\n")
	}
	if err := runner.BuildImage(ctx, buildCtx, docker.ImageBuildOptions{Tag: plan.Tag, Dockerfile: "Dockerfile", Pull: f.pull, Remove: true, NoCache: noCache}, out); err != nil {
		return fmt.Errorf("build image: %w", err)
	}
	if err := layout.EnsureDirs(); err != nil {
		return err
	}

	refreshIndexes := []int{}
	if upgradeAll {
		for i := range planned {
			refreshIndexes = append(refreshIndexes, i)
		}
	} else {
		refreshIndexes = append(refreshIndexes, targetIndex)
	}
	refreshed := append([]manifest.Integration(nil), planned...)
	for _, i := range refreshIndexes {
		item := refreshed[i]
		descCmd := descriptorCommandFor(item)
		if len(descCmd) == 0 {
			return fmt.Errorf("integration %q has no descriptor command metadata", item.Name)
		}
		descEntrypoint, descCommand := splitEntrypointCommand(descCmd)
		fmt.Fprintf(out, "Validating descriptor for %s...\n", item.Name)
		descData, err := runContainerCapture(ctx, runner, docker.Spec{Image: plan.Tag, Source: layout.PicoclawHome(), Volumes: integrationSetupBinds(layout), Entrypoint: descEntrypoint, Command: descCommand, Env: nil, Labels: map[string]string{docker.LabelWorkspace: dir}})
		if err != nil {
			return fmt.Errorf("describe integration %s: %w", item.Name, err)
		}
		desc, err := picintegration.ParseDescriptor(descData)
		if err != nil {
			return fmt.Errorf("parse integration descriptor for %s: %w", item.Name, err)
		}
		if desc.Name != item.Name {
			return fmt.Errorf("integration %q descriptor reported name %q; refusing to replace state owned by %q", item.Name, desc.Name, item.Name)
		}
		refreshed[i] = manifest.Integration{
			Name:              desc.Name,
			DisplayName:       desc.DisplayName,
			Ref:               item.Ref,
			Commands:          []string(desc.Commands),
			DescriptorCommand: descCmd,
			SetupCommand:      desc.Setup.Command,
			StatusCommand:     desc.StatusCommand,
			SetupRequiresTTY:  desc.Setup.RequiresTTY,
			SetupPorts:        desc.Setup.Ports,
		}
	}

	m.Container.Image = plan.Tag
	m.Runtime.Integrations = refreshed
	if err := m.Write(manifestPath); err != nil {
		return fmt.Errorf("update %s: %w", workspace.ManifestName, err)
	}
	fmt.Fprintf(out, "\n✓ Upgraded integrations\n")
	fmt.Fprintf(out, "  image: %s\n", plan.Tag)
	if upgradeAll {
		fmt.Fprintf(out, "\nNext:\n  piccolo restart\n  piccolo integration setup <name>  # optional, only if reconfiguration is needed\n")
	} else {
		fmt.Fprintf(out, "\nNext:\n  piccolo restart\n  piccolo integration setup %s  # optional, only if reconfiguration is needed\n", name)
	}
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
		if m.Runtime.Integrations[i].Name == name {
			found = &m.Runtime.Integrations[i]
			break
		}
	}
	if found == nil {
		return fmt.Errorf("integration %q not installed — run 'piccolo integration add %s' first", name, name)
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
	fmt.Fprintf(out, "Running setup for %s in a temporary container...\n", displayIntegration(*found))
	setupEntrypoint, setupCommand := splitEntrypointCommand(found.SetupCommand)
	spec := docker.Spec{
		Name:       temporarySetupContainerName(found.Name),
		Image:      m.Container.Image,
		Network:    m.Container.Network,
		Source:     layout.PicoclawHome(),
		Ports:      found.SetupPorts,
		Env:        env,
		Volumes:    integrationSetupBinds(layout),
		Entrypoint: setupEntrypoint,
		Command:    setupCommand,
		TTY:        found.SetupRequiresTTY,
		Labels:     map[string]string{docker.LabelWorkspace: dir},
	}
	proc, err := runner.Run(ctx, spec)
	if err != nil {
		return err
	}
	go func() { _, _ = io.Copy(proc.Stdin(), cmd.InOrStdin()); _ = proc.Stdin().Close() }()
	go func() { _, _ = io.Copy(out, proc.Stdout()) }()
	go func() { _, _ = io.Copy(cmd.ErrOrStderr(), proc.Stderr()) }()
	code, waitErr := proc.Wait(ctx)
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
	proc, err := runner.Run(ctx, spec)
	if err != nil {
		return nil, err
	}
	var stdout, stderr bytes.Buffer
	stdoutDone := make(chan struct{})
	stderrDone := make(chan struct{})
	go func() { _, _ = io.Copy(&stdout, proc.Stdout()); close(stdoutDone) }()
	go func() { _, _ = io.Copy(&stderr, proc.Stderr()); close(stderrDone) }()
	_ = proc.Stdin().Close()
	code, waitErr := proc.Wait(ctx)
	<-stdoutDone
	<-stderrDone
	shutdownErr := proc.Shutdown(0)
	if waitErr != nil {
		return nil, waitErr
	}
	if shutdownErr != nil {
		return nil, shutdownErr
	}
	if code != 0 {
		return nil, fmt.Errorf("command exited %d: %s", code, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

func integrationSetupBinds(layout workspace.Layout) []string {
	// Integration binaries are installed into the image at build time through
	// Devbox/Nix. Do not mount the workspace runtime/nix cache over /nix for
	// describe/setup one-shots: doing so hides the image's build-time Nix store
	// and breaks Nix wrapper shebangs such as /nix/store/.../bin/bash. The
	// one-shot containers only need PicoClaw home (Spec.Source) plus the
	// integration state root.
	return []string{layout.IntegrationBind()}
}

func splitEntrypointCommand(cmd []string) ([]string, []string) {
	if len(cmd) == 0 {
		return nil, nil
	}
	return []string{cmd[0]}, append([]string(nil), cmd[1:]...)
}

func buildHasUnpinnedIntegrations(in []runtimeimage.Integration) bool {
	for _, item := range in {
		if picintegration.IsUnpinned(item.Ref) {
			return true
		}
	}
	return false
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

func manifestIntegrationsForBuild(in []manifest.Integration) []runtimeimage.Integration {
	out := make([]runtimeimage.Integration, 0, len(in))
	for _, item := range in {
		cmds := append([]string(nil), item.Commands...)
		if len(cmds) == 0 && len(item.DescriptorCommand) > 0 {
			cmds = []string{item.DescriptorCommand[0]}
		}
		out = append(out, runtimeimage.Integration{Ref: item.Ref, Commands: cmds})
	}
	return out
}

func upsertBuildIntegration(in []runtimeimage.Integration, item runtimeimage.Integration) []runtimeimage.Integration {
	for i := range in {
		if in[i].Ref == item.Ref {
			in[i].Commands = append(in[i].Commands, item.Commands...)
			return in
		}
	}
	return append(in, item)
}

func upsertManifestIntegration(in []manifest.Integration, item manifest.Integration) []manifest.Integration {
	for i := range in {
		if in[i].Name == item.Name {
			in[i] = item
			return in
		}
	}
	return append(in, item)
}

func temporarySetupContainerName(name string) string {
	return fmt.Sprintf("piccolo-setup-%s-%d", name, time.Now().UnixNano())
}

func displayIntegration(in manifest.Integration) string {
	if strings.TrimSpace(in.DisplayName) != "" {
		return in.DisplayName
	}
	return in.Name
}
