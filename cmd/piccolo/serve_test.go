package main

import (
	"strings"
	"testing"

	"github.com/kimus/piccolo/internal/manifest"
	"github.com/kimus/piccolo/internal/picoclaw"
	"github.com/kimus/piccolo/internal/workspace"
)

func TestAgentMediaMirrorMounts(t *testing.T) {
	cfg := picoclaw.Config{Agents: picoclaw.Agents{
		Defaults: picoclaw.Defaults{Workspace: "~/.picoclaw/agents/main"},
		List: []picoclaw.Agent{
			{ID: "main", Default: true},
			{ID: "research", Workspace: "~/.picoclaw/agents/research"},
		},
	}}
	layout := workspace.NewLayout("/ws")
	mounts := agentMediaMirrorMounts(layout, cfg)
	want := []string{
		"/ws/runtime/media:/root/.picoclaw/agents/main/media:rw",
		"/ws/runtime/media:/root/.picoclaw/agents/research/media:rw",
	}
	if strings.Join(mounts, "\n") != strings.Join(want, "\n") {
		t.Fatalf("mounts = %#v, want %#v", mounts, want)
	}
}

func TestHasContainerPort(t *testing.T) {
	ports := []string{"127.0.0.1:18800:18800", "9000:9000/tcp"}
	if !hasContainerPort(ports, 18800) {
		t.Error("18800 must be detected")
	}
	if !hasContainerPort(ports, 9000) {
		t.Error("9000 with proto suffix must be detected")
	}
	if hasContainerPort(ports, 18790) {
		t.Error("18790 must not be detected")
	}
	if hasContainerPort(nil, 1) {
		t.Error("empty list must not match")
	}
}

func TestWithGatewayHost(t *testing.T) {
	env := withGatewayHost([]string{"A=1"})
	if len(env) != 2 || env[1] != "PICOCLAW_GATEWAY_HOST=0.0.0.0" {
		t.Errorf("env = %v", env)
	}

	// User-provided value wins.
	env = withGatewayHost([]string{"PICOCLAW_GATEWAY_HOST=127.0.0.1"})
	if len(env) != 1 || !strings.HasSuffix(env[0], "127.0.0.1") {
		t.Errorf("user value overwritten: %v", env)
	}
}

// TestComposePortsConsoleOff asserts a console-off manifest publishes only the
// auto-added gateway/health port — never :18800 (no dead published port).
func TestComposePortsConsoleOff(t *testing.T) {
	m := &manifest.Manifest{Container: manifest.Container{
		Image:   "docker.io/sipeed/picoclaw:v0.3.1",
		Network: "bridge",
		// no :18800 binding — web console disabled
	}}
	ports := composePorts(m, 18790)
	if len(ports) != 1 || ports[0] != "127.0.0.1:18790:18790" {
		t.Fatalf("console-off ports = %v, want only the gateway port", ports)
	}
	if hasContainerPort(ports, 18800) {
		t.Error("console-off must not publish :18800")
	}
}

// TestComposePortsConsoleOn asserts a console-on manifest keeps its :18800
// binding and still auto-adds the gateway port.
func TestComposePortsConsoleOn(t *testing.T) {
	m := &manifest.Manifest{Container: manifest.Container{
		Image:   "docker.io/sipeed/picoclaw:launcher",
		Network: "bridge",
		Ports:   []string{"127.0.0.1:18800:18800"},
	}}
	ports := composePorts(m, 18790)
	if !hasContainerPort(ports, 18800) {
		t.Errorf("console-on ports = %v, want :18800 published", ports)
	}
	if !hasContainerPort(ports, 18790) {
		t.Errorf("console-on ports = %v, want gateway port auto-added", ports)
	}
}

// TestComposePortsHostNetwork asserts host networking publishes nothing extra.
func TestComposePortsHostNetwork(t *testing.T) {
	m := &manifest.Manifest{Container: manifest.Container{Network: "host"}}
	if ports := composePorts(m, 18790); len(ports) != 0 {
		t.Errorf("host-network ports = %v, want none", ports)
	}
}

func TestWebConsoleURL(t *testing.T) {
	m := &manifest.Manifest{Container: manifest.Container{
		Ports: []string{"127.0.0.1:18800:18800"},
	}}
	if got := webConsoleURL(m); got != "http://127.0.0.1:18800" {
		t.Errorf("webConsoleURL = %q", got)
	}

	m.Container.Ports = []string{"18800:18800"}
	if got := webConsoleURL(m); got != "http://127.0.0.1:18800" {
		t.Errorf("two-part binding: %q", got)
	}

	m.Container.Ports = []string{"127.0.0.1:18790:18790"}
	if got := webConsoleURL(m); got != "" {
		t.Errorf("no launcher port: %q", got)
	}
}
