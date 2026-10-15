package main

import (
	"fmt"
	"github.com/kimus/piccolo/internal/generate"
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
	version   = "0.3.0-dev"
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

// rejectUnknownArgs makes a command GROUP fail when an argument does not select
// a subcommand. Cobra's default for a non-runnable parent is to print help and
// exit 0 — and it does that before any Args hook runs — so a typo like
// `piccolo integratio add x` "succeeds" while doing nothing. That is the worst
// outcome for a script, and it is also how a removed subcommand looks when it
// simply no longer exists.
//
// Making the group runnable is the only way in: bare invocation still prints
// help, an unmatched argument becomes an error.
func rejectUnknownArgs(cmd *cobra.Command) *cobra.Command {
	cmd.Args = cobra.ArbitraryArgs
	cmd.SilenceUsage = true
	cmd.RunE = func(c *cobra.Command, args []string) error {
		if len(args) > 0 {
			suggest := ""
			if near := nearestSubcommand(c, args[0]); near != "" {
				suggest = ", did you mean '" + c.CommandPath() + " " + near + "'?"
			}
			return fmt.Errorf("unknown command %q for %q%s\nRun '%s --help' to list subcommands", args[0], c.CommandPath(), suggest, c.CommandPath())
		}
		return c.Help()
	}
	return cmd
}

// nearestSubcommand offers a suggestion when the typo is close to a real verb.
// A shared prefix alone is not enough: "bulid" and "build" share two characters,
// which is exactly the case a user needs help with, so edit distance decides.
func nearestSubcommand(cmd *cobra.Command, given string) string {
	given = strings.ToLower(strings.TrimSpace(given))
	if len(given) < 3 {
		return ""
	}
	best, bestDist := "", len(given)+1
	// Hidden commands are candidates on purpose: a retired verb stays hidden from
	// the listing but is still the closest match for a typo of it, and following
	// the suggestion lands the user on the removal message instead of silence.
	for _, sub := range cmd.Commands() {
		name := strings.ToLower(sub.Name())
		if name == given {
			return sub.Name()
		}
		d := editDistance(name, given)
		if d < bestDist {
			best, bestDist = sub.Name(), d
		}
	}
	if bestDist <= 2 {
		return best
	}
	return ""
}

func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(min(cur[j-1]+1, prev[j]+1), prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

func newRootCmd() *cobra.Command {
	// Stamp the generation package so a workspace record names the Piccolo that
	// wrote it. Done here rather than in main() so the value is set for every
	// construction of the command tree, including tests.
	generate.SetVersion(version)
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
