// Package runtimeimage renders the deterministic local Piccolo runtime image
// build inputs. The build context is generated on demand and is not written
// into the user's workspace.
//
// The image is self-contained: the toolchain (installed with the base
// distribution's own package manager) and the release integrations are both
// baked in as layers, so nothing installs at container start and no host cache
// is mounted over the image's package directories. Anything that shadows image
// content makes its binaries unexecutable, which is why the toolchain lives in
// the build.
package runtimeimage

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	DefaultBaseImage = "docker.io/sipeed/picoclaw:v0.3.1"
	DefaultTagPrefix = "piccolo/picoclaw-runtime"

	// MediaTempDir is mirrored from workspace runtime/media so deterministic
	// tools can read channel attachments.
	MediaTempDir = "/tmp/picoclaw_media"

	// Image label keys. Labels are written at build time, so a container
	// inherits them and they cannot drift from the image's actual content.
	// They explain WHY a rebuild is wanted; the tag remains the cache identity.
	LabelBaseImage = "piccolo.base"
	LabelToolchain = "piccolo.toolchain"
	// LabelIntegrationPrefix + sanitised package = "<version>+<short digest>".
	LabelIntegrationPrefix = "piccolo.integration."
)

// DefaultCommands are the command names the build verifies are on PATH after
// the toolchain packages are installed. Each must be provided by one of
// DefaultPackages; a configured command no package provides fails the build.
var DefaultCommands = []string{
	"zbarimg", "file", "python3", "jq", "yq",
	"pdftotext", "pdfinfo", "pdftoppm", "pdftocairo",
	"tesseract", "pandoc", "xmllint", "exiftool",
	"ffprobe", "ffmpeg", "sqlite3", "unzip", "7z",
	"libreoffice", "soffice", "magick", "convert", "identify",
}

// DefaultTools is kept as a compatibility alias for callers/tests that still
// refer to the old name.
var DefaultTools = DefaultCommands

// DefaultPackages is the standard file-handling toolset, named for the base
// distribution's package manager (Alpine apk). Verified against Alpine 3.21:
// every entry exists and every DefaultCommands entry resolves after install.
//
// OCR installs the engine plus English language data only. The alternative —
// one package carrying every trained model — measured 1015 MB against 39 MB
// for engine + English, which is why broad language coverage is an explicit
// opt-in through a custom package list rather than a default.
var DefaultPackages = []string{
	"zbar",
	"file",
	"python3",
	"jq",
	"yq",
	"poppler-utils",
	"tesseract-ocr",
	"tesseract-ocr-data-eng",
	"pandoc",
	"libxml2-utils",
	"exiftool",
	"ffmpeg",
	"sqlite",
	"unzip",
	"p7zip",
	"libreoffice",
	"imagemagick",
}

type Options struct {
	BaseImage string
	Tag       string
	Packages  []string
	Commands  []string
	Bundles   []IntegrationBundle
}

// IntegrationBundle is a verified, already-expanded release artifact directory.
// Its contents are copied into the image; bin/* is installed onto PATH.
// Package/Version/Digest contribute to the image identity even when Root is
// empty, which is how a desired tag is computed without downloading anything.
type IntegrationBundle struct {
	Package  string
	Version  string
	Digest   string
	Root     string
	Commands []string
}

type Plan struct {
	BaseImage  string
	Tag        string
	Dockerfile string
	Packages   []string
	Commands   []string
	Bundles    []IntegrationBundle
	// Identity is the sorted owner/repo@version+digest list of installed
	// release integrations. It is part of the tag and of the image labels.
	Identity []string
	// ToolchainIdentity is the short digest of the baked toolchain layer
	// (package list + command list).
	ToolchainIdentity string
}

