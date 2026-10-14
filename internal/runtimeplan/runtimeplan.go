// Package runtimeplan answers "which runtime image does this workspace want?"
// from piccolo.yaml metadata alone.
//
// This must be cheap and download-free: every serve, restart, status and doctor
// run asks the question, and a drift check that fetched each installed
// integration artifact (~31 MB for piccolo-gws) just to print a warning would be
// worse than no check. The plan it returns is therefore comparable but not
// buildable — its bundles carry identity and no expanded directory.
package runtimeplan

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/kimus/piccolo/internal/generate"
	"github.com/kimus/piccolo/internal/manifest"
	"github.com/kimus/piccolo/internal/runtimeimage"
)

// SourceRelease is the manifest value marking an integration installed from a
// published release artifact. SourceBuild marks the legacy source-built path.
const (
	SourceRelease = "release"
	SourceBuild   = "build"
)

// Toolchain returns the effective toolchain: the workspace list when set,
// otherwise the built-in defaults. One definition, so a drift check and a build
// can never disagree about what the toolchain was.
func Toolchain(m *manifest.Manifest) manifest.Toolchain {
	tc := m.Runtime.Toolchain
	if len(tc.Packages) == 0 {
		tc.Packages = append([]string(nil), runtimeimage.DefaultPackages...)
	} else {
		tc.Packages = append([]string(nil), tc.Packages...)
	}
	if len(tc.Commands) == 0 {
		tc.Commands = append([]string(nil), runtimeimage.DefaultCommands...)
	} else {
		tc.Commands = append([]string(nil), tc.Commands...)
	}
	return tc
}

// BaseImage returns the official base for a rebuild. A Piccolo runtime image is
// never its own base: stacking a rebuild on a previous build would inherit that
// image's (possibly stale) layers. The launcher flavor is preserved when the web
// console is published.
func BaseImage(m *manifest.Manifest) string {
	base := m.Container.Image
	if !runtimeimage.IsLocalRuntimeImage(base) {
		return base
	}
	base = runtimeimage.DefaultBaseImage
	for _, p := range m.Container.Ports {
		if strings.Contains(p, ":18800") {
			return generate.LauncherImage
		}
	}
	return base
}

// Bundles represents the installed release integrations by identity only. Root
// stays empty, which is the compile-time guarantee that nothing was downloaded to
// answer this question.
func Bundles(in []manifest.Integration) []runtimeimage.IntegrationBundle {
	out := make([]runtimeimage.IntegrationBundle, 0, len(in))
	for _, item := range in {
		if strings.TrimSpace(item.Package) == "" || item.Source != SourceRelease {
			continue
		}
		out = append(out, runtimeimage.IntegrationBundle{
			Package:  item.Package,
			Version:  item.ResolvedVersion,
			Digest:   item.ArtifactDigest,
			Commands: append([]string(nil), item.Commands...),
		})
	}
	return out
}

func integrationPackageKey(in manifest.Integration) string {
	if pkg := strings.TrimSpace(in.Package); pkg != "" {
		if i := strings.Index(pkg, "@"); i >= 0 {
			pkg = pkg[:i]
		}
		return pkg
	}
	if strings.HasPrefix(in.Ref, "github:") {
		ref := strings.TrimPrefix(in.Ref, "github:")
		if i := strings.IndexAny(ref, "?#@"); i >= 0 {
			ref = ref[:i]
		}
		if strings.Count(ref, "/") == 1 {
			return "github:" + ref
		}
	}
	return ""
}

// RejectSourceBuilds fails a REBUILD that would silently drop a legacy
// source-built integration. Those entries named a Devbox/Nix flake ref, and the
// image no longer installs a package system that can resolve one.
//
// The exclusion names the integration this command is replacing, because
// `piccolo integration add <pkg>` is precisely the documented remedy: refusing
// it would block the way out. An empty exclude refuses every entry.
//
// This guards destructive composition only. Drift checks keep computing a plan
// for such a workspace — the image already built still runs, and a refusal there
// would turn a warning into a broken serve.
func RejectSourceBuilds(in []manifest.Integration, excludePackage string) error {
	excludePackage = strings.TrimSpace(excludePackage)
	for _, item := range in {
		if item.Source != SourceBuild && strings.TrimSpace(item.Ref) == "" {
			continue
		}
		name := integrationPackageKey(item)
		if name == "" {
			name = strings.TrimSpace(item.Package)
		}
		if excludePackage != "" && name == excludePackage {
			continue
		}
		return fmt.Errorf("integration %q is recorded as built from source (%q), which is no longer supported: the runtime image installs its toolchain with the base distribution's package manager and has no Nix. Reinstall it from a published release with 'piccolo integration add %s' (or drop the entry from runtime.integrations in piccolo.yaml) before rebuilding", name, item.Ref, name)
	}
	return nil
}

// Plan composes the runtime image this workspace describes. Source-built entries
// contribute nothing to it — they cannot be installed any more — which is why
// every command that BUILDS calls RejectSourceBuilds first.
func Plan(m *manifest.Manifest) (runtimeimage.Plan, error) {
	tc := Toolchain(m)
	return runtimeimage.BuildPlan(runtimeimage.Options{
		BaseImage: BaseImage(m),
		Packages:  tc.Packages,
		Commands:  tc.Commands,
		Bundles:   Bundles(m.Runtime.Integrations),
	})
}

