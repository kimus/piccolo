package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/kimus/piccolo/internal/docker"
	"github.com/kimus/piccolo/internal/manifest"
	"github.com/kimus/piccolo/internal/runtimeimage"
	"github.com/kimus/piccolo/internal/workspace"
)

type runtimeFlags struct {
	yes bool
}

func newRuntimeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "runtime",
		Short: "Inspect and maintain the Piccolo runtime toolchain",
	}
	cmd.AddCommand(newRuntimeStatusCmd(), newRuntimeApplyCmd(), newRuntimePruneCmd(), newRuntimeResetCacheCmd())
	return cmd
}

func newRuntimeStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show configured runtime toolchain and live command availability",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRuntimeStatus(cmd)
		},
	}
}

func newRuntimeApplyCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "apply",
		Short: "Apply the configured runtime toolchain to the running gateway container",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRuntimeApply(cmd)
		},
	}
}

func newRuntimePruneCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "prune",
		Short: "Attempt safe Devbox/Nix garbage collection in the running gateway container",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRuntimePrune(cmd)
		},
	}
}

func newRuntimeResetCacheCmd() *cobra.Command {
	var f runtimeFlags
	cmd := &cobra.Command{
		Use:   "reset-cache",
		Short: "Remove Piccolo Devbox/Nix cache volumes (destructive; reinstall on next serve)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRuntimeResetCache(cmd, f)
		},
	}
	cmd.Flags().BoolVar(&f.yes, "yes", false, "confirm destructive volume removal")
	return cmd
}

func runtimeManifest() (string, *manifest.Manifest, manifest.Toolchain, error) {
	dir, err := workspace.Require(dirFlag)
	if err != nil {
		return "", nil, manifest.Toolchain{}, err
	}
	m, err := manifest.Load(workspace.NewLayout(dir).Manifest())
	if err != nil {
		return "", nil, manifest.Toolchain{}, err
	}
	return dir, m, effectiveToolchain(m), nil
}

func runRuntimeStatus(cmd *cobra.Command) error {
	out := cmd.OutOrStdout()
	_, m, tc, err := runtimeManifest()
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "Runtime toolchain\n")
	fmt.Fprintf(out, "  image:    %s\n", m.Container.Image)
	fmt.Fprintf(out, "  packages: %s\n", strings.Join(tc.Packages, ", "))
	fmt.Fprintf(out, "  commands: %s\n", strings.Join(tc.Commands, ", "))

	r := docker.New()
	ctx := cmd.Context()
	st, err := r.StateByName(ctx, m.Container.Name)
	if err != nil {
		return err
	}
	if !st.Found || !st.Running {
		fmt.Fprintf(out, "  live:     container not running; command availability not checked\n")
		return nil
	}
	stdout, stderr, code, err := runtimeExecCapture(ctx, r, m.Container.Name, []string{"sh", "-lc", runtimeCommandCheckScript(tc.Commands)})
	if err != nil {
		return err
	}
	if stderr != "" {
		fmt.Fprint(out, stderr)
	}
	if code != 0 {
		fmt.Fprintf(out, "  live:     command check exited %d\n", code)
	}
	fmt.Fprint(out, stdout)
	return nil
}

func runRuntimeApply(cmd *cobra.Command) error {
	out := cmd.OutOrStdout()
	_, m, tc, err := runtimeManifest()
	if err != nil {
		return err
	}
	r := docker.New()
	ctx := cmd.Context()
	st, err := r.StateByName(ctx, m.Container.Name)
	if err != nil {
		return err
	}
	if !st.Found || !st.Running {
		return fmt.Errorf("container %q is not running — run 'piccolo serve' or 'piccolo restart' first", m.Container.Name)
	}
	applyCtx, cancel := context.WithTimeout(ctx, devboxInstallTimeout)
	defer cancel()
	fmt.Fprintf(out, "Applying runtime toolchain to %s ...\n", m.Container.Name)
	stdout, stderr, code, err := runtimeExecCapture(applyCtx, r, m.Container.Name, []string{runtimeimage.SetupScriptPath, strings.Join(tc.Packages, " "), strings.Join(tc.Commands, " ")})
	if stdout != "" {
		fmt.Fprint(out, stdout)
	}
	if stderr != "" {
		fmt.Fprint(out, stderr)
	}
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("runtime toolchain apply exited %d", code)
	}
	fmt.Fprintln(out, "✓ runtime tools ready")
	return nil
}