func BuildPlan(opts Options) (Plan, error) {
	base := strings.TrimSpace(opts.BaseImage)
	if base == "" {
		base = DefaultBaseImage
	}
	packages := append([]string(nil), opts.Packages...)
	if len(packages) == 0 {
		packages = append([]string(nil), DefaultPackages...)
	}
	packages = uniqueSorted(packages)
	commands := append([]string(nil), opts.Commands...)
	if len(commands) == 0 {
		commands = append([]string(nil), DefaultCommands...)
	}
	commands = uniqueSorted(commands)
	bundles := normalizeBundles(opts.Bundles)
	identity := bundleIdentity(bundles)
	toolchainID := toolchainIdentity(packages, commands)
	dockerfile := renderDockerfile(base, packages, commands, bundles)
	tag := strings.TrimSpace(opts.Tag)
	if tag == "" {
		// The tag must cover integration identity: without version+digest here,
		// two versions of one integration collide on the same tag while the
		// manifest records a version the image does not contain.
		//
		// The identity is carried twice on purpose — once as this explicit input
		// and once inside the Dockerfile text through the content labels — so an
		// edit that drops either one still cannot produce a stale-cache reuse.
		// Removing BOTH is what reintroduces the collision.
		tag = DefaultTag(base, dockerfile, strings.Join(identity, "\n"))
	}
	return Plan{BaseImage: base, Tag: tag, Dockerfile: dockerfile, Packages: packages, Commands: commands, Bundles: bundles, Identity: identity, ToolchainIdentity: toolchainID}, nil
}

func IsLocalRuntimeImage(ref string) bool {
	return strings.HasPrefix(ref, DefaultTagPrefix+":")
}

// DefaultTag derives the content-addressed local image tag from the build
// inputs that determine image content.
func DefaultTag(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	return fmt.Sprintf("%s:%s", DefaultTagPrefix, hex.EncodeToString(sum[:])[:12])
}

// Labels returns the content labels this plan writes into the image. Comparing
// them against a running image's labels is how a drift warning names the reason
// (base, toolchain, or which integration version) instead of only "different".
func (p Plan) Labels() map[string]string {
	return contentLabels(p.BaseImage, p.Packages, p.Commands, p.Bundles)
}

func (p Plan) ContextTar() (io.Reader, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	files := []struct {
		name string
		body string
	}{
		{name: "Dockerfile", body: p.Dockerfile},
	}
	for _, f := range files {
		b := []byte(f.body)
		if err := tw.WriteHeader(&tar.Header{Name: f.name, Mode: 0o644, Size: int64(len(b))}); err != nil {
			return nil, err
		}
		if _, err := tw.Write(b); err != nil {
			return nil, err
		}
	}
	for _, bundle := range p.Bundles {
		// ContextTar needs expanded bytes; the tag/labels do not. A plan built
		// from manifest metadata alone (drift checks) must never be tarred.
		if strings.TrimSpace(bundle.Root) == "" {
			return nil, fmt.Errorf("release bundle %s has no expanded artifact directory", bundle.Package)
		}
		root := filepath.Clean(bundle.Root)
		if _, err := os.Stat(root); err != nil {
			return nil, fmt.Errorf("release bundle %s: %w", bundle.Package, err)
		}
		if err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			name := filepath.ToSlash(filepath.Join(bundleContextDir(bundle), rel))
			if err := tw.WriteHeader(&tar.Header{Name: name, Mode: int64(info.Mode().Perm()), Size: int64(len(b))}); err != nil {
				return err
			}
			_, err = tw.Write(b)
			return err
		}); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return bytes.NewReader(buf.Bytes()), nil
}

