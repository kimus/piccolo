package main

import (
	"github.com/spf13/cobra"
)

// dirFlag is the shared -D/--dir workspace override (default: cwd).
var dirFlag string

// version is the piccolo build version (override with -ldflags
// "-X main.version=...").
var version = "0.1.0-poc"

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "piccolo",
		Short:         "Setup and launcher for PicoClaw multi-agent assistants",
		Long:          "Piccolo creates, validates, and runs a PicoClaw-powered multi-agent assistant in a Docker container.",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: false,
	}
	root.PersistentFlags().StringVarP(&dirFlag, "dir", "D", "", "workspace directory (default: current directory)")
	root.AddCommand(newInitCmd(), newSetupCmd(), newImageCmd(), newIntegrationCmd(), newRuntimeCmd(), newDoctorCmd(), newServeCmd(), newStopCmd(), newRestartCmd(), newReloadCmd(), newStatusCmd(), newLogsCmd(), newChatCmd())
	return root
}
