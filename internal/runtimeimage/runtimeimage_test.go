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
		"COPY piccolo-setup-tools /usr/local/bin/piccolo-setup-tools",
		"RUN devbox version",
	} {
		if !strings.Contains(p1.Dockerfile, want) {
			t.Errorf("Dockerfile missing %q", want)
		}
	}
	// Tools must NOT be installed at build time (only `devbox version` runs).
	for _, bad := range []string{"\nRUN devbox global install", "\nRUN devbox install", "\nRUN devbox global add"} {
		if strings.Contains(p1.Dockerfile, bad) {
			t.Errorf("image must not install tools at build time (found %q)", bad)
		}
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
	for _, want := range []string{"piccolo-integrations-devbox.json", "github:kimus/piccolo-gws", "Installing integration packages with Devbox/Nix", "devbox install", "Finalizing integration commands", "install -m 0755", "nix-store --add-root", "nix-collect-garbage -d", "command -v 'piccolo-gws'"} {
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
	if len(b) == 0 || !strings.Contains(string(b), "Dockerfile") || !strings.Contains(string(b), "devbox.json") || !strings.Contains(string(b), "piccolo-setup-tools") {
		t.Fatalf("tar missing expected file names")
	}
	// The setup script installs packages and symlinks each tool's real store
	// binary onto PATH, so tools are cwd-independent ordinary commands.
	for _, want := range []string{"PACKAGES=", "COMMANDS=", "devbox install", "devbox shellenv", "STATE_FILE", `ln -sf "$(readlink -f "$bin")" /usr/local/bin/"$tool"`, "rm -f /usr/local/bin"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("setup script missing %q", want)
		}
	}
}
