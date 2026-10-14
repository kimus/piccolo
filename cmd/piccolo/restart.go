package main

import (
	"github.com/spf13/cobra"
)

func newRestartCmd() *cobra.Command {
	var foreground bool
	cmd := &cobra.Command{
		Use:   "restart",
		Short: "Restart the gateway container, promoting a new image safely if one is configured",
		Long: "Recreates the gateway container, picking up config.json / piccolo.yaml changes. " +
			"When container.image names a different runtime image than the one running, the " +
			"current gateway is parked as a revert target first and the new image must pass a " +
			"readiness gate (health plus every installed integration actually executing) before it " +
			"is adopted; a failed gate restores the previous gateway. Use --foreground to attach " +
			"stdio and stop the container with Ctrl-C instead.",
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

// runRestart delegates to serve. It no longer stops the gateway first: that
// stop destroyed the only thing a failed promotion could fall back to, which
// turned a bad image into an outage. serve parks the running container as a
// revert target, gates the replacement, and reverts on failure.
func runRestart(cmd *cobra.Command, serve func(*cobra.Command) error) error {
	return serve(cmd)
}
