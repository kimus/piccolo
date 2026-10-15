package main

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/kimus/piccolo/internal/docker"
	"github.com/kimus/piccolo/internal/manifest"
	"github.com/kimus/piccolo/internal/runtimeimage"
	"github.com/kimus/piccolo/internal/workspace"
)

func newRuntimeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "runtime",
		Short: "Inspect the Piccolo runtime toolchain",
	}
	cmd.AddCommand(newRuntimeStatusCmd(), newRuntimeApplyCmd())
	return rejectUnknownArgs(cmd)
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
		Short: "Report whether the running gateway already has the configured toolchain",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRuntimeApply(cmd)
		},
	}
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

// runRuntimeApply no longer installs anything. The toolchain is baked into the
// runtime image, so a package-list change is an IMAGE change: installing into a
// running container would create a second source of truth for one Nix store and
// leave the next start with nothing. Piccolo compares and points at the rebuild
// instead of performing it — a config edit must not silently cost a build.
func runRuntimeApply(cmd *cobra.Command) error {
	out := cmd.OutOrStdout()
	_, m, tc, err := runtimeManifest()
	if err != nil {
		return err
	}
	r := docker.New()
	ctx := cmd.Context()

	labels, found, err := r.ImageLabels(ctx, m.Container.Image)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("runtime image %s is not present locally — rebuild it: piccolo image build --set", m.Container.Image)
	}
	want := map[string]string{}
	if plan, perr := desiredRuntimePlan(m); perr == nil {
		want = plan.Labels()
	}
	if got := labels[runtimeimage.LabelToolchain]; got != want[runtimeimage.LabelToolchain] {
		fmt.Fprintf(out, "Runtime toolchain is NOT in the running image\n")
		fmt.Fprintf(out, "  image:     %s (%s=%s)\n", m.Container.Image, runtimeimage.LabelToolchain, valueOrDash(got))
		fmt.Fprintf(out, "  wanted:    %s\n", want[runtimeimage.LabelToolchain])
		fmt.Fprintf(out, "  packages:  %s\n", strings.Join(tc.Packages, ", "))
		fmt.Fprintf(out, "  commands:  %s\n", strings.Join(tc.Commands, ", "))
		fmt.Fprintf(out, "\nToolchain changes are image changes. Rebuild when you choose:\n  piccolo image build --set && piccolo restart\n")
		return fmt.Errorf("runtime toolchain does not match the running image")
	}
	fmt.Fprintf(out, "\u2713 running image %s matches the configured toolchain (%s)\n", m.Container.Image, want[runtimeimage.LabelToolchain])
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
