package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/kimus/piccolo/internal/docker"
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

type integrationRunner interface {
	Ping(context.Context) error
	BuildImage(context.Context, io.Reader, docker.ImageBuildOptions, io.Writer) error
	Run(context.Context, docker.Spec) (docker.Process, error)
}

var newIntegrationRunner = func() integrationRunner { return docker.New() }

func newIntegrationCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "integration", Short: "Install and set up containerized integrations"}
	cmd.AddCommand(newIntegrationAddCmd(), newIntegrationSetupCmd())
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
	manifestPath := filepath.Join(dir, workspace.ManifestName)
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

	base := m.Container.Image
	if runtimeimage.IsLocalRuntimeImage(base) {
		base = runtimeimage.DefaultBaseImage
	}
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
	if err := runner.BuildImage(ctx, buildCtx, docker.ImageBuildOptions{Tag: plan.Tag, Dockerfile: "Dockerfile", Pull: f.pull, Remove: true}, out); err != nil {
		return fmt.Errorf("build image: %w", err)
	}
	descEntrypoint, descCommand := splitEntrypointCommand(resolved.DescriptorCommand)
	descData, err := runContainerCapture(ctx, runner, docker.Spec{Image: plan.Tag, Source: dir, Entrypoint: descEntrypoint, Command: descCommand, Env: nil, Labels: map[string]string{docker.LabelWorkspace: dir}})
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
	m, err := manifest.Load(filepath.Join(dir, workspace.ManifestName))
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
		Source:     dir,
		Ports:      found.SetupPorts,
		Env:        env,
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

func splitEntrypointCommand(cmd []string) ([]string, []string) {
	if len(cmd) == 0 {
		return nil, nil
	}
	return []string{cmd[0]}, append([]string(nil), cmd[1:]...)
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
