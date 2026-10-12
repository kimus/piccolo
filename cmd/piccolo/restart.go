package main

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

func newRestartCmd() *cobra.Command {
	var foreground bool
	cmd := &cobra.Command{
		Use:   "restart",
		Short: "Restart the gateway container (stop, then serve)",
		Long: "Stops and removes the running gateway container (if any) and starts a " +
			"fresh one, picking up config.json / piccolo.yaml changes. Use this after " +
			"'piccolo setup' edits to apply new configuration.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if foreground {
				return runRestart(cmd, func(c *cobra.Command) error { return runServeForeground(c) })
			}
			return runRestart(cmd, runServeDetached)
		},
	}
	cmd.Flags().BoolVarP(&foreground, "foreground", "f", false, "attach stdio in the foreground (Ctrl-C stops the container)")
	return cmd
}

// runRestart stops the current container gracefully, then re-serves via the
// given serve function. A container that is not running is not an error.
func runRestart(cmd *cobra.Command, serve func(*cobra.Command) error) error {
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
	if found {
		fmt.Fprintf(cmd.OutOrStdout(), "\u2713 stopped %s\n", name)
	} else {
		fmt.Fprintf(cmd.OutOrStdout(), "gateway container %q was not running\n", name)
	}
	return serve(cmd)
}
