// Package runtimeimage renders the deterministic local Piccolo runtime image
// build inputs. The build context is generated on demand and is not written
// into the user's workspace.
//
// Design: the image stays lean — it installs only the Devbox binary plus a
// setup script. The actual file-handling tools are installed AFTER `docker run`
// (by `piccolo serve`/runtime apply) into Devbox's default HOME-based global
// location, which is backed by a named volume so the installed tools and the
// Nix store persist across container recreation. Tools land on PATH, so they
// work from any working directory without wrappers or a per-workspace
// devbox.json.
package runtimeimage

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
)

const (
	DefaultBaseImage = "docker.io/sipeed/picoclaw:v0.3.1"
	DefaultTagPrefix = "piccolo/picoclaw-runtime"

	// DevboxGlobalDir is Devbox's default HOME-based global project location
	// (the PicoClaw image runs with HOME=/root). `devbox global install`
	// populates it; DevboxGlobalBin is then added to PATH so tools resolve
	// from any cwd.
	DevboxGlobalDir = "/root/.local/share/devbox/global/default"
	DevboxGlobalBin = DevboxGlobalDir + "/.devbox/nix/profile/default/bin"

	// MediaTempDir is mirrored from workspace runtime/media so deterministic
	// tools can read channel attachments.
	MediaTempDir = "/tmp/picoclaw_media"

	// SetupScriptPath is baked into the image and run by `piccolo serve` after
	// the gateway is healthy. It installs the Devbox packages and symlinks each
	// tool's real /nix/store binary into /usr/local/bin, so tools become
	// ordinary PATH commands that work from any cwd with relative or absolute
	// args (no per-call devbox overhead, no cwd rewrite). `devbox run` cds to
	// the project root and Devbox's profile-bin links only a subset of packages
	// in this daemon-less Nix setup, so resolving real binaries via
	// `devbox shellenv` is the reliable path.
	SetupScriptPath = "/usr/local/bin/piccolo-setup-tools"
)

// DefaultCommands are the command names symlinked onto PATH by the setup
// script. Each is provided by one of DefaultPackages.
var DefaultCommands = []string{
	"zbarimg", "file", "python3", "jq", "yq",
	"pdftotext", "pdfinfo", "pdftoppm", "pdftocairo",
	"tesseract", "pandoc", "xmllint", "exiftool",
	"ffprobe", "ffmpeg", "sqlite3", "unzip", "7z", "7za",
	"libreoffice", "soffice", "magick", "convert", "identify",
}

// DefaultTools is kept as a compatibility alias for callers/tests that still
// refer to the old name.
var DefaultTools = DefaultCommands

// DefaultPackages is the standard file-handling toolset installed into the
// Devbox global environment after the container starts.
var DefaultPackages = []string{
	"zbar",
	"file",
	"python3",
	"jq",
	"yq-go",
	"poppler-utils",
	"tesseract",
	"pandoc",
	"libxml2",
	"exiftool",
	"ffmpeg",
	"sqlite",
	"unzip",
	"p7zip",
	"libreoffice",
	"imagemagick",
}

type Options struct {
	BaseImage    string
	Tag          string
	Packages     []string
	Commands     []string
	Integrations []Integration
}

// Integration is a build-time package that must be available before the
// gateway entrypoint starts (for example, a stdio MCP server binary).
type Integration struct {
	Ref      string
	Commands []string
}

type Plan struct {
	BaseImage        string
	Tag              string
	Dockerfile       string
	DevboxJSON       string
	IntegrationsJSON string
	Packages         []string
	Commands         []string
	Integrations     []Integration
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
	devbox, err := renderDevbox(packages)
	if err != nil {
		return Plan{}, err
	}
	integrations := normalizeIntegrations(opts.Integrations)
	integrationsJSON, err := renderIntegrationsDevbox(integrations)
	if err != nil {
		return Plan{}, err
	}
	dockerfile := renderDockerfile(base, integrations)
	tag := strings.TrimSpace(opts.Tag)
	if tag == "" {
		tag = DefaultTag(base, dockerfile, devbox, integrationsJSON)
	}
	return Plan{BaseImage: base, Tag: tag, Dockerfile: dockerfile, DevboxJSON: devbox, IntegrationsJSON: integrationsJSON, Packages: packages, Commands: commands, Integrations: integrations}, nil
}

