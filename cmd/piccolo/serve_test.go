package main

import (
	"strings"
	"testing"

	"github.com/kimus/piccolo/internal/manifest"
)

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