// Labels reads the content labels of the image in use.
type LabelReader interface {
	ImageLabels(ctx context.Context, image string) (map[string]string, bool, error)
}

// DriftKind separates "the image content is wrong" from "only the build recipe
// moved". Rebuilding for content changes what the gateway can do; rebuilding for
// a recipe change alters only the tag, and calling that stale would train users
// to ignore a warning they cannot act on.
type DriftKind int

const (
	// DriftNone means the image in use is the one the workspace describes.
	DriftNone DriftKind = iota
	// DriftContent means an input that changes image CONTENT moved: base image,
	// toolchain, or an integration version/digest.
	DriftContent
	// DriftTemplate means the recorded content identity agrees but the tag
	// differs, so only Piccolo's build recipe changed (a Piccolo upgrade). Nothing
	// is missing; adopting the new tag is optional housekeeping.
	DriftTemplate
)

// Drift is the difference between the image in use and the image the workspace
// describes, with the reasons named.
type Drift struct {
	Current string
	Desired string
	Reasons []string
	Kind    DriftKind
	// Verifiable is false when the comparison could not be computed (no daemon,
	// image not pulled, metadata unreachable). A warning path must never block
	// starting the gateway.
	Verifiable bool
}

// Stale reports content drift: the running image does not contain what the
// workspace says it should.
func (d Drift) Stale() bool { return d.Verifiable && d.Kind == DriftContent }

// Check compares the running image against the desired one. The tag decides
// identity; the labels name what moved, which is what makes the warning
// actionable instead of a shrug.
func Check(ctx context.Context, reader LabelReader, m *manifest.Manifest) (Drift, error) {
	plan, err := Plan(m)
	if err != nil {
		return Drift{}, err
	}
	info := Drift{Current: m.Container.Image, Desired: plan.Tag}
	if !runtimeimage.IsLocalRuntimeImage(info.Current) {
		// Not a Piccolo-built image: there is nothing here to keep current.
		return info, nil
	}
	if plan.Tag == info.Current {
		info.Verifiable = true
		return info, nil
	}
	labels, found, err := reader.ImageLabels(ctx, info.Current)
	if err != nil {
		return info, err
	}
	if !found {
		// The image is not here to describe. The presence check owns that
		// message, so drift stays quiet instead of reporting it twice.
		return info, nil
	}
	info.Verifiable = true
	if labels[runtimeimage.LabelBaseImage] == "" {
		// Built before Piccolo recorded content labels, so no reason can be
		// honestly attributed. The tag differs, which is a real difference — say
		// both parts rather than inventing a cause.
		info.Kind = DriftContent
		info.Reasons = []string{"the running image predates Piccolo content labels"}
		return info, nil
	}
	info.Reasons = Reasons(plan, labels)
	if len(info.Reasons) == 0 {
		info.Kind = DriftTemplate
		return info, nil
	}
	info.Kind = DriftContent
	return info, nil
}

// Reasons names every image input that moved. It reads like a changelog, because
// the user has to decide whether to spend a rebuild.
func Reasons(desired runtimeimage.Plan, current map[string]string) []string {
	want := desired.Labels()
	var reasons []string
	if got := current[runtimeimage.LabelBaseImage]; got != want[runtimeimage.LabelBaseImage] {
		reasons = append(reasons, fmt.Sprintf("base image: %s -> %s", orUnknown(got), want[runtimeimage.LabelBaseImage]))
	}
	if got := current[runtimeimage.LabelToolchain]; got != want[runtimeimage.LabelToolchain] {
		reasons = append(reasons, "toolchain package list changed")
	}

	wantIntegrations := integrationLabels(want)
	haveIntegrations := integrationLabels(current)
	pkgs := make([]string, 0, len(wantIntegrations)+len(haveIntegrations))
	seen := map[string]bool{}
	for _, pkg := range append(keysOf(wantIntegrations), keysOf(haveIntegrations)...) {
		if !seen[pkg] {
			seen[pkg] = true
			pkgs = append(pkgs, pkg)
		}
	}
	sort.Strings(pkgs)
	for _, pkg := range pkgs {
		w, wanted := wantIntegrations[pkg]
		h, present := haveIntegrations[pkg]
		switch {
		case wanted && !present:
			reasons = append(reasons, fmt.Sprintf("integration %s (%s) is not in the running image", pkg, w))
		case present && !wanted:
			reasons = append(reasons, fmt.Sprintf("running image still contains integration %s (%s)", pkg, h))
		case w != h:
			reasons = append(reasons, fmt.Sprintf("integration %s: image has %s, workspace wants %s", pkg, h, w))
		}
	}
	return reasons
}

func integrationLabels(labels map[string]string) map[string]string {
	out := map[string]string{}
	for key, value := range labels {
		if strings.HasPrefix(key, runtimeimage.LabelIntegrationPrefix) {
			out[strings.TrimPrefix(key, runtimeimage.LabelIntegrationPrefix)] = value
		}
	}
	return out
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func orUnknown(v string) string {
	if strings.TrimSpace(v) == "" {
		return "unknown"
	}
	return v
}