func IsLocalRuntimeImage(ref string) bool {
	return strings.HasPrefix(ref, DefaultTagPrefix+":")
}

// DefaultDevboxJSON renders the global Devbox project config (package list
// only). It is baked into the image at DevboxGlobalDir/devbox.json and used by
// `devbox global install` at runtime; tools are NOT installed at build time.
func DefaultDevboxJSON() (string, error) {
	return renderDevbox(uniqueSorted(DefaultPackages))
}

func DefaultTag(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	return fmt.Sprintf("%s:%s", DefaultTagPrefix, hex.EncodeToString(sum[:])[:12])
}

func (p Plan) ContextTar() (io.Reader, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	files := []struct {
		name string
		body string
	}{
		{name: "Dockerfile", body: p.Dockerfile},
		{name: "devbox.json", body: p.DevboxJSON},
		{name: "piccolo-integrations-devbox.json", body: p.IntegrationsJSON},
		{name: "piccolo-setup-tools", body: renderSetupScript()},
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
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return bytes.NewReader(buf.Bytes()), nil
}

// renderDockerfile installs the Devbox binary, the global project config, the
// nixbld group Nix needs, and the setup script. The tools themselves are
// installed after run (the setup script, triggered by `piccolo serve`) into the
// persisted volumes and symlinked onto PATH. The default PicoClaw entrypoint is
// left untouched.
func renderDockerfile(base string, integrations []Integration) string {
	integrationBlock := ""
	if len(integrations) > 0 {
		integrationBlock = renderIntegrationInstallBlock(integrations)
	}
	return fmt.Sprintf(`FROM %s
USER root
RUN apk add --no-cache bash curl ca-certificates xz tar git shadow coreutils
# Nix (bootstrapped by Devbox at runtime) requires the 'nixbld' build group and
# users to exist. They normally live in /etc/group + /etc/passwd, which are
# ephemeral and reset on container recreation, breaking the tool install after a
# restart ("group 'nixbld' does not exist"). Bake them into the image so they
# always survive; the runtime Nix installer detects and reuses them.
RUN groupadd -g 30000 nixbld \
 && i=1; while [ "$i" -le 32 ]; do useradd -M -N -g nixbld -G nixbld -u "$((30000 + i))" -s /sbin/nologin "nixbld$i"; i=$((i + 1)); done
RUN curl -fsSL https://get.jetify.com/devbox | bash -s -- -f
RUN mkdir -p %[2]s
COPY devbox.json %[2]s/devbox.json
COPY piccolo-setup-tools %[3]s
RUN chmod 0755 %[3]s
ENV PATH="/root/.nix-profile/bin:/nix/var/nix/profiles/default/bin:${PATH}"
RUN devbox version
%sLABEL org.opencontainers.image.title="Piccolo PicoClaw Runtime"
LABEL org.opencontainers.image.description="PicoClaw runtime with Devbox/Nix; file-handling tools install into persisted volumes after start"
`, base, DevboxGlobalDir, SetupScriptPath, integrationBlock)
}

// renderSetupScript is run by `piccolo serve` after the gateway is healthy. It
// installs the configured Devbox packages (idempotent; fast when the /nix
// volume is warm) and symlinks each configured tool's real store binary into
// /usr/local/bin so tools are ordinary cwd-independent PATH commands. Packages
// and commands are passed as space-separated argv values by Piccolo; defaults
// are used only for backwards compatibility with older callers.
func renderSetupScript() string {
	return `#!/bin/sh
set -eu
PROJ=` + DevboxGlobalDir + `
STATE_DIR=/usr/local/share/piccolo
STATE_FILE=$STATE_DIR/toolchain.commands
PACKAGES="${1:-` + strings.Join(DefaultPackages, " ") + `}"
COMMANDS="${2:-` + strings.Join(DefaultCommands, " ") + `}"
mkdir -p "$PROJ" "$STATE_DIR"
cd "$PROJ"
{
  printf '{\n  "packages": [\n'
  first=1
  for pkg in $PACKAGES; do
    if [ "$first" = 1 ]; then first=0; else printf ',\n'; fi
    printf '    "%s"' "$pkg"
  done
  printf '\n  ]\n}\n'
} > devbox.json

devbox install >/dev/null 2>&1 || devbox install
if [ -f "$STATE_FILE" ]; then
  for old in $(cat "$STATE_FILE"); do
    keep=0
    for tool in $COMMANDS; do [ "$old" = "$tool" ] && keep=1; done
    if [ "$keep" = 0 ] && [ -L /usr/local/bin/"$old" ]; then
      rm -f /usr/local/bin/"$old"
    fi
  done
fi

eval "$(devbox shellenv 2>/dev/null)"
: > "$STATE_FILE"
missing=""
for tool in $COMMANDS; do
  bin="$(command -v "$tool" 2>/dev/null || true)"
  if [ -n "$bin" ]; then
    ln -sf "$(readlink -f "$bin")" /usr/local/bin/"$tool"
    printf '%s\n' "$tool" >> "$STATE_FILE"
  else
    missing="$missing $tool"
  fi
done
if [ -n "$missing" ]; then
  echo "missing configured commands:$missing" >&2
  exit 2
fi
`
}

func renderDevbox(packages []string) (string, error) {
	data := map[string]any{"packages": packages}
	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b) + "\n", nil
}

