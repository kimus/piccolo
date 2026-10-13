package docker

import (
	"testing"
)

func TestPrepareComposesSpec(t *testing.T) {
	r := New()
	pc, err := r.Prepare(Spec{
		Name:    "piccolo-gateway",
		Image:   "docker.io/sipeed/picoclaw:v0.3.1",
		Network: "bridge",
		Source:  "/abs/workspace",
		Ports:   []string{"127.0.0.1:18800:18800"},
		Env:     []string{"PICOCLAW_GATEWAY_HOST=0.0.0.0"},
		Labels:  map[string]string{LabelWorkspace: "/abs/workspace"},
	})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	cfg := pc.Config
	if cfg.Image != "docker.io/sipeed/picoclaw:v0.3.1" {
		t.Errorf("image = %q", cfg.Image)
	}
	if !cfg.OpenStdin || !cfg.AttachStdin || !cfg.AttachStdout || !cfg.AttachStderr {
		t.Error("stdio must be attached")
	}
	if cfg.Tty {
		t.Error("tty must be off (multiplexed streams)")
	}
	if cfg.Labels[LabelManaged] != "true" || cfg.Labels[LabelWorkspace] != "/abs/workspace" {
		t.Errorf("labels = %v", cfg.Labels)
	}
	if len(cfg.Env) != 1 || cfg.Env[0] != "PICOCLAW_GATEWAY_HOST=0.0.0.0" {
		t.Errorf("env = %v", cfg.Env)
	}
	if len(cfg.ExposedPorts) != 1 {
		t.Errorf("exposed ports = %v", cfg.ExposedPorts)
	}

	host := pc.HostConfig
	if len(host.Binds) != 1 || host.Binds[0] != "/abs/workspace:/root/.picoclaw:rw" {
		t.Errorf("binds = %v", host.Binds)
	}
	if host.Init == nil || !*host.Init {
		t.Error("init must be enabled (PID1 signal forwarding)")
	}
	if host.AutoRemove {
		t.Error("auto-remove must be off (explicit teardown)")
	}
	if string(host.NetworkMode) != "" {
		t.Errorf("bridge network mode = %q, want default", host.NetworkMode)
	}
	if len(host.PortBindings) != 1 {
		t.Errorf("port bindings = %v", host.PortBindings)
	}
}

func TestPrepareCommandOverrideAndTTY(t *testing.T) {
	r := New()
	pc, err := r.Prepare(Spec{Name: "setup", Image: "img:1", Source: "/ws", Entrypoint: []string{"piccolo-gws"}, Command: []string{"setup"}, TTY: true})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if len(pc.Config.Entrypoint) != 1 || pc.Config.Entrypoint[0] != "piccolo-gws" {
		t.Fatalf("entrypoint = %#v", pc.Config.Entrypoint)
	}
	if len(pc.Config.Cmd) != 1 || pc.Config.Cmd[0] != "setup" {
		t.Fatalf("cmd = %#v", pc.Config.Cmd)
	}
	if !pc.Config.Tty {
		t.Fatal("tty should be enabled")
	}
}

func TestPrepareAddsNamedVolumes(t *testing.T) {
	r := New()
	pc, err := r.Prepare(Spec{Name: "n", Image: "img:1", Source: "/ws", Volumes: []string{"piccolo-devbox-nix-cache:/nix:rw"}})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if len(pc.HostConfig.Binds) != 2 {
		t.Fatalf("binds = %v", pc.HostConfig.Binds)
	}
	if pc.HostConfig.Binds[1] != "piccolo-devbox-nix-cache:/nix:rw" {
		t.Errorf("named volume bind = %q", pc.HostConfig.Binds[1])
	}
}

func TestPrepareNetworkModes(t *testing.T) {
	r := New()
	for network, want := range map[string]string{"none": "none", "host": "host", "bridge": "", "": ""} {
		pc, err := r.Prepare(Spec{Name: "n", Image: "img:1", Source: "/ws", Network: network})
		if err != nil {
			t.Fatalf("network %q: %v", network, err)
		}
		if got := string(pc.HostConfig.NetworkMode); got != want {
			t.Errorf("network %q -> %q, want %q", network, got, want)
		}
	}
	if _, err := r.Prepare(Spec{Name: "n", Image: "img:1", Source: "/ws", Network: "mesh"}); err == nil {
		t.Error("expected error for unsupported network")
	}
}

func TestPrepareValidation(t *testing.T) {
	r := New()
	if _, err := r.Prepare(Spec{Name: "n", Source: "/ws"}); err == nil {
		t.Error("expected error without image")
	}
	if _, err := r.Prepare(Spec{Name: "n", Image: "img:1"}); err == nil {
		t.Error("expected error without source")
	}
	if _, err := r.Prepare(Spec{Name: "n", Image: "img:1", Source: "/ws", Ports: []string{"bad"}}); err == nil {
		t.Error("expected error for bad port")
	}
}

func TestParsePortSpec(t *testing.T) {
	port, binding, err := parsePortSpec("127.0.0.1:18800:18800")
	if err != nil {
		t.Fatalf("parsePortSpec: %v", err)
	}
	if binding.HostPort != "18800" || !binding.HostIP.Is4() {
		t.Errorf("binding = %+v", binding)
	}
	if port.String() == "" {
		t.Error("port not parsed")
	}

	if _, _, err := parsePortSpec("a:b:c:d"); err == nil {
		t.Error("expected error for malformed port")
	}
	if _, _, err := parsePortSpec("99999"); err == nil {
		t.Error("expected error for out-of-range port")
	}
}

func TestShortID(t *testing.T) {
	if got := shortID("0123456789abcdef"); got != "0123456789ab" {
		t.Errorf("shortID = %q", got)
	}
	if got := shortID("abc"); got != "abc" {
		t.Errorf("shortID = %q", got)
	}
}