// renderDockerfile bakes everything the gateway needs into the image: the
// toolchain (via the base distribution's package manager), the integration
// layers, and content labels. Nothing installs at container start any more.
//
// There is deliberately no secondary package system here. The image used to
// install Devbox and Nix to get these same tools, and measured the cost: 4.28
// GiB of tool closures plus a ~0.58 GiB Nix floor, against 1.3 GB of rootfs for
// the identical command list installed with apk. Integration bundles still
// unpack into /nix/store, because they ship their own closure there, but Nix is
// no longer installed to run it. The default PicoClaw entrypoint is untouched.
func renderDockerfile(base string, packages, commands []string, bundles []IntegrationBundle) string {
	integrationBlock := ""
	if len(bundles) > 0 {
		integrationBlock = renderBundleInstallBlock(bundles)
	}
	return fmt.Sprintf(`FROM %s
USER root
RUN apk add --no-cache bash curl ca-certificates tar git coreutils
%[2]s%[3]sLABEL org.opencontainers.image.title="Piccolo PicoClaw Runtime"
LABEL org.opencontainers.image.description="PicoClaw runtime with the distribution toolchain and release integrations baked in"
%[4]s`, base, renderToolchainInstallBlock(packages, commands), integrationBlock, renderLabelBlock(base, packages, commands, bundles))
}

// renderToolchainInstallBlock installs the configured packages at BUILD time and
// verifies every configured command resolves on PATH afterwards. The
// distribution installs into /usr/bin, which is already on PATH, so no copies,
// symlinks, or garbage-collection pinning are needed — and no later layer
// collects what this one installed.
//
// A configured command that no package provides fails the build: an image must
// not claim tools it cannot run. The layer sits before the integration layers so
// Docker's cache reuses it across integration changes.
func renderToolchainInstallBlock(packages, commands []string) string {
	pkgs := uniqueSorted(packages)
	tools := uniqueSorted(commands)
	if len(pkgs) == 0 && len(tools) == 0 {
		return ""
	}
	install := ""
	if len(pkgs) > 0 {
		install = fmt.Sprintf("apk add --no-cache --update %s", strings.Join(shellQuoteList(pkgs), " "))
	}
	if len(tools) > 0 {
		verify := fmt.Sprintf(`for tool in %s; do command -v "$tool" >/dev/null || { echo "configured command is not provided by the installed packages: $tool" >&2; exit 3; }; done`, strings.Join(shellQuoteList(tools), " "))
		if install != "" {
			install += " \\\n && " + verify
		} else {
			install = verify
		}
	}
	return fmt.Sprintf("RUN set -eu \\\n && %s\n", install)
}

// renderLabelBlock records image content. Labels answer "what is in this
// image?" and explain WHY a rebuild is wanted; the tag stays the cache identity.
// They are written at build time, so a container inherits them and cannot drift
// from the image's bytes — including across a promotion rename.
func renderLabelBlock(base string, packages, commands []string, bundles []IntegrationBundle) string {
	labels := contentLabels(base, packages, commands, bundles)
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "LABEL %s=%s\n", k, labelValue(labels[k]))
	}
	return b.String()
}

// contentLabels is the single definition of what an image says about itself.
// renderLabelBlock writes exactly this map into the Dockerfile and Plan.Labels
// returns it, so a drift comparison can never be checking something other than
// what the build recorded.
func contentLabels(base string, packages, commands []string, bundles []IntegrationBundle) map[string]string {
	labels := map[string]string{
		LabelBaseImage: base,
		LabelToolchain: toolchainIdentity(packages, commands),
	}
	for _, bundle := range bundles {
		if strings.TrimSpace(bundle.Package) == "" {
			continue
		}
		labels[IntegrationLabelKey(bundle.Package)] = bundleLabelValue(bundle)
	}
	return labels
}

// IntegrationLabelKey is the image label key recording one integration's
// installed identity. Package refs carry a provider prefix (`github:kimus/x`),
// so the key is sanitised the same way build-context paths are: one rule, and a
// lookup that cannot disagree with what the build wrote.
func IntegrationLabelKey(pkg string) string {
	return LabelIntegrationPrefix + sanitizeBundleSegment(pkg)
}

func bundleLabelValue(bundle IntegrationBundle) string {
	return fmt.Sprintf("%s+%s", valueOrUnknown(bundle.Version), shortDigest(bundle.Digest))
}