func renderIntegrationsDevbox(integrations []Integration) (string, error) {
	pkgs := make([]string, 0, len(integrations))
	for _, in := range integrations {
		pkgs = append(pkgs, in.Ref)
	}
	return renderDevbox(pkgs)
}

func normalizeIntegrations(in []Integration) []Integration {
	byRef := map[string]Integration{}
	for _, item := range in {
		ref := strings.TrimSpace(item.Ref)
		if ref == "" {
			continue
		}
		cur := byRef[ref]
		cur.Ref = ref
		cur.Commands = uniqueSorted(append(cur.Commands, item.Commands...))
		byRef[ref] = cur
	}
	refs := make([]string, 0, len(byRef))
	for ref := range byRef {
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	out := make([]Integration, 0, len(refs))
	for _, ref := range refs {
		out = append(out, byRef[ref])
	}
	return out
}

func renderIntegrationInstallBlock(integrations []Integration) string {
	var commands []string
	for _, in := range integrations {
		commands = append(commands, in.Commands...)
	}
	commands = uniqueSorted(commands)
	var verify strings.Builder
	for _, cmd := range commands {
		verify.WriteString(fmt.Sprintf("RUN command -v %s >/dev/null\n", shellQuote(cmd)))
	}
	return fmt.Sprintf(`COPY piccolo-integrations-devbox.json /opt/piccolo/integrations/devbox.json
ARG PICCOLO_INTEGRATIONS_CACHE_BUST=static
RUN echo "Integration cache key: ${PICCOLO_INTEGRATIONS_CACHE_BUST}" >/dev/null \
 && cd /opt/piccolo/integrations \
 && echo "Installing integration packages with Devbox/Nix; this can take several minutes on first run..." \
 && devbox install \
 && echo "Finalizing integration commands; this may take a moment..." \
 && eval "$(devbox shellenv)" \
 && mkdir -p /nix/var/nix/gcroots/piccolo-integrations \
 && for tool in %s; do bin="$(readlink -f "$(command -v "$tool")")"; install -m 0755 "$bin" /usr/local/bin/"$tool"; nix-store --add-root /nix/var/nix/gcroots/piccolo-integrations/"$tool" --indirect "$bin" >/dev/null; done \
 && echo "Pruning integration build dependencies from the image layer..." \
 && nix-collect-garbage -d >/dev/null
%s`, strings.Join(shellQuoteList(commands), " "), verify.String())
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
