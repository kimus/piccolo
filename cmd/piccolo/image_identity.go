package main

import (
	"context"
	"fmt"
	"io"

	"github.com/kimus/piccolo/internal/manifest"
	"github.com/kimus/piccolo/internal/runtimeimage"
	"github.com/kimus/piccolo/internal/runtimeplan"
)

// The desired-image question is answered in internal/runtimeplan from metadata
// only. This file is the presentation layer: it turns a drift report into words
// and a command, and never into a build.

// desiredRuntimePlan composes the runtime image the workspace currently
// describes. Root stays empty on purpose: a plan built for comparison is never
// tarred, and that is the compile-time guarantee that no artifact was downloaded
// to answer this question.
func desiredRuntimePlan(m *manifest.Manifest) (runtimeimage.Plan, error) {
	return runtimeplan.Plan(m)
}

// warnImageDrift reports a stale runtime image and names the rebuild command.
// It never builds anything: a config edit must not silently cost minutes of
// building, and the reason the inputs moved is exactly what the user needs to
// see before spending that build. A comparison that cannot be computed says so
// and lets the gateway start anyway.
func warnImageDrift(ctx context.Context, out io.Writer, reader runtimeplan.LabelReader, m *manifest.Manifest) {
	info, err := runtimeplan.Check(ctx, reader, m)
	if err != nil {
		fmt.Fprintf(out, "  [WARN] could not verify runtime image currency: %v\n", err)
		return
	}
	if !info.Verifiable {
		return
	}
	if info.Kind == runtimeplan.DriftNone {
		return
	}
	if info.Kind == runtimeplan.DriftTemplate {
		// Content matches; only Piccolo's recipe moved. One quiet line, because a
		// rebuild here buys a new tag and nothing else.
		fmt.Fprintf(out, "  note: image content matches this workspace; the tag differs because Piccolo's build recipe changed (%s → %s)\n", info.Current, info.Desired)
		return
	}
	printImageDrift(out, info)
}

func printImageDrift(out io.Writer, info runtimeplan.Drift) {
	fmt.Fprintf(out, "\n[WARN] the runtime image in use does not match this workspace\n")
	fmt.Fprintf(out, "  running: %s\n", info.Current)
	fmt.Fprintf(out, "  wanted:  %s\n", info.Desired)
	for _, reason := range info.Reasons {
		fmt.Fprintf(out, "    - %s\n", reason)
	}
	fmt.Fprintf(out, "  Rebuild when you choose: piccolo image build --set && piccolo restart\n")
	fmt.Fprintf(out, "  Piccolo never rebuilds the image on its own.\n")
}