// bundleIdentity is the sorted owner/repo@version+digest list of installed
// release integrations. Identity comes from release metadata, never from the
// artifact bytes, so the desired image tag is computable on every serve without
// downloading anything — a drift warning that cost ~31 MB per integration would
// be worse than no warning at all.
func bundleIdentity(bundles []IntegrationBundle) []string {
	out := make([]string, 0, len(bundles))
	for _, bundle := range bundles {
		if strings.TrimSpace(bundle.Package) == "" {
			continue
		}
		out = append(out, fmt.Sprintf("%s@%s+%s", bundle.Package, valueOrUnknown(bundle.Version), shortDigest(bundle.Digest)))
	}
	// Sorted: the tag must not depend on install order.
	sort.Strings(out)
	return out
}

func toolchainIdentity(packages, commands []string) string {
	sum := sha256.Sum256([]byte(strings.Join(uniqueSorted(packages), " ") + "|" + strings.Join(uniqueSorted(commands), " ")))
	return "sha256:" + hex.EncodeToString(sum[:])[:12]
}

func shortDigest(digest string) string {
	d := strings.TrimPrefix(strings.TrimSpace(digest), "sha256:")
	if d == "" {
		return "unknown"
	}
	if len(d) > 12 {
		return d[:12]
	}
	return d
}

func valueOrUnknown(v string) string {
	if strings.TrimSpace(v) == "" {
		return "unknown"
	}
	return strings.TrimSpace(v)
}

// bundleContextDir is the build-context directory for one release bundle. It is
// named by package rather than by positional index: an index shifts when an
// alphabetically earlier integration is added, invalidating unrelated layers.
func bundleContextDir(bundle IntegrationBundle) string {
	return "release-integrations/" + sanitizeBundleSegment(bundle.Package)
}

func sanitizeBundleSegment(pkg string) string {
	parts := strings.Split(strings.TrimSpace(pkg), "/")
	for i, part := range parts {
		var b strings.Builder
		for _, r := range part {
			switch {
			case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
				b.WriteRune(r)
			default:
				b.WriteRune('_')
			}
		}
		cleaned := b.String()
		if cleaned == "" || cleaned == "." || cleaned == ".." {
			cleaned = "x"
		}
		parts[i] = cleaned
	}
	return strings.Join(parts, "/")
}

func labelValue(s string) string {
	return "\"" + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + "\""
}

func normalizeBundles(in []IntegrationBundle) []IntegrationBundle {
	out := append([]IntegrationBundle(nil), in...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Package == out[j].Package {
			return out[i].Version < out[j].Version
		}
		return out[i].Package < out[j].Package
	})
	for i := range out {
		out[i].Commands = uniqueSorted(out[i].Commands)
	}
	return out
}

func renderBundleInstallBlock(bundles []IntegrationBundle) string {
	var b strings.Builder
	for _, bundle := range bundles {
		commands := uniqueSorted(bundle.Commands)
		dir := bundleContextDir(bundle)
		b.WriteString(fmt.Sprintf("COPY %s/ /opt/piccolo/%s/\n", dir, dir))
		b.WriteString(fmt.Sprintf("RUN set -eu; src=/opt/piccolo/%s; \\\n", dir))
		b.WriteString(" if [ -d \"$src/nix/store\" ]; then mkdir -p /nix/store; cp -a \"$src/nix/store/.\" /nix/store/; fi; \\\n")
		b.WriteString(" if [ -d \"$src/bin\" ]; then for bin in \"$src\"/bin/*; do [ -f \"$bin\" ] || continue; case \"$(basename \"$bin\")\" in ._*) continue;; .DS_Store) continue;; esac; install -m 0755 \"$bin\" /usr/local/bin/\"$(basename \"$bin\")\"; done; fi")
		for _, cmd := range commands {
			b.WriteString(fmt.Sprintf("; command -v %s >/dev/null", shellQuote(cmd)))
		}
		b.WriteString("\n")
	}
	return b.String()
}

func shellQuoteList(values []string) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		out = append(out, shellQuote(v))
	}
	return out
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func uniqueSorted(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, p := range in {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}
