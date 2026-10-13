package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kimus/piccolo/internal/docker"
	picintegration "github.com/kimus/piccolo/internal/integration"
	"github.com/kimus/piccolo/internal/manifest"
	"github.com/kimus/piccolo/internal/runtimeimage"
)

type fakeIntegrationRunner struct {
	pingErr      error
	buildErr     error
	builds       int
	tag          string
	buildArgs    map[string]string
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
	f.buildArgs = opts.BuildArgs
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
	stdout, stderr string
	code           int
}

func (p *fakeProcess) Stdin() io.WriteCloser             { return nopWriteCloser{bytes.NewBuffer(nil)} }
func (p *fakeProcess) Stdout() io.ReadCloser             { return io.NopCloser(strings.NewReader(p.stdout)) }
func (p *fakeProcess) Stderr() io.ReadCloser             { return io.NopCloser(strings.NewReader(p.stderr)) }
func (p *fakeProcess) Wait(context.Context) (int, error) { return p.code, nil }
func (p *fakeProcess) Shutdown(time.Duration) error      { return nil }
func (p *fakeProcess) ExitCode() int                     { return p.code }

type nopWriteCloser struct{ *bytes.Buffer }

func (n nopWriteCloser) Close() error { return nil }

type fakeReleaseFetcher struct {
	release fetchedRelease
	err     error
}

func (f fakeReleaseFetcher) Fetch(context.Context, picintegration.PackageRef) (fetchedRelease, error) {
	return f.release, f.err
}

