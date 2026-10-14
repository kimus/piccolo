package runtimeimage

import (
	"io"
	"strings"
	"testing"
)

// The generated Dockerfile is executed by the daemon, not by Go, so a missing
// line continuation is a build failure rather than a test failure. This checks
// the one rule that a string-building function can get wrong silently.
func TestDockerfileLineContinuationsAreValid(t *testing.T) {
	plan, err := BuildPlan(Options{
		Packages: []string{"file", "libreoffice"},
		Commands: []string{"file", "soffice"},
		Bundles:  []IntegrationBundle{{Package: "kimus/x", Version: "1", Digest: "sha256:aaaa", Commands: []string{"x"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(plan.Dockerfile, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "&&") && !strings.HasPrefix(trimmed, "if [") && !strings.HasPrefix(trimmed, "for ") {
			continue
		}
		if i == 0 {
			t.Fatalf("line 0 starts a continuation: %q", line)
		}
		prev := strings.TrimRight(lines[i-1], " \t")
		if !strings.HasSuffix(prev, "\\") {
			t.Errorf("line %d continues a command but the previous line has no trailing backslash:\nprev=%q\nline=%q", i+1, lines[i-1], line)
		}
	}
}

func TestBuildPlanDeterministic(t *testing.T) {
	p1, err := BuildPlan(Options{BaseImage: "docker.io/sipeed/picoclaw:v0.3.1", Packages: []string{"file", "zbar", "file"}, Commands: []string{"file", "zbarimg", "file"}})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	p2, err := BuildPlan(Options{BaseImage: "docker.io/sipeed/picoclaw:v0.3.1", Packages: []string{"zbar", "file"}, Commands: []string{"zbarimg", "file"}})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if p1.Tag != p2.Tag || p1.Dockerfile != p2.Dockerfile {
		t.Fatal("plan is not deterministic")
	}
	for _, want := range []string{
		"FROM docker.io/sipeed/picoclaw:v0.3.1",
		// Bootstrap carries only what the build and runtime need. `shadow` and
		// `xz` existed solely for the Nix installer and are gone.
		"RUN apk add --no-cache bash curl ca-certificates tar git coreutils",
	} {
		if !strings.Contains(p1.Dockerfile, want) {
			t.Errorf("Dockerfile missing %q", want)
		}
	}
	// The toolchain is installed by the base distribution at BUILD time and each
	// configured command is verified on PATH afterwards. A package that does not
	// provide one fails the build by name instead of shipping an image that
	// advertises a tool it cannot run.
	for _, want := range []string{
		"apk add --no-cache --update 'file' 'zbar'",
		`for tool in 'file' 'zbarimg'; do command -v "$tool" >/dev/null || { echo "configured command is not provided by the installed packages: $tool" >&2; exit 3; }; done`,
	} {
		if !strings.Contains(p1.Dockerfile, want) {
			t.Errorf("Dockerfile missing toolchain step %q\nDockerfile:\n%s", want, p1.Dockerfile)
		}
	}
	// Nothing from the removed packaging system may come back: not as an
	// installer, a config file, a PATH, a profile, or a garbage-collection step.
	// The image's /nix/store holds only what integration bundles copy into it.
	for _, banned := range []string{
		"devbox", "nixbld", "groupadd", "useradd", "shellenv", "nix-collect-garbage",
		"get.jetify.com", "/root/.nix-profile", "devbox.json", "piccolo-setup-tools",
	} {
		if strings.Contains(p1.Dockerfile, banned) {
			t.Errorf("Dockerfile must not mention %q", banned)
		}
	}
	if !strings.HasPrefix(p1.Tag, DefaultTagPrefix+":") || strings.HasSuffix(p1.Tag, ":latest") {
		t.Errorf("tag = %q", p1.Tag)
	}
}

// The default lists must describe something that actually installs. These are
// the apk names verified against Alpine 3.21, where all 23 commands resolve;
// `7za` is absent because neither Alpine p7zip nor 7zip provides it.
func TestDefaultToolchainIsAlpineNamed(t *testing.T) {
	for _, want := range []string{
		"tesseract-ocr", "tesseract-ocr-data-eng", "yq", "libxml2-utils",
		"poppler-utils", "libreoffice", "imagemagick",
	} {
		if !strings.Contains(strings.Join(DefaultPackages, " "), want) {
			t.Errorf("DefaultPackages missing %q", want)
		}
	}
	for _, gone := range []string{"yq-go", "libxml2\"", "tesseract\""} {
		for _, p := range DefaultPackages {
			if p == strings.TrimSuffix(strings.TrimPrefix(gone, "\""), "\"") && p != "yq" {
				t.Errorf("DefaultPackages still uses the nixpkgs name %q", p)
			}
		}
	}
	if strings.Contains(strings.Join(DefaultCommands, " "), "7za") {
		t.Error("7za is not provided by any Alpine package and must not be a default command")
	}
	// OCR ships English data only; the all-language package measured 1015 MB of
	// the image's 4.28 GiB of tool closures.
	for _, p := range DefaultPackages {
		if p == "tesseract-ocr-data-all" {
			t.Error("default toolchain must not install every OCR language")
		}
	}
}

// An entry may pin a version; Piccolo passes it through unchanged so the user
// can trade floating repos for reproducibility without any new syntax.
func TestPackageEntryPassesThroughVerbatim(t *testing.T) {
	plan, err := BuildPlan(Options{Packages: []string{"jq=1.7.1-r0"}, Commands: []string{"jq"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan.Dockerfile, "'jq=1.7.1-r0'") {
		t.Errorf("pinned package was rewritten or dropped:\n%s", plan.Dockerfile)
	}
}

// The same toolchain layer must not be re-executed when only integrations move.
func TestBakedIntegrationInstallUsesBundleClosure(t *testing.T) {
	p, err := BuildPlan(Options{
		BaseImage: "docker.io/sipeed/picoclaw:v0.3.1",
		Bundles: []IntegrationBundle{{
			Package: "kimus/piccolo-gws", Version: "0.3.4", Digest: "sha256:8a0937a5d583",
			Commands: []string{"piccolo-gws", "gws"},
		}},
	})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	for _, want := range []string{
		"install -m 0755", "command -v 'piccolo-gws'", "command -v 'gws'",
		// Bundles carry their own runtime closure; it lands in /nix/store as
		// ordinary image content, with no Nix installed to manage it.
		"$src/nix/store", "/nix/store/",
	} {
		if !strings.Contains(p.Dockerfile, want) {
			t.Errorf("bundle layer missing %q", want)
		}
	}
	p2, err := BuildPlan(Options{BaseImage: "docker.io/sipeed/picoclaw:v0.3.1"})
	if err != nil {
		t.Fatalf("BuildPlan no integration: %v", err)
	}
	if p.Tag == p2.Tag {
		t.Fatal("integration identity must affect the deterministic tag")
	}
}

func TestToolchainLayerPrecedesIntegrationLayers(t *testing.T) {
	p, err := BuildPlan(Options{
		BaseImage: "b",
		Packages:  []string{"file"},
		Commands:  []string{"file"},
		Bundles:   []IntegrationBundle{{Package: "kimus/piccolo-gws", Version: "1", Digest: "sha256:aaaa", Commands: []string{"gws"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	toolchain := strings.Index(p.Dockerfile, "apk add --no-cache --update")
	release := strings.Index(p.Dockerfile, "release-integrations/")
	if !(toolchain >= 0 && toolchain < release) {
		t.Errorf("toolchain layer must precede the integration layers (toolchain=%d release=%d)", toolchain, release)
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
	if !strings.Contains(string(b), "Dockerfile") {
		t.Error("tar missing Dockerfile")
	}
	// The devbox config files were the old mechanism's only context inputs. A
	// plan that still ships one has grown a second, divergent install path.
	for _, gone := range []string{"devbox.json", "piccolo-integrations-devbox.json", "piccolo-setup-tools"} {
		if strings.Contains(string(b), gone) {
			t.Errorf("tar must not contain %q", gone)
		}
	}
}

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

// The install loop must skip packaging residue too: commands may be recorded
// correctly while the files still get copied into the bundle, and installing
// ._x into /usr/local/bin would ship a resource fork as a "tool".
func TestBundleInstallBlockSkipsPackagingResidue(t *testing.T) {
	plan, err := BuildPlan(Options{BaseImage: "b", Bundles: []IntegrationBundle{{Package: "kimus/x", Version: "1", Digest: "sha256:aaaa", Commands: []string{"x"}}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"._*) continue", ".DS_Store) continue"} {
		if !strings.Contains(plan.Dockerfile, want) {
			t.Errorf("bundle install block missing residue guard %q:\n%s", want, plan.Dockerfile)
		}
	}
}
