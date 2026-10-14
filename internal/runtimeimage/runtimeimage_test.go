package runtimeimage

import (
	"io"
	"strings"
	"testing"
)

func TestBuildPlanDeterministic(t *testing.T) {
	p1, err := BuildPlan(Options{BaseImage: "docker.io/sipeed/picoclaw:v0.3.1", Packages: []string{"file", "zbar", "file"}, Commands: []string{"file", "zbarimg", "file"}})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	p2, err := BuildPlan(Options{BaseImage: "docker.io/sipeed/picoclaw:v0.3.1", Packages: []string{"zbar", "file"}, Commands: []string{"zbarimg", "file"}})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if p1.Tag != p2.Tag || p1.Dockerfile != p2.Dockerfile || p1.DevboxJSON != p2.DevboxJSON {
		t.Fatal("plan is not deterministic")
	}
	for _, want := range []string{
		"FROM docker.io/sipeed/picoclaw:v0.3.1",
		"apk add --no-cache bash curl ca-certificates xz tar git shadow coreutils",
		"COPY devbox.json /root/.local/share/devbox/global/default/devbox.json",
		"groupadd -g 30000 nixbld",
		"RUN devbox version",
	} {
		if !strings.Contains(p1.Dockerfile, want) {
			t.Errorf("Dockerfile missing %q", want)
		}
	}
	// The toolchain is image content now: installed at BUILD time, rooted so a
	// later layer's garbage collection cannot take it, and symlinked onto PATH.
	// Nothing about it depends on a start-time step or a host mount.
	for _, want := range []string{
		"RUN set -eu; cd " + DevboxGlobalDir,
		"&& devbox install \\",
		"eval \"$(devbox shellenv)\"",
		"install -m 0755 \"$bin\" /usr/local/bin/\"$tool\"",
		"ln -sfn \"$bin\" " + ToolchainGCRoot,
	} {
		if !strings.Contains(p1.Dockerfile, want) {
			t.Errorf("Dockerfile missing toolchain bake step %q", want)
		}
	}
	// The start-time setup script is gone: an image that bakes its own toolchain
	// must not also offer a second, divergent install path.
	if strings.Contains(p1.Dockerfile, "piccolo-setup-tools") {
		t.Error("Dockerfile must not ship the start-time tool setup script")
	}
	// Layer order is the caching contract: the toolchain install follows the
	// devbox config copy (so the package list is part of its cache key) and
	// precedes the integration layers (so an integration change reuses it).
	idxCopy := strings.Index(p1.Dockerfile, "COPY devbox.json")
	idxToolchain := strings.Index(p1.Dockerfile, "cd "+DevboxGlobalDir)
	if !(idxCopy >= 0 && idxToolchain > idxCopy) {
		t.Errorf("toolchain install must follow the devbox config copy (copy=%d toolchain=%d)", idxCopy, idxToolchain)
	}
	for _, want := range []string{`"file"`, `"zbar"`} {
		if !strings.Contains(p1.DevboxJSON, want) {
			t.Errorf("devbox.json missing %q", want)
		}
	}
	if !strings.HasPrefix(p1.Tag, DefaultTagPrefix+":") || strings.HasSuffix(p1.Tag, ":latest") {
		t.Errorf("tag = %q", p1.Tag)
	}
}

func TestBuildPlanWithIntegrationsInstallsAtBuildTime(t *testing.T) {
	p, err := BuildPlan(Options{BaseImage: "docker.io/sipeed/picoclaw:v0.3.1", Integrations: []Integration{{Ref: "github:kimus/piccolo-gws", Commands: []string{"piccolo-gws"}}}})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	for _, want := range []string{"piccolo-integrations-devbox.json", "github:kimus/piccolo-gws", "Installing integration packages with Devbox/Nix", "devbox install", "Finalizing integration commands", "install -m 0755", "ln -sfn", "nix-collect-garbage -d", "command -v 'piccolo-gws'"} {
		if !strings.Contains(p.Dockerfile+p.IntegrationsJSON, want) {
			t.Errorf("integration plan missing %q", want)
		}
	}
	p2, err := BuildPlan(Options{BaseImage: "docker.io/sipeed/picoclaw:v0.3.1"})
	if err != nil {
		t.Fatalf("BuildPlan no integration: %v", err)
	}
	if p.Tag == p2.Tag {
		t.Fatal("integration refs must affect deterministic tag")
	}
}

