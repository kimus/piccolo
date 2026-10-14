package main

import (
	"github.com/spf13/cobra"
	"runtime"
	"strings"
)

// dirFlag is the shared -D/--dir workspace override (default: cwd).
var dirFlag string

// version and buildDate are stamped at link time:
//
//	go build -ldflags "-X main.version=$(git describe --tags) -X main.buildDate=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
//
// A plain `go build` leaves them at these defaults, and formatVersion() says so
// out loud instead of pretending to be a release build. That matters because a
// stale local binary has actually confused real usage: the running ./piccolo can
// be older than the source it was built from.
var (
	version   = "0.1.0-poc"
	buildDate = "unknown"
)

// formatVersion renders the --version output, flagging unstamped dev builds.
func formatVersion() string {
	switch strings.TrimSpace(buildDate) {
	case "", "unknown":
		return version + " (dev build, not stamped; pass -ldflags to stamp a build date)"
	default:
		return version + " (built " + buildDate + ", " + runtime.Version() + ")"
	}
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "piccolo",
		Short:         "Setup and launcher for PicoClaw multi-agent assistants",
		Long:          "Piccolo creates, validates, and runs a PicoClaw-powered multi-agent assistant in a Docker container.",
		Version:       formatVersion(),
		SilenceUsage:  true,
		SilenceErrors: false,
	}
	root.PersistentFlags().StringVarP(&dirFlag, "dir", "D", "", "workspace directory (default: current directory)")
	root.AddCommand(newInitCmd(), newSetupCmd(), newImageCmd(), newIntegrationCmd(), newRuntimeCmd(), newWorkspaceCmd(), newDoctorCmd(), newServeCmd(), newStopCmd(), newRestartCmd(), newReloadCmd(), newStatusCmd(), newLogsCmd(), newChatCmd())
	return root
}
