package manifest

import (
	"errors"
	"strings"
	"testing"
)

const validYAML = `version: 1
container:
  image: docker.io/sipeed/picoclaw:v0.3.1
  name: piccolo-gateway
  network: bridge
`

func TestParseValid(t *testing.T) {
	m, err := Parse(strings.NewReader(validYAML))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if m.Container.Image != "docker.io/sipeed/picoclaw:v0.3.1" {
		t.Errorf("image = %q", m.Container.Image)
	}
	if m.Container.Name != "piccolo-gateway" {
		t.Errorf("name = %q", m.Container.Name)
	}
}

func TestParseRejectsUnknownFields(t *testing.T) {
	_, err := Parse(strings.NewReader(validYAML + "  bogus: true\n"))
	if err == nil {
		t.Fatal("expected strict-parse error for unknown key")
	}
}

func TestParseAcceptsRuntimeToolchain(t *testing.T) {
	m, err := Parse(strings.NewReader(validYAML + `runtime:
  toolchain:
    packages:
      - poppler-utils
      - imagemagick
    commands:
      - pdfinfo
      - magick
`))
	if err != nil {
		t.Fatalf("Parse runtime toolchain: %v", err)
	}
	if strings.Join(m.Runtime.Toolchain.Packages, ",") != "poppler-utils,imagemagick" {
		t.Fatalf("packages = %#v", m.Runtime.Toolchain.Packages)
	}
	if strings.Join(m.Runtime.Toolchain.Commands, ",") != "pdfinfo,magick" {
		t.Fatalf("commands = %#v", m.Runtime.Toolchain.Commands)
	}
}

func TestParseAcceptsRuntimeIntegrations(t *testing.T) {
	m, err := Parse(strings.NewReader(validYAML + `runtime:
  integrations:
    - name: gws
      display_name: Google Workspace
      ref: github:kimus/piccolo-gws
      commands: [piccolo-gws]
      descriptor_command: [piccolo-gws, describe, --json]
      setup_command: [piccolo-gws, setup]
      setup_requires_tty: true
      setup_ports: [127.0.0.1:8000:8000]
`))
	if err != nil {
		t.Fatalf("Parse runtime integrations: %v", err)
	}
	if len(m.Runtime.Integrations) != 1 || m.Runtime.Integrations[0].Name != "gws" || !m.Runtime.Integrations[0].SetupRequiresTTY {
		t.Fatalf("integrations = %#v", m.Runtime.Integrations)
	}
}

func TestParseRejectsBadRuntimeIntegrations(t *testing.T) {
	_, err := Parse(strings.NewReader(validYAML + `runtime:
  integrations:
    - name: gws
      ref: github:kimus/piccolo-gws
    - name: gws
      ref: github:kimus/other
`))
	if err == nil {
		t.Fatal("expected duplicate integration error")
	}
}

func TestParseRejectsBadRuntimeToolchain(t *testing.T) {
	_, err := Parse(strings.NewReader(validYAML + `runtime:
  toolchain:
    packages: [file, file]
`))
	if err == nil {
		t.Fatal("expected duplicate package error")
	}
	_, err = Parse(strings.NewReader(validYAML + `runtime:
  toolchain:
    commands: ["bad command"]
`))
	if err == nil {
		t.Fatal("expected whitespace command error")
	}
}

func TestParseAcceptsSetupMetadata(t *testing.T) {
	m, err := Parse(strings.NewReader(validYAML + `setup:
  channels:
    discord:
      application_id: "123456789012345678"
`))
	if err != nil {
		t.Fatalf("Parse setup metadata: %v", err)
	}
	if m.Setup.Channels.Discord.ApplicationID != "123456789012345678" {
		t.Fatalf("application id = %q", m.Setup.Channels.Discord.ApplicationID)
	}
}

func TestParseRejectsPicoclawDomainConfig(t *testing.T) {
	// PicoClaw-domain runtime keys are still unknown in piccolo.yaml.
	_, err := Parse(strings.NewReader(validYAML + "  model: gpt-5.4\n"))
	if err == nil {
		t.Fatal("expected error for PicoClaw-domain key in piccolo.yaml")
	}
}

func TestValidateRejectsUnpinnedImage(t *testing.T) {
	for _, img := range []string{"", "sipeed/picoclaw", "sipeed/picoclaw:latest"} {
		m := Manifest{Version: 1, Container: Container{Image: img, Name: "x"}}
		if err := m.Validate(); err == nil {
			t.Errorf("image %q: expected validation error", img)
		}
	}
}

func TestValidateAcceptsPinnedImage(t *testing.T) {
	for _, img := range []string{
		"docker.io/sipeed/picoclaw:v0.3.1",
		"docker.io/sipeed/picoclaw:launcher",
		"sipeed/picoclaw@sha256:abcdef",
	} {
		if !IsPinnedImage(img) {
			t.Errorf("image %q: expected pinned", img)
		}
	}
}

func TestValidateBadVersionAndNetwork(t *testing.T) {
	m := Manifest{Version: 2, Container: Container{Image: "x:1", Name: "n"}}
	if err := m.Validate(); err == nil {
		t.Error("expected version error")
	}
	m = Manifest{Version: 1, Container: Container{Image: "x:1", Name: "n", Network: "mesh"}}
	if err := m.Validate(); err == nil {
		t.Error("expected network error")
	}
}

func TestValidateBadPorts(t *testing.T) {
	m := Manifest{Version: 1, Container: Container{Image: "x:1", Name: "n", Ports: []string{"abc"}}}
	if err := m.Validate(); err == nil {
		t.Error("expected port error")
	}
	m.Container.Ports = []string{"127.0.0.1:18800:18800"}
	if err := m.Validate(); err != nil {
		t.Errorf("valid port rejected: %v", err)
	}
}

func TestResolveEnv(t *testing.T) {
	m := Manifest{
		Version: 1,
		Container: Container{
			Image: "x:1", Name: "n",
			Env: map[string]string{
				"LITERAL": "value",
				"SECRET":  "env:HOST_SECRET",
				"AAAA":    "first",
			},
		},
	}
	env, err := m.ResolveEnv(func(k string) (string, bool) {
		if k == "HOST_SECRET" {
			return "s3cret", true
		}
		return "", false
	})
	if err != nil {
		t.Fatalf("ResolveEnv: %v", err)
	}
	want := []string{"AAAA=first", "LITERAL=value", "SECRET=s3cret"}
	if strings.Join(env, "|") != strings.Join(want, "|") {
		t.Errorf("env = %v, want sorted %v", env, want)
	}
}

func TestResolveEnvMissingRef(t *testing.T) {
	m := Manifest{Version: 1, Container: Container{Image: "x:1", Name: "n", Env: map[string]string{"K": "env:MISSING"}}}
	_, err := m.ResolveEnv(func(string) (string, bool) { return "", false })
	var ref *ErrPlaceholderEnv
	if !errors.As(err, &ref) || ref.Var != "MISSING" {
		t.Fatalf("expected ErrPlaceholderEnv, got %v", err)
	}
}

func TestEnvRefs(t *testing.T) {
	m := Manifest{Container: Container{Env: map[string]string{
		"A": "env:VAR_A", "B": "literal", "C": "env:VAR_C",
	}}}
	refs := m.EnvRefs()
	if len(refs) != 2 || refs[0] != "VAR_A" || refs[1] != "VAR_C" {
		t.Errorf("refs = %v", refs)
	}
}
