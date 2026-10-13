package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kimus/piccolo/internal/docker"
	"github.com/kimus/piccolo/internal/manifest"
)

type fakeIntegrationRunner struct {
	pingErr error
	builds  int
	tag     string
	runs    []docker.Spec
	stdout  string
	stderr  string
	code    int
}

func (f *fakeIntegrationRunner) Ping(context.Context) error { return f.pingErr }
func (f *fakeIntegrationRunner) BuildImage(_ context.Context, _ io.Reader, opts docker.ImageBuildOptions, out io.Writer) error {
	f.builds++
	f.tag = opts.Tag
	_, _ = fmt.Fprintln(out, `{"stream":"ok"}`)
	return nil
}
func (f *fakeIntegrationRunner) Run(_ context.Context, spec docker.Spec) (docker.Process, error) {
	f.runs = append(f.runs, spec)
	return &fakeProcess{stdout: f.stdout, stderr: f.stderr, code: f.code}, nil
}

type fakeProcess struct {
	stdout string
	stderr string
	code   int
}

func (p *fakeProcess) Stdin() io.WriteCloser             { return nopWriteCloser{bytes.NewBuffer(nil)} }
func (p *fakeProcess) Stdout() io.ReadCloser             { return io.NopCloser(strings.NewReader(p.stdout)) }
func (p *fakeProcess) Stderr() io.ReadCloser             { return io.NopCloser(strings.NewReader(p.stderr)) }
func (p *fakeProcess) Wait(context.Context) (int, error) { return p.code, nil }
func (p *fakeProcess) Shutdown(_ time.Duration) error    { return nil }
func (p *fakeProcess) ExitCode() int                     { return p.code }

type nopWriteCloser struct{ *bytes.Buffer }

func (n nopWriteCloser) Close() error { return nil }

func TestIntegrationAddBuildsDescribesAndUpdatesManifest(t *testing.T) {
	dir := t.TempDir()
	writeTestManifest(t, dir, "docker.io/sipeed/picoclaw:v0.3.1")
	desc := `{"schema":"piccolo.integration.v1","name":"gws","displayName":"Google Workspace","commands":["piccolo-gws"],"setup":{"command":["piccolo-gws","setup","--config-dir","/root/.picoclaw/integrations/google"],"requiresTty":true,"ports":["127.0.0.1:8000:8000"]}}`
	fake := &fakeIntegrationRunner{stdout: desc}
	old := newIntegrationRunner
	newIntegrationRunner = func() integrationRunner { return fake }
	defer func() { newIntegrationRunner = old }()
	oldDir := dirFlag
	dirFlag = dir
	defer func() { dirFlag = oldDir }()

	var out bytes.Buffer
	cmd := testCommand(&out)
	if err := runIntegrationAdd(cmd, "gws", integrationFlags{yes: true, tag: "piccolo/picoclaw-runtime:gws"}); err != nil {
		t.Fatalf("runIntegrationAdd: %v", err)
	}
	if fake.builds != 1 || fake.tag != "piccolo/picoclaw-runtime:gws" {
		t.Fatalf("builds=%d tag=%q", fake.builds, fake.tag)
	}
	if len(fake.runs) != 1 || fake.runs[0].Entrypoint[0] != "piccolo-gws" || strings.Join(fake.runs[0].Command, " ") != "describe --json" {
		t.Fatalf("describe spec = %#v", fake.runs)
	}
	m, err := manifest.Load(filepath.Join(dir, "piccolo.yaml"))
	if err != nil {
		t.Fatalf("load manifest: %v", err)
	}
	if m.Container.Image != "piccolo/picoclaw-runtime:gws" || len(m.Runtime.Integrations) != 1 || m.Runtime.Integrations[0].Name != "gws" {
		t.Fatalf("manifest = %#v", m)
	}
}

func TestIntegrationSetupRunsSetupContainer(t *testing.T) {
	dir := t.TempDir()
	m := manifest.Manifest{
		Version:   1,
		Container: manifest.Container{Image: "piccolo/picoclaw-runtime:gws", Name: "piccolo-gateway", Network: "bridge"},
		Runtime: manifest.Runtime{Integrations: []manifest.Integration{{
			Name:             "gws",
			DisplayName:      "Google Workspace",
			Ref:              "github:kimus/piccolo-gws",
			SetupCommand:     []string{"piccolo-gws", "setup"},
			SetupRequiresTTY: true,
			SetupPorts:       []string{"127.0.0.1:8000:8000"},
		}}},
	}
	if err := m.Write(filepath.Join(dir, "piccolo.yaml")); err != nil {
		t.Fatal(err)
	}
	fake := &fakeIntegrationRunner{}
	old := newIntegrationRunner
	newIntegrationRunner = func() integrationRunner { return fake }
	defer func() { newIntegrationRunner = old }()
	oldDir := dirFlag
	dirFlag = dir
	defer func() { dirFlag = oldDir }()

	var out bytes.Buffer
	cmd := testCommand(&out)
	if err := runIntegrationSetup(cmd, "gws"); err != nil {
		t.Fatalf("runIntegrationSetup: %v", err)
	}
	if len(fake.runs) != 1 {
		t.Fatalf("runs = %d", len(fake.runs))
	}
	spec := fake.runs[0]
	if spec.Image != "piccolo/picoclaw-runtime:gws" || spec.Source != dir || spec.Entrypoint[0] != "piccolo-gws" || spec.Command[0] != "setup" || !spec.TTY {
		t.Fatalf("setup spec = %#v", spec)
	}
}