func TestIntegrationAddReleaseBuildsDescribesAndUpdatesManifest(t *testing.T) {
	dir := t.TempDir()
	writeTestManifest(t, dir, "docker.io/sipeed/picoclaw:v0.3.1")
	bundleRoot := makeBundleDir(t, []string{"piccolo-gws", "gws"})
	rel := fetchedRelease{Ref: mustPackageRef(t, "kimus/piccolo-gws"), Version: "3.1.0", Digest: "sha256:abc", Bundle: runtimeimage.IntegrationBundle{Package: "github:kimus/piccolo-gws", Version: "3.1.0", Root: bundleRoot, Commands: []string{"piccolo-gws", "gws"}}}
	desc := `{"schema":"piccolo.integration.v1","name":"gws","displayName":"Google Workspace","commands":["piccolo-gws"],"setup":{"command":["piccolo-gws","setup","--config-dir","/root/.piccolo/integrations/gws"],"requiresTty":true,"ports":["127.0.0.1:8000:8000"]},"commands":{"status":{"command":"piccolo-gws","args":["status","--config-dir","/root/.piccolo/integrations/gws","--json"]},"setup":{"command":"piccolo-gws","args":["setup","--config-dir","/root/.piccolo/integrations/gws"]}}}`
	fake := &fakeIntegrationRunner{stdout: desc}
	oldRunner, oldFetcher := newIntegrationRunner, newReleaseFetcher
	newIntegrationRunner = func() integrationRunner { return fake }
	newReleaseFetcher = func() releaseFetcher { return fakeReleaseFetcher{release: rel} }
	defer func() { newIntegrationRunner = oldRunner; newReleaseFetcher = oldFetcher }()
	oldDir := dirFlag
	dirFlag = dir
	defer func() { dirFlag = oldDir }()
	var out bytes.Buffer
	if err := runIntegrationAdd(testCommand(&out), "kimus/piccolo-gws", integrationFlags{yes: true, tag: "piccolo/picoclaw-runtime:gws"}); err != nil {
		t.Fatalf("add: %v", err)
	}
	if fake.builds != 1 || fake.tag != "piccolo/picoclaw-runtime:gws" {
		t.Fatalf("builds=%d tag=%q", fake.builds, fake.tag)
	}
	if !strings.Contains(fake.buildContext, "release-integrations/bundle-0/bin/piccolo-gws") {
		t.Fatalf("build context missing bundle: %q", fake.buildContext)
	}
	m, err := manifest.Load(filepath.Join(dir, "piccolo.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	in := m.Runtime.Integrations[0]
	if in.Package != "github:kimus/piccolo-gws" || in.Source != "release" || in.Channel != "latest" || in.ResolvedVersion != "3.1.0" || in.StatePath != "integrations/kimus/piccolo-gws" {
		t.Fatalf("integration = %#v", in)
	}
	if !strings.Contains(strings.Join(in.SetupCommand, " "), "/root/.piccolo/integrations/kimus/piccolo-gws") {
		t.Fatalf("setup command not rewritten: %#v", in.SetupCommand)
	}
}

func TestIntegrationAddRejectsAlias(t *testing.T) {
	dir := t.TempDir()
	writeTestManifest(t, dir, "docker.io/sipeed/picoclaw:v0.3.1")
	oldDir := dirFlag
	dirFlag = dir
	defer func() { dirFlag = oldDir }()
	if err := runIntegrationAdd(testCommand(&bytes.Buffer{}), "gws", integrationFlags{yes: true}); err == nil || !strings.Contains(err.Error(), "package refs") {
		t.Fatalf("err = %v", err)
	}
}

func TestIntegrationListHumanAndJSON(t *testing.T) {
	dir := t.TempDir()
	writeIntegrationManifest(t, dir, []manifest.Integration{{Package: "github:kimus/piccolo-gws", Requested: "kimus/piccolo-gws", Source: "release", Channel: "latest", ResolvedVersion: "3.1.0", StatePath: "integrations/kimus/piccolo-gws", Name: "gws", DisplayName: "Google Workspace MCP Server", Commands: []string{"piccolo-gws"}}})
	oldDir := dirFlag
	dirFlag = dir
	defer func() { dirFlag = oldDir }()
	var out bytes.Buffer
	if err := runIntegrationList(testCommand(&out), false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "kimus/piccolo-gws") || !strings.Contains(out.String(), "3.1.0") {
		t.Fatalf("out=%s", out.String())
	}
	out.Reset()
	if err := runIntegrationList(testCommand(&out), true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"package": "github:kimus/piccolo-gws"`) {
		t.Fatalf("json=%s", out.String())
	}
}

func TestIntegrationUpgradePinnedGuidance(t *testing.T) {
	dir := t.TempDir()
	writeIntegrationManifest(t, dir, []manifest.Integration{{Package: "github:kimus/piccolo-gws", Requested: "kimus/piccolo-gws@3.1.0", Source: "release", Channel: "fixed", Selector: "3.1.0", ResolvedVersion: "3.1.0", Name: "gws", Commands: []string{"piccolo-gws"}}})
	oldDir := dirFlag
	dirFlag = dir
	defer func() { dirFlag = oldDir }()
	err := runIntegrationUpgrade(testCommand(&bytes.Buffer{}), "kimus/piccolo-gws", integrationUpgradeFlags{integrationFlags: integrationFlags{yes: true}})
	if err == nil || !strings.Contains(err.Error(), "pinned") {
		t.Fatalf("err=%v", err)
	}
}

func TestIntegrationSetupAndStatusUsePackageIdentity(t *testing.T) {
	dir := t.TempDir()
	writeIntegrationManifest(t, dir, []manifest.Integration{{Package: "github:kimus/piccolo-gws", Source: "release", Channel: "latest", Name: "gws", DisplayName: "Google Workspace", Commands: []string{"piccolo-gws"}, SetupCommand: []string{"piccolo-gws", "setup"}, StatusCommand: []string{"piccolo-gws", "status", "--json"}, SetupRequiresTTY: true}})
	fake := &fakeIntegrationRunner{stdout: `{"schema":"piccolo.integration.status.v1","name":"gws","status":"ok","summary":"connected","checks":[{"id":"gmail","label":"Gmail","status":"ok","message":"connected"}]}`}
	old := newIntegrationRunner
	newIntegrationRunner = func() integrationRunner { return fake }
	defer func() { newIntegrationRunner = old }()
	oldDir := dirFlag
	dirFlag = dir
	defer func() { dirFlag = oldDir }()
	if err := runIntegrationSetup(testCommand(&bytes.Buffer{}), "kimus/piccolo-gws"); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := runIntegrationStatus(testCommand(&bytes.Buffer{}), "kimus/piccolo-gws", integrationStatusFlags{}); err != nil {
		t.Fatalf("status: %v", err)
	}
	if len(fake.runs) != 2 {
		t.Fatalf("runs=%d", len(fake.runs))
	}
}

func TestIntegrationBuildSourceWarnsAndBuilds(t *testing.T) {
	dir := t.TempDir()
	writeTestManifest(t, dir, "docker.io/sipeed/picoclaw:v0.3.1")
	desc := `{"schema":"piccolo.integration.v1","name":"gws","displayName":"Google Workspace","commands":["piccolo-gws"],"setup":{"command":["piccolo-gws","setup"]}}`
	fake := &fakeIntegrationRunner{stdout: desc}
	old := newIntegrationRunner
	newIntegrationRunner = func() integrationRunner { return fake }
	defer func() { newIntegrationRunner = old }()
	oldDir := dirFlag
	dirFlag = dir
	defer func() { dirFlag = oldDir }()
	var out bytes.Buffer
	if err := runIntegrationBuild(testCommand(&out), "kimus/piccolo-gws@main", integrationFlags{yes: true, tag: "piccolo/picoclaw-runtime:src"}); err != nil {
		t.Fatalf("build: %v", err)
	}
	if !strings.Contains(out.String(), "source builds can download") {
		t.Fatalf("out=%s", out.String())
	}
	m, _ := manifest.Load(filepath.Join(dir, "piccolo.yaml"))
	if m.Runtime.Integrations[0].Source != "build" || m.Runtime.Integrations[0].Ref != "github:kimus/piccolo-gws?ref=main" {
		t.Fatalf("integration=%#v", m.Runtime.Integrations[0])
	}
}

func writeIntegrationManifest(t *testing.T, dir string, integrations []manifest.Integration) {
	t.Helper()
	m := manifest.Manifest{Version: 1, Container: manifest.Container{Image: "piccolo/picoclaw-runtime:old", Name: "piccolo-gateway", Network: "bridge"}, Runtime: manifest.Runtime{Integrations: integrations}}
	if err := m.Write(filepath.Join(dir, "piccolo.yaml")); err != nil {
		t.Fatal(err)
	}
}

func mustPackageRef(t *testing.T, s string) picintegration.PackageRef {
	t.Helper()
	r, err := picintegration.ParsePackageRef(s)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func makeBundleDir(t *testing.T, commands []string) string {
	t.Helper()
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, c := range commands {
		if err := os.WriteFile(filepath.Join(bin, c), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestGithubReleaseFetcherAcceptHeaders(t *testing.T) {
	var seen []string
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		seen = append(seen, req.Header.Get("Accept"))
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("{}")), Header: make(http.Header)}, nil
	})}
	g := githubReleaseFetcher{client: client}
	_, _ = g.githubJSON(context.Background(), "https://api.github.test/json")
	_, _ = g.httpGet(context.Background(), "https://api.github.test/bin")
	if len(seen) != 2 || seen[0] != "application/vnd.github+json" || seen[1] != "application/octet-stream" {
		t.Fatalf("Accept headers = %#v", seen)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func makeBundleArchive(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, name := range []string{"bin/piccolo-gws", "bin/gws"} {
		b := []byte("#!/bin/sh\n")
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(b))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(b); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
