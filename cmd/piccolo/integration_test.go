package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kimus/piccolo/internal/docker"
	"github.com/kimus/piccolo/internal/manifest"
)

type fakeIntegrationRunner struct {
	pingErr      error
	buildErr     error
	builds       int
	tag          string
	buildContext string
	runs         []docker.Spec
	stdout       string
	stdouts      []string
	stderr       string
	code         int
}

func (f *fakeIntegrationRunner) Ping(context.Context) error { return f.pingErr }
func (f *fakeIntegrationRunner) BuildImage(_ context.Context, r io.Reader, opts docker.ImageBuildOptions, out io.Writer) error {
	f.builds++
	f.tag = opts.Tag
	if r != nil {
		data, _ := io.ReadAll(r)
		f.buildContext = string(data)
	}
	_, _ = fmt.Fprintln(out, `{"stream":"ok"}`)
	return f.buildErr
}
func (f *fakeIntegrationRunner) Run(_ context.Context, spec docker.Spec) (docker.Process, error) {
	f.runs = append(f.runs, spec)
	stdout := f.stdout
	if len(f.stdouts) > 0 {
		stdout = f.stdouts[0]
		f.stdouts = f.stdouts[1:]
	}
	return &fakeProcess{stdout: stdout, stderr: f.stderr, code: f.code}, nil
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
	desc := `{"schema":"piccolo.integration.v1","name":"gws","displayName":"Google Workspace","commands":["piccolo-gws"],"setup":{"command":["piccolo-gws","setup","--config-dir","/root/.piccolo/integrations/gws"],"requiresTty":true,"ports":["127.0.0.1:8000:8000"]}}`
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
	if spec.Image != "piccolo/picoclaw-runtime:gws" || spec.Source != filepath.Join(dir, "picoclaw") || spec.Entrypoint[0] != "piccolo-gws" || spec.Command[0] != "setup" || !spec.TTY {
		t.Fatalf("setup spec = %#v", spec)
	}
	wantBind := filepath.Join(dir, "integrations") + ":/root/.piccolo/integrations:rw"
	if len(spec.Volumes) != 1 || spec.Volumes[0] != wantBind {
		t.Fatalf("setup volumes = %#v, want only %q", spec.Volumes, wantBind)
	}
}

func TestIntegrationUpgradeValidationErrors(t *testing.T) {
	dir := t.TempDir()
	writeTestManifest(t, dir, "piccolo/picoclaw-runtime:old")
	oldDir := dirFlag
	dirFlag = dir
	defer func() { dirFlag = oldDir }()
	var out bytes.Buffer
	cmd := testCommand(&out)
	if err := runIntegrationUpgrade(cmd, "", integrationUpgradeFlags{}); err == nil || !strings.Contains(err.Error(), "<name>") {
		t.Fatalf("missing target error = %v", err)
	}
	if err := runIntegrationUpgrade(cmd, "", integrationUpgradeFlags{all: true}); err == nil || !strings.Contains(err.Error(), "no integrations installed") {
		t.Fatalf("no integrations error = %v", err)
	}
}

func TestIntegrationUpgradeUnknownIntegrationDoesNotBuild(t *testing.T) {
	dir := t.TempDir()
	writeIntegrationManifest(t, dir, []manifest.Integration{testManifestIntegration("gws", "github:kimus/piccolo-gws")})
	fake := &fakeIntegrationRunner{}
	old := newIntegrationRunner
	newIntegrationRunner = func() integrationRunner { return fake }
	defer func() { newIntegrationRunner = old }()
	oldDir := dirFlag
	dirFlag = dir
	defer func() { dirFlag = oldDir }()
	if err := runIntegrationUpgrade(testCommand(&bytes.Buffer{}), "ghost", integrationUpgradeFlags{integrationFlags: integrationFlags{yes: true}}); err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("unknown error = %v", err)
	}
	if fake.builds != 0 {
		t.Fatalf("builds = %d, want 0", fake.builds)
	}
}