func runRuntimePrune(cmd *cobra.Command) error {
	out := cmd.OutOrStdout()
	_, m, _, err := runtimeManifest()
	if err != nil {
		return err
	}
	r := docker.New()
	ctx := cmd.Context()
	st, err := r.StateByName(ctx, m.Container.Name)
	if err != nil {
		return err
	}
	if !st.Found || !st.Running {
		return fmt.Errorf("container %q is not running — run 'piccolo serve' first, or use 'piccolo runtime reset-cache --yes' for a full reset", m.Container.Name)
	}
	pruneCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	fmt.Fprintf(out, "Pruning runtime cache in %s ...\n", m.Container.Name)
	stdout, stderr, code, err := runtimeExecCapture(pruneCtx, r, m.Container.Name, []string{"sh", "-lc", "nix-collect-garbage -d 2>/dev/null || nix store gc 2>/dev/null || { echo 'Nix garbage collection is unavailable in this runtime' >&2; exit 3; }"})
	if stdout != "" {
		fmt.Fprint(out, stdout)
	}
	if stderr != "" {
		fmt.Fprint(out, stderr)
	}
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("runtime prune exited %d", code)
	}
	fmt.Fprintln(out, "✓ runtime cache prune complete")
	return nil
}

func runRuntimeResetCache(cmd *cobra.Command, f runtimeFlags) error {
	out := cmd.OutOrStdout()
	dir, err := workspace.Require(dirFlag)
	if err != nil {
		return err
	}
	layout := workspace.NewLayout(dir)
	if !f.yes {
		return fmt.Errorf("reset-cache is destructive; re-run with --yes to remove %s", layout.Runtime())
	}
	for _, path := range []string{layout.RuntimeDevbox(), layout.RuntimeNix(), layout.RuntimeMedia()} {
		if err := os.RemoveAll(path); err != nil {
			fmt.Fprintf(out, "  [WARN] remove %s: %v\n", path, err)
			continue
		}
		if err := os.MkdirAll(path, 0o755); err != nil {
			fmt.Fprintf(out, "  [WARN] recreate %s: %v\n", path, err)
			continue
		}
		fmt.Fprintf(out, "  reset %s\n", path)
	}
	fmt.Fprintln(out, "Runtime cache reset. Next 'piccolo serve' or 'piccolo restart' will reinstall the configured toolchain.")
	return nil
}

func runtimeExecCapture(ctx context.Context, r *docker.Runner, name string, command []string) (string, string, int, error) {
	session, err := r.Exec(ctx, name, command, false)
	if err != nil {
		return "", "", -1, err
	}
	defer session.Close()
	var stdout, stderr bytes.Buffer
	_ = docker.Demux(session.Reader, &stdout, &stderr)
	code, err := session.Wait(ctx)
	return stdout.String(), stderr.String(), code, err
}

func runtimeCommandCheckScript(commands []string) string {
	var b strings.Builder
	b.WriteString("ok=0; miss=0\n")
	for _, c := range commands {
		b.WriteString("if command -v ")
		b.WriteString(shellQuote(c))
		b.WriteString(" >/dev/null 2>&1; then echo '  [OK]   command: ")
		b.WriteString(c)
		b.WriteString("'; ok=$((ok+1)); else echo '  [WARN] command missing: ")
		b.WriteString(c)
		b.WriteString("'; miss=$((miss+1)); fi\n")
	}
	b.WriteString("echo \"  live:     $ok available, $miss missing\"\n")
	return b.String()
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}
