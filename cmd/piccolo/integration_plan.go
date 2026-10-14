package main

import (
	"context"
	"fmt"
	"io"
	"strings"

	picintegration "github.com/kimus/piccolo/internal/integration"
	"github.com/kimus/piccolo/internal/manifest"
)

// manifestIntegration finds the installed entry for a package reference.
func manifestIntegration(m *manifest.Manifest, pkg string) (manifest.Integration, bool) {
	for _, in := range m.Runtime.Integrations {
		if in.Package == pkg {
			return in, true
		}
	}
	return manifest.Integration{}, false
}

// installedOf returns the matching entry or the zero value, for rendering.
func installedOf(m *manifest.Manifest, pkg string) manifest.Integration {
	in, _ := manifestIntegration(m, pkg)
	return in
}

// shortDigest trims a "sha256:<hex>" digest for display.
func shortDigest(digest string) string {
	d := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(digest)), "sha256:")
	switch {
	case d == "":
		return "unknown"
	case len(d) > 12:
		return d[:12]
	default:
		return d
	}
}

// downloadResolvedRelease resolves then downloads. Callers that are already
// committed to rebuilding (upgrade, and paths that must rehydrate every bundle)
// use this; `add` keeps resolve and download separate so it can ask first.
func downloadResolvedRelease(ctx context.Context, fetcher releaseFetcher, pkg picintegration.PackageRef) (fetchedRelease, error) {
	res, err := fetcher.Resolve(ctx, pkg)
	if err != nil {
		return fetchedRelease{}, err
	}
	return fetcher.Download(ctx, res)
}

// printIntegrationPlan shows what a run WOULD install using only cheap
// metadata — the resolved version, digest, and asset from the release manifest,
// plus the base image, toolchain, and the state directory being preserved.
//
// It deliberately runs BEFORE any artifact download or image build, so the
// confirmation is meaningful. The final image tag is a digest of the exact
// build inputs (including every bundled integration), which is only computable
// after downloads, so it is not claimed here.
func printIntegrationPlan(out io.Writer, m *manifest.Manifest, pkg picintegration.PackageRef, res releaseResolution, installed manifest.Integration) {
	fmt.Fprintf(out, "\nThis will install:\n")
	if installed.Package == "" {
		fmt.Fprintf(out, "  integration: %s %s  (new)\n", pkg.Short(), res.Version)
	} else {
		from := valueOrDash(installed.ResolvedVersion)
		if installed.ArtifactDigest != "" && installed.ArtifactDigest != res.Digest {
			fmt.Fprintf(out, "  integration: %s %s -> %s  (digest %s -> %s)\n",
				pkg.Short(), from, res.Version, shortDigest(installed.ArtifactDigest), shortDigest(res.Digest))
		} else {
			fmt.Fprintf(out, "  integration: %s %s -> %s\n", pkg.Short(), from, res.Version)
		}
	}
	fmt.Fprintf(out, "  artifact:    %s  sha256:%s\n", res.Asset, shortDigest(res.Digest))
	fmt.Fprintf(out, "  base image:  %s\n", integrationBuildBase(m))
	fmt.Fprintf(out, "  toolchain:   %s\n", strings.Join(effectiveToolchain(m).Packages, ", "))
	if installed.StatePath != "" {
		fmt.Fprintf(out, "  state:       %s (preserved)\n", installed.StatePath)
	} else {
		fmt.Fprintf(out, "  state:       integrations/%s/%s (new)\n", pkg.Owner, pkg.Repo)
	}
	var others []string
	for _, in := range m.Runtime.Integrations {
		if in.Package == "" || in.Package == pkg.Package() {
			continue
		}
		others = append(others, fmt.Sprintf("%s %s", displayPackage(in), valueOrDash(in.ResolvedVersion)))
	}
	if len(others) > 0 {
		fmt.Fprintf(out, "  unchanged:   %s\n", strings.Join(others, ", "))
	}
}