func TestIntegrationUpgradeSinglePreservesOtherIntegrations(t *testing.T) {
	dir := t.TempDir()
	writeIntegrationManifest(t, dir, []manifest.Integration{
		testManifestIntegration("gws", "github:kimus/piccolo-gws"),
		testManifestIntegration("other", "github:example/other?ref=v1"),
	})
	desc := `{"schema":"piccolo.integration.v1","name":"gws","displayName":"Google Workspace v2","commands":["piccolo-gws"],"setup":{"command":["piccolo-gws","setup"],"requiresTty":true}}`
	fake := &fakeIntegrationRunner{stdout: desc}
	old := newIntegrationRunner
	newIntegrationRunner = func() integrationRunner { return fake }
	defer func() { newIntegrationRunner = old }()
	oldDir := dirFlag
	dirFlag = dir
	defer func() { dirFlag = oldDir }()
	if err := runIntegrationUpgrade(testCommand(&bytes.Buffer{}), "gws", integrationUpgradeFlags{integrationFlags: integrationFlags{yes: true, tag: "piccolo/picoclaw-runtime:new"}}); err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	if fake.builds != 1 || len(fake.runs) != 1 {
		t.Fatalf("builds=%d runs=%d", fake.builds, len(fake.runs))
	}
	if !strings.Contains(fake.buildContext, "github:kimus/piccolo-gws") || !strings.Contains(fake.buildContext, "github:example/other?ref=v1") {
		t.Fatalf("build context missing integration refs")
	}
	m, err := manifest.Load(filepath.Join(dir, "piccolo.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if m.Container.Image != "piccolo/picoclaw-runtime:new" || len(m.Runtime.Integrations) != 2 {
		t.Fatalf("manifest = %#v", m)
	}
	if m.Runtime.Integrations[0].DisplayName != "Google Workspace v2" || m.Runtime.Integrations[1].Name != "other" {
		t.Fatalf("integrations = %#v", m.Runtime.Integrations)
	}
}

func TestIntegrationUpgradeAllRefreshesDescriptors(t *testing.T) {
	dir := t.TempDir()
	writeIntegrationManifest(t, dir, []manifest.Integration{
		testManifestIntegration("gws", "github:kimus/piccolo-gws"),
		testManifestIntegration("other", "github:example/other?ref=v1"),
	})
	fake := &fakeIntegrationRunner{stdouts: []string{
		`{"schema":"piccolo.integration.v1","name":"gws","displayName":"Google Workspace refreshed","commands":["piccolo-gws"],"setup":{"command":["piccolo-gws","setup"]}}`,
		`{"schema":"piccolo.integration.v1","name":"other","displayName":"Other refreshed","commands":["other"],"setup":{"command":["other","setup"]}}`,
	}}
	old := newIntegrationRunner
	newIntegrationRunner = func() integrationRunner { return fake }
	defer func() { newIntegrationRunner = old }()
	oldDir := dirFlag
	dirFlag = dir
	defer func() { dirFlag = oldDir }()
	if err := runIntegrationUpgrade(testCommand(&bytes.Buffer{}), "", integrationUpgradeFlags{all: true, integrationFlags: integrationFlags{yes: true, tag: "piccolo/picoclaw-runtime:all"}}); err != nil {
		t.Fatalf("upgrade all: %v", err)
	}
	if len(fake.runs) != 2 {
		t.Fatalf("runs = %d", len(fake.runs))
	}
	m, err := manifest.Load(filepath.Join(dir, "piccolo.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if m.Runtime.Integrations[0].DisplayName != "Google Workspace refreshed" || m.Runtime.Integrations[1].DisplayName != "Other refreshed" {
		t.Fatalf("integrations = %#v", m.Runtime.Integrations)
	}
}

func TestIntegrationUpgradeToSwitchesRef(t *testing.T) {
	dir := t.TempDir()
	writeIntegrationManifest(t, dir, []manifest.Integration{testManifestIntegration("gws", "github:kimus/piccolo-gws?ref=v1")})
	desc := `{"schema":"piccolo.integration.v1","name":"gws","displayName":"Google Workspace","commands":["piccolo-gws"],"setup":{"command":["piccolo-gws","setup"]}}`
	fake := &fakeIntegrationRunner{stdout: desc}
	old := newIntegrationRunner
	newIntegrationRunner = func() integrationRunner { return fake }
	defer func() { newIntegrationRunner = old }()
	oldDir := dirFlag
	dirFlag = dir
	defer func() { dirFlag = oldDir }()
	if err := runIntegrationUpgrade(testCommand(&bytes.Buffer{}), "gws", integrationUpgradeFlags{integrationFlags: integrationFlags{yes: true, tag: "piccolo/picoclaw-runtime:v2"}, to: "github:kimus/piccolo-gws?ref=v2"}); err != nil {
		t.Fatalf("upgrade --to: %v", err)
	}
	if !strings.Contains(fake.buildContext, "github:kimus/piccolo-gws?ref=v2") {
		t.Fatalf("build context = %q", fake.buildContext)
	}
	m, err := manifest.Load(filepath.Join(dir, "piccolo.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if got := m.Runtime.Integrations[0].Ref; got != "github:kimus/piccolo-gws?ref=v2" {
		t.Fatalf("ref = %q", got)
	}
}

func TestIntegrationUpgradeFailuresDoNotWriteManifest(t *testing.T) {
	dir := t.TempDir()
	writeIntegrationManifest(t, dir, []manifest.Integration{testManifestIntegration("gws", "github:kimus/piccolo-gws")})
	path := filepath.Join(dir, "piccolo.yaml")
	before, _ := io.ReadAll(mustOpen(t, path))
	fake := &fakeIntegrationRunner{buildErr: fmt.Errorf("boom")}
	old := newIntegrationRunner
	newIntegrationRunner = func() integrationRunner { return fake }
	defer func() { newIntegrationRunner = old }()
	oldDir := dirFlag
	dirFlag = dir
	defer func() { dirFlag = oldDir }()
	if err := runIntegrationUpgrade(testCommand(&bytes.Buffer{}), "gws", integrationUpgradeFlags{integrationFlags: integrationFlags{yes: true}}); err == nil {
		t.Fatal("expected build error")
	}
	after, _ := io.ReadAll(mustOpen(t, path))
	if string(after) != string(before) {
		t.Fatalf("manifest changed after build failure")
	}

	fake = &fakeIntegrationRunner{stdout: `not-json`}
	newIntegrationRunner = func() integrationRunner { return fake }
	if err := runIntegrationUpgrade(testCommand(&bytes.Buffer{}), "gws", integrationUpgradeFlags{integrationFlags: integrationFlags{yes: true}}); err == nil {
		t.Fatal("expected descriptor error")
	}
	after, _ = io.ReadAll(mustOpen(t, path))
	if string(after) != string(before) {
		t.Fatalf("manifest changed after descriptor failure")
	}
}

func TestIntegrationUpgradeDescriptorContainerMounts(t *testing.T) {
	dir := t.TempDir()
	writeIntegrationManifest(t, dir, []manifest.Integration{testManifestIntegration("gws", "github:kimus/piccolo-gws")})
	desc := `{"schema":"piccolo.integration.v1","name":"gws","displayName":"Google Workspace","commands":["piccolo-gws"],"setup":{"command":["piccolo-gws","setup"]}}`
	fake := &fakeIntegrationRunner{stdout: desc}
	old := newIntegrationRunner
	newIntegrationRunner = func() integrationRunner { return fake }
	defer func() { newIntegrationRunner = old }()
	oldDir := dirFlag
	dirFlag = dir
	defer func() { dirFlag = oldDir }()
	if err := runIntegrationUpgrade(testCommand(&bytes.Buffer{}), "gws", integrationUpgradeFlags{integrationFlags: integrationFlags{yes: true}}); err != nil {
		t.Fatal(err)
	}
	if len(fake.runs) != 1 {
		t.Fatalf("runs = %d", len(fake.runs))
	}
	spec := fake.runs[0]
	wantSource := filepath.Join(dir, "picoclaw")
	wantBind := filepath.Join(dir, "integrations") + ":/root/.piccolo/integrations:rw"
	if spec.Source != wantSource || len(spec.Volumes) != 1 || spec.Volumes[0] != wantBind {
		t.Fatalf("spec mounts source=%q volumes=%#v", spec.Source, spec.Volumes)
	}
	for _, v := range spec.Volumes {
		if strings.Contains(v, ":/nix:") {
			t.Fatalf("upgrade descriptor must not mount /nix: %#v", spec.Volumes)
		}
	}
}

func writeIntegrationManifest(t *testing.T, dir string, integrations []manifest.Integration) {
	t.Helper()
	m := manifest.Manifest{
		Version:   1,
		Container: manifest.Container{Image: "piccolo/picoclaw-runtime:old", Name: "piccolo-gateway", Network: "bridge"},
		Runtime:   manifest.Runtime{Integrations: integrations},
	}
	if err := m.Write(filepath.Join(dir, "piccolo.yaml")); err != nil {
		t.Fatal(err)
	}
}

func testManifestIntegration(name, ref string) manifest.Integration {
	cmd := name
	if name == "gws" {
		cmd = "piccolo-gws"
	}
	return manifest.Integration{
		Name:              name,
		DisplayName:       name,
		Ref:               ref,
		Commands:          []string{cmd},
		DescriptorCommand: []string{cmd, "describe", "--json"},
		SetupCommand:      []string{cmd, "setup"},
	}
}

func mustOpen(t *testing.T, path string) *bytes.Reader {
	t.Helper()
	data, err := io.ReadAll(mustOpenFile(t, path))
	if err != nil {
		t.Fatal(err)
	}
	return bytes.NewReader(data)
}

func mustOpenFile(t *testing.T, path string) io.ReadCloser {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestIntegrationStatusSuccessAndMounts(t *testing.T) {
	dir := t.TempDir()
	writeIntegrationManifest(t, dir, []manifest.Integration{testManifestIntegration("gws", "github:kimus/piccolo-gws")})
	m, err := manifest.Load(filepath.Join(dir, "piccolo.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	m.Runtime.Integrations[0].StatusCommand = []string{"piccolo-gws", "status", "--json"}
	if err := m.Write(filepath.Join(dir, "piccolo.yaml")); err != nil {
		t.Fatal(err)
	}
	fake := &fakeIntegrationRunner{stdout: `{"schema":"piccolo.integration.status.v1","name":"gws","status":"ok","summary":"connected","checks":[{"id":"gmail","label":"Gmail","status":"ok","message":"connected"}]}`}
	old := newIntegrationRunner
	newIntegrationRunner = func() integrationRunner { return fake }
	defer func() { newIntegrationRunner = old }()
	oldDir := dirFlag
	dirFlag = dir
	defer func() { dirFlag = oldDir }()
	var out bytes.Buffer
	if err := runIntegrationStatus(testCommand(&out), "gws", integrationStatusFlags{}); err != nil {
		t.Fatalf("status: %v", err)
	}
	if !strings.Contains(out.String(), "gws: OK") || !strings.Contains(out.String(), "Gmail") {
		t.Fatalf("output = %s", out.String())
	}
	if len(fake.runs) != 1 {
		t.Fatalf("runs = %d", len(fake.runs))
	}
	spec := fake.runs[0]
	wantBind := filepath.Join(dir, "integrations") + ":/root/.piccolo/integrations:rw"
	if spec.Source != filepath.Join(dir, "picoclaw") || len(spec.Volumes) != 1 || spec.Volumes[0] != wantBind {
		t.Fatalf("spec mounts source=%q volumes=%#v", spec.Source, spec.Volumes)
	}
	if strings.Contains(strings.Join(spec.Volumes, "\n"), ":/nix:") {
		t.Fatalf("must not mount /nix: %#v", spec.Volumes)
	}
}

func TestIntegrationStatusFailExitsError(t *testing.T) {
	dir := t.TempDir()
	writeIntegrationManifest(t, dir, []manifest.Integration{testManifestIntegration("gws", "github:kimus/piccolo-gws")})
	m, _ := manifest.Load(filepath.Join(dir, "piccolo.yaml"))
	m.Runtime.Integrations[0].StatusCommand = []string{"piccolo-gws", "status", "--json"}
	_ = m.Write(filepath.Join(dir, "piccolo.yaml"))
	fake := &fakeIntegrationRunner{stdout: `{"schema":"piccolo.integration.status.v1","name":"gws","status":"fail","summary":"broken","checks":[{"id":"auth","label":"Auth","status":"fail","message":"login required"}]}`}
	old := newIntegrationRunner
	newIntegrationRunner = func() integrationRunner { return fake }
	defer func() { newIntegrationRunner = old }()
	oldDir := dirFlag
	dirFlag = dir
	defer func() { dirFlag = oldDir }()
	if err := runIntegrationStatus(testCommand(&bytes.Buffer{}), "gws", integrationStatusFlags{}); err == nil {
		t.Fatal("expected failed status error")
	}
}

func TestIntegrationStatusUnsupportedUnknownAndAllJSON(t *testing.T) {
	dir := t.TempDir()
	writeIntegrationManifest(t, dir, []manifest.Integration{
		testManifestIntegration("gws", "github:kimus/piccolo-gws"),
		testManifestIntegration("other", "github:example/other?ref=v1"),
	})
	m, _ := manifest.Load(filepath.Join(dir, "piccolo.yaml"))
	m.Runtime.Integrations[1].StatusCommand = []string{"other", "status", "--json"}
	_ = m.Write(filepath.Join(dir, "piccolo.yaml"))
	fake := &fakeIntegrationRunner{stdout: `{"schema":"piccolo.integration.status.v1","name":"other","status":"ok","checks":[{"id":"x","label":"X","status":"ok"}]}`}
	old := newIntegrationRunner
	newIntegrationRunner = func() integrationRunner { return fake }
	defer func() { newIntegrationRunner = old }()
	oldDir := dirFlag
	dirFlag = dir
	defer func() { dirFlag = oldDir }()
	var out bytes.Buffer
	if err := runIntegrationStatus(testCommand(&out), "", integrationStatusFlags{all: true, json: true}); err != nil {
		t.Fatalf("status all: %v", err)
	}
	if !strings.Contains(out.String(), "piccolo.integration.status.aggregate.v1") || !strings.Contains(out.String(), "status-command") {
		t.Fatalf("json output = %s", out.String())
	}
	if err := runIntegrationStatus(testCommand(&bytes.Buffer{}), "ghost", integrationStatusFlags{}); err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("unknown error = %v", err)
	}
}