// Layer order is the caching contract, across both kinds of integration input:
// the toolchain layer must be reusable when only integrations change.
func TestToolchainLayerPrecedesIntegrationLayers(t *testing.T) {
	p, err := BuildPlan(Options{
		BaseImage:    "docker.io/sipeed/picoclaw:v0.3.1",
		Packages:     []string{"file"},
		Commands:     []string{"file"},
		Integrations: []Integration{{Ref: "github:kimus/piccolo-gws", Commands: []string{"piccolo-gws"}}},
		Bundles:      []IntegrationBundle{{Package: "kimus/piccolo-gws", Version: "1", Digest: "sha256:aaaa"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	toolchain := strings.Index(p.Dockerfile, "cd "+DevboxGlobalDir)
	source := strings.Index(p.Dockerfile, "piccolo-integrations-devbox.json")
	release := strings.Index(p.Dockerfile, "release-integrations/")
	if !(toolchain >= 0 && toolchain < source && toolchain < release) {
		t.Errorf("toolchain layer must precede the integration layers (toolchain=%d source=%d release=%d)", toolchain, source, release)
	}
}

func TestContextTarContainsBuildFiles(t *testing.T) {
	p, err := BuildPlan(Options{Packages: []string{"file"}})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	r, err := p.ContextTar()
	if err != nil {
		t.Fatalf("ContextTar: %v", err)
	}
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read tar: %v", err)
	}
	for _, want := range []string{"Dockerfile", "devbox.json", "piccolo-integrations-devbox.json"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("tar missing expected file %q", want)
		}
	}
	if strings.Contains(string(b), "piccolo-setup-tools") {
		t.Error("tar must not contain the removed start-time setup script")
	}
}

// A bundle needs its expanded bytes only when the context is tarred. Identity —
// package, version, digest — is enough for the tag and the labels, which is what
// lets a drift check run on every serve without transferring ~31 MB per
// installed integration.
func TestPlanIdentityNeedsNoArtifactBytes(t *testing.T) {
	opts := Options{
		BaseImage: "docker.io/sipeed/picoclaw:v0.3.1",
		Bundles: []IntegrationBundle{{
			Package: "kimus/piccolo-gws", Version: "0.3.1", Digest: "sha256:131f8cf4172fabcd",
			Commands: []string{"piccolo-gws", "gws"},
		}},
	}
	plan, err := BuildPlan(opts)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if len(plan.Bundles[0].Root) != 0 {
		t.Fatal("identity-only plan must not require an expanded bundle directory")
	}
	if _, err := plan.ContextTar(); err == nil {
		t.Error("tar must refuse to build a context from a bundle with no expanded directory")
	}
	want := "kimus/piccolo-gws@0.3.1+131f8cf4172f"
	if len(plan.Identity) != 1 || plan.Identity[0] != want {
		t.Errorf("identity = %#v, want [%s]", plan.Identity, want)
	}
	if got := plan.Labels()[LabelIntegrationPrefix+"kimus/piccolo-gws"]; got != "0.3.1+131f8cf4172f" {
		t.Errorf("integration label = %q", got)
	}
}

// Reproduced defect: two versions of one integration produced the same tag, so
// `add` of an older release reused the current image while the manifest recorded
// a version the image did not contain.
func TestTagChangesWithIntegrationVersion(t *testing.T) {
	newPlan := func(version, digest string) string {
		plan, err := BuildPlan(Options{
			BaseImage: "docker.io/sipeed/picoclaw:v0.3.1",
			Packages:  []string{"file"},
			Bundles: []IntegrationBundle{{
				Package: "kimus/piccolo-gws", Version: version, Digest: digest,
				Commands: []string{"piccolo-gws"},
			}},
		})
		if err != nil {
			t.Fatalf("BuildPlan: %v", err)
		}
		return plan.Tag
	}
	got := newPlan("0.2.0", "sha256:aaaaaaaabbbbbbbb")
	want := newPlan("0.3.1", "sha256:ccccccccdddddddd")
	if got == want {
		t.Fatalf("different integration versions collide on tag %s", got)
	}
	// Same content, different install order → same tag.
	p1, err := BuildPlan(Options{BaseImage: "b", Bundles: []IntegrationBundle{
		{Package: "a/x", Version: "1", Digest: "sha256:1111111111111111"},
		{Package: "b/y", Version: "2", Digest: "sha256:2222222222222222"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	p2, err := BuildPlan(Options{BaseImage: "b", Bundles: []IntegrationBundle{
		{Package: "b/y", Version: "2", Digest: "sha256:2222222222222222"},
		{Package: "a/x", Version: "1", Digest: "sha256:1111111111111111"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if p1.Tag != p2.Tag {
		t.Errorf("install order changed the tag: %s vs %s", p1.Tag, p2.Tag)
	}
}

// Positional bundle directories shifted whenever an alphabetically earlier
// integration was added, invalidating layers of unrelated integrations.
func TestBundleContextPathsAreNamedByPackage(t *testing.T) {
	plan, err := BuildPlan(Options{
		BaseImage: "b",
		Bundles: []IntegrationBundle{
			{Package: "kimus/piccolo-gws", Version: "1", Digest: "sha256:aaaa"},
			{Package: "acme/other", Version: "1", Digest: "sha256:bbbb"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"release-integrations/kimus/piccolo-gws/", "release-integrations/acme/other/"} {
		if !strings.Contains(plan.Dockerfile, want) {
			t.Errorf("Dockerfile missing bundle path %q", want)
		}
	}
	if strings.Contains(plan.Dockerfile, "bundle-0") || strings.Contains(plan.Dockerfile, "bundle-1") {
		t.Error("bundle directories must not be positional")
	}
}

func TestBundleSegmentCannotEscapeContext(t *testing.T) {
	for _, pkg := range []string{"../../etc", "..", "a/../../b"} {
		got := sanitizeBundleSegment(pkg)
		if got == ".." || strings.Contains(got, "../") || strings.HasPrefix(got, "..") {
			t.Errorf("sanitizeBundleSegment(%q) = %q escapes the bundle root", pkg, got)
		}
	}
}
