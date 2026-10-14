package main

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/kimus/piccolo/internal/docker"
)

// A promotion must never leave a workspace with no gateway. The old sequence
// stopped and removed the running container first, then discovered whether the
// replacement was healthy — so a bad image was an outage with no way back.
//
// The sequence here parks the current container as a revert target, starts the
// target image under the canonical name, and gates it. Every step it relies on
// was verified against the daemon: stopping releases the published port, a
// differently named container can bind the same port, and renaming a RUNNING
// container keeps its port bindings, so the winner takes the canonical name
// without a second outage.
type promoter interface {
	StateByName(ctx context.Context, name string) (docker.State, error)
	StopKeptByName(ctx context.Context, name string, grace time.Duration) (bool, error)
	RenameContainer(ctx context.Context, from, to string) error
	RemoveByName(ctx context.Context, name string, force bool) (bool, error)
	StartByName(ctx context.Context, name string) (bool, error)
}

// gatewaySwap holds the two names a promotion works between.
type gatewaySwap struct {
	runner    promoter
	out       io.Writer
	canonical string
	previous  string
	grace     time.Duration
	// parkedImage records what the revert target was running, empty when
	// nothing was parked.
	parkedImage string
}

func newGatewaySwap(runner promoter, out io.Writer, canonical string) *gatewaySwap {
	return &gatewaySwap{
		runner:    runner,
		out:       out,
		canonical: canonical,
		previous:  docker.PreviousName(canonical),
		grace:     stopGrace,
	}
}

// parked reports whether a revert target exists.
func (s *gatewaySwap) parked() bool { return s.parkedImage != "" }

// recover repairs an interrupted promotion. Because the parked container always
// carries the reserved name, there is exactly one abnormal state to look for:
// the reserved name exists while the canonical name does not.
func (s *gatewaySwap) recover(ctx context.Context) error {
	prev, err := s.runner.StateByName(ctx, s.previous)
	if err != nil {
		return err
	}
	if !prev.Found {
		return nil
	}
	cur, err := s.runner.StateByName(ctx, s.canonical)
	if err != nil {
		return err
	}
	if cur.Found {
		// Service is up; the leftover is evidence of an older interrupted
		// promote, not an outage. Disturbing a running gateway to tidy up would
		// be the worse outcome, so report it and let park() replace it.
		fmt.Fprintf(s.out, "[WARN] leftover promotion target %q exists (image %s, %s); it will be replaced on the next promote\n",
			s.previous, orUnknown(prev.Image), orUnknown(prev.Status))
		return nil
	}
	fmt.Fprintf(s.out, "Recovering a gateway left mid-promotion: restoring %q ...\n", s.previous)
	if err := s.restorePrevious(ctx); err != nil {
		return err
	}
	if _, err := s.runner.StartByName(ctx, s.canonical); err != nil {
		return fmt.Errorf("restore after interrupted promotion: %w", err)
	}
	fmt.Fprintf(s.out, "  \u2713 previous gateway restored (%s) — the promotion that was interrupted did not complete\n", orUnknown(prev.Image))
	return nil
}

// park stops the current gateway and moves it to the reserved name. Stopping
// releases the published port the replacement needs; renaming frees the
// canonical name, which a stopped container would otherwise keep holding.
func (s *gatewaySwap) park(ctx context.Context) error {
	cur, err := s.runner.StateByName(ctx, s.canonical)
	if err != nil {
		return err
	}
	if !cur.Found {
		return nil
	}
	if _, err := s.runner.StopKeptByName(ctx, s.canonical, s.grace); err != nil {
		return fmt.Errorf("stop gateway for promotion: %w", err)
	}
	// A leftover parked container from an older interrupted promote cannot
	// share the reserved name; drop it now, before anything depends on it.
	if prev, err := s.runner.StateByName(ctx, s.previous); err == nil && prev.Found {
		if _, err := s.runner.RemoveByName(ctx, s.previous, true); err != nil {
			return err
		}
	}
	if err := s.runner.RenameContainer(ctx, s.canonical, s.previous); err != nil {
		return fmt.Errorf("park gateway %q as revert target: %w", s.canonical, err)
	}
	s.parkedImage = cur.Image
	return nil
}

// restorePrevious renames the parked container back to the canonical name and
// consumes the parked state, so a completed revert leaves no reserved name.
func (s *gatewaySwap) restorePrevious(ctx context.Context) error {
	if err := s.runner.RenameContainer(ctx, s.previous, s.canonical); err != nil {
		return fmt.Errorf("restore parked gateway name: %w", err)
	}
	s.parkedImage = ""
	return nil
}

// commit finalizes a promotion: the revert target is no longer needed.
func (s *gatewaySwap) commit(ctx context.Context) error {
	if !s.parked() {
		return nil
	}
	if _, err := s.runner.RemoveByName(ctx, s.previous, true); err != nil {
		return fmt.Errorf("promotion succeeded but the old container could not be removed: %w", err)
	}
	s.parkedImage = ""
	return nil
}

// gateFailure carries what the readiness gate observed, so it can be reported
// before the container that produced it is deleted.
type gateFailure struct {
	Stage   string // "health" | "integration"
	Reason  string
	Details []string
}

func (g gateFailure) Error() string { return g.Reason }

// revert abandons the promotion: capture first, then delete the candidate and
// start the previous gateway again. Restoring the parked CONTAINER (rather than
// recreating from the manifest) is deliberate — it brings back the exact image,
// mounts, env and port configuration that was known good, which is what "go
// back to what was working" means, while the manifest may have drifted since.
//
// Diagnostics are printed before the removal because the deleted container is
// otherwise the only record of why the promote failed. It returns the image the
// workspace is running again, or an error when NEITHER gateway is up — which is
// reported as such instead of a rollback that did not happen.
func (s *gatewaySwap) revert(ctx context.Context, failure gateFailure, logs func(context.Context, string) string) (string, error) {
	fmt.Fprintf(s.out, "\n[FAIL] promotion failed (%s): %s\n", failure.Stage, failure.Reason)
	for _, line := range failure.Details {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fmt.Fprintf(s.out, "    %s\n", line)
	}
	if tail := logs(ctx, "30"); tail != "" {
		fmt.Fprintf(s.out, "\n  candidate gateway logs (last lines before removal):\n")
		for _, line := range strings.Split(strings.TrimRight(tail, "\n"), "\n") {
			fmt.Fprintf(s.out, "    %s\n", line)
		}
	}

	if _, err := s.runner.RemoveByName(ctx, s.canonical, true); err != nil {
		return "", fmt.Errorf("promotion failed AND the candidate could not be removed: %w", err)
	}
	// Captured before restorePrevious clears it: this is what the workspace is
	// running again, and the report has to name it.
	restored := s.parkedImage
	if err := s.restorePrevious(ctx); err != nil {
		return "", err
	}
	if _, err := s.runner.StartByName(ctx, s.canonical); err != nil {
		return "", fmt.Errorf("promotion failed AND the previous gateway could not be restored: %w", err)
	}
	return restored, nil
}

// orUnknown keeps a report honest about what it does not know: a value the
// daemon never reported prints as "unknown" rather than an empty gap that reads
// like "none".
func orUnknown(v string) string {
	if strings.TrimSpace(v) == "" {
		return "unknown"
	}
	return v
}
