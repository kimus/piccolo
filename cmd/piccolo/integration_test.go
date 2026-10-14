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
	removePrefix []string
	removed      [][]string
	execs        []execCall
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

func (f *fakeIntegrationRunner) RemoveByPrefix(_ context.Context, prefix string) ([]string, error) {
	var out []string
	for _, name := range f.removePrefix {
		if strings.HasPrefix(name, prefix) {
			out = append(out, name)
		}
	}
	f.removed = append(f.removed, out)
	return out, nil
}

// execCalls records relay attempts so the callback courier is observable in
// tests without a daemon.
func (f *fakeIntegrationRunner) Exec(_ context.Context, name string, cmd []string, _ bool) (*docker.ExecSession, error) {
	f.execs = append(f.execs, execCall{name: name, cmd: append([]string(nil), cmd...)})
	return nil, fmt.Errorf("fake exec has no live container")
}

type execCall struct {
	name string
	cmd  []string
}

type nopWriteCloser struct{ *bytes.Buffer }

func (n nopWriteCloser) Close() error { return nil }

// fakeReleaseFetcher records how often the cheap resolve and the expensive
// download run, so tests can prove a no-op install never touches the artifact.
type fakeReleaseFetcher struct {
	release   fetchedRelease
	version   string
	digest    string
	err       error
	resolves  *int
	downloads *int
}

func (f fakeReleaseFetcher) Resolve(_ context.Context, ref picintegration.PackageRef) (releaseResolution, error) {
	if f.resolves != nil {
		*f.resolves++
	}
	if f.err != nil {
		return releaseResolution{}, f.err
	}
	version, digest := f.version, f.digest
	if version == "" {
		version = f.release.Version
	}
	if digest == "" {
		digest = f.release.Digest
	}
	return releaseResolution{
		Ref:     ref,
		Tag:     version,
		Version: version,
		Digest:  digest,
		Asset:   "piccolo-gws_" + version + "_linux_arm64.tar.gz",
	}, nil
}

func (f fakeReleaseFetcher) Download(_ context.Context, res releaseResolution) (fetchedRelease, error) {
	if f.downloads != nil {
		*f.downloads++
	}
	if f.err != nil {
		return fetchedRelease{}, f.err
	}
	rel := f.release
	rel.Version = res.Version
	rel.Digest = res.Digest
	if rel.Bundle.Package == "" {
		rel.Bundle = runtimeimage.IntegrationBundle{Package: res.Ref.Package(), Version: res.Version}
	} else {
		rel.Bundle.Version = res.Version
	}
	return rel, nil
}

func TestIntegrationAddReleaseBuildsDescribesAndUpdatesManifest(t *testing.T) {
	dir := t.TempDir()
	writeTestManifest(t, dir, "docker.io/sipeed/picoclaw:v0.3.1")
	bundleRoot := makeBundleDir(t, []string{"piccolo-gws", "gws"})
	var resolves, downloads int
	rel := fetchedRelease{Ref: mustPackageRef(t, "kimus/piccolo-gws"), Version: "3.1.0", Digest: "sha256:abc", Manifest: picintegration.ReleaseManifest{Schema: picintegration.ReleaseManifestSchemaV1, Package: "github:kimus/piccolo-gws", Version: "3.1.0"}, Bundle: runtimeimage.IntegrationBundle{Package: "github:kimus/piccolo-gws", Version: "3.1.0", Root: bundleRoot, Commands: []string{"piccolo-gws", "gws"}}}
	desc := `{"schema":"piccolo.integration.v1","name":"gws","displayName":"Google Workspace","commands":["piccolo-gws"],"setup":{"command":["piccolo-gws","setup","--config-dir","/root/.piccolo/integrations/gws"],"requiresTty":true,"ports":["127.0.0.1:8000:8000"]},"commands":{"status":{"command":"piccolo-gws","args":["status","--config-dir","/root/.piccolo/integrations/gws","--json"]},"setup":{"command":"piccolo-gws","args":["setup","--config-dir","/root/.piccolo/integrations/gws"]}}}`
	fake := &fakeIntegrationRunner{stdout: desc}
	oldRunner, oldFetcher := newIntegrationRunner, newReleaseFetcher
	newIntegrationRunner = func() integrationRunner { return fake }
	newReleaseFetcher = func() releaseFetcher {
		return fakeReleaseFetcher{release: rel, resolves: &resolves, downloads: &downloads}
	}
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
	if !strings.Contains(fake.buildContext, "release-integrations/github_kimus/piccolo-gws/bin/piccolo-gws") {
		t.Fatalf("build context missing package-named bundle: %q", fake.buildContext)
	}
	if strings.Contains(fake.buildContext, "bundle-0") {
		t.Fatal("bundle context paths must be named by package, not position")
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

func TestIntegrationAddReleaseReplacesLegacySourceBuild(t *testing.T) {
	dir := t.TempDir()
	writeIntegrationManifest(t, dir, []manifest.Integration{{Name: "gws", Ref: "github:kimus/piccolo-gws", Commands: []string{"piccolo-gws"}}})
	bundleRoot := makeBundleDir(t, []string{"piccolo-gws", "gws"})
	var resolves, downloads int
	rel := fetchedRelease{Ref: mustPackageRef(t, "kimus/piccolo-gws"), Version: "0.1.0", Digest: "sha256:abc", Manifest: picintegration.ReleaseManifest{Schema: picintegration.ReleaseManifestSchemaV1, Package: "github:kimus/piccolo-gws", Version: "0.1.0"}, Bundle: runtimeimage.IntegrationBundle{Package: "github:kimus/piccolo-gws", Version: "0.1.0", Root: bundleRoot, Commands: []string{"piccolo-gws", "gws"}}}
	desc := `{"schema":"piccolo.integration.v1","name":"gws","displayName":"Google Workspace","commands":["piccolo-gws"],"setup":{"command":["piccolo-gws","setup"]}}`
	fake := &fakeIntegrationRunner{stdout: desc}
	oldRunner, oldFetcher := newIntegrationRunner, newReleaseFetcher
	newIntegrationRunner = func() integrationRunner { return fake }
	newReleaseFetcher = func() releaseFetcher {
		return fakeReleaseFetcher{release: rel, resolves: &resolves, downloads: &downloads}
	}
	defer func() { newIntegrationRunner = oldRunner; newReleaseFetcher = oldFetcher }()
	oldDir := dirFlag
	dirFlag = dir
	defer func() { dirFlag = oldDir }()
	if err := runIntegrationAdd(testCommand(&bytes.Buffer{}), "kimus/piccolo-gws", integrationFlags{yes: true, tag: "piccolo/picoclaw-runtime:gws"}); err != nil {
		t.Fatalf("add: %v", err)
	}
	// The toolchain layer runs its own Nix GC, so look for the source-build layer
	// itself: the Devbox integration project it installs into.
	// The devbox file is always in the context; the source-build LAYER is the
	// thing a release add must not emit.
	if strings.Contains(fake.buildContext, "/opt/piccolo/integrations") {
		t.Fatalf("release add preserved legacy source build context")
	}
	m, err := manifest.Load(filepath.Join(dir, "piccolo.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Runtime.Integrations) != 1 || m.Runtime.Integrations[0].Ref != "" || m.Runtime.Integrations[0].Package != "github:kimus/piccolo-gws" {
		t.Fatalf("integrations = %#v", m.Runtime.Integrations)
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

func TestIntegrationSetupReclaimsAbandonedContainers(t *testing.T) {
	dir := t.TempDir()
	writeIntegrationManifest(t, dir, []manifest.Integration{{Package: "github:kimus/piccolo-gws", Source: "release", Name: "gws", DisplayName: "Google Workspace", SetupCommand: []string{"piccolo-gws", "setup"}, SetupRequiresTTY: true}})
	fake := &fakeIntegrationRunner{
		// A legacy timestamped orphan plus the current deterministic name.
		removePrefix: []string{"piccolo-setup-gws-1791932406936295000", "piccolo-setup-gws", "piccolo-gateway"},
	}
	old := newIntegrationRunner
	newIntegrationRunner = func() integrationRunner { return fake }
	defer func() { newIntegrationRunner = old }()
	oldDir := dirFlag
	dirFlag = dir
	defer func() { dirFlag = oldDir }()

	var out bytes.Buffer
	if err := runIntegrationSetup(testCommand(&out), "kimus/piccolo-gws"); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if len(fake.runs) != 1 {
		t.Fatalf("runs = %d", len(fake.runs))
	}
	if fake.runs[0].Name != "piccolo-setup-gws" {
		t.Fatalf("container name = %q, want deterministic name", fake.runs[0].Name)
	}
	if !strings.Contains(out.String(), "abandoned setup container") || !strings.Contains(out.String(), "piccolo-setup-gws-1791932406936295000") {
		t.Fatalf("orphan notice missing from output: %q", out.String())
	}
	if len(fake.removed) == 0 || len(fake.removed[0]) != 2 {
		t.Fatalf("removed = %#v, want the two piccolo-setup containers only", fake.removed)
	}
	for _, name := range fake.removed[0] {
		if name == "piccolo-gateway" {
			t.Fatal("must never reclaim the gateway container")
		}
		if !strings.HasPrefix(name, docker.SetupContainerPrefix) {
			t.Fatalf("unexpected reclaim target %q", name)
		}
	}
}

func TestSetupAndStatusContainerNamesAreDeterministic(t *testing.T) {
	if got := setupContainerName("gws"); got != "piccolo-setup-gws" {
		t.Fatalf("setupContainerName = %q", got)
	}
	if got := statusContainerName("gws"); got != "piccolo-setup-status-gws" {
		t.Fatalf("statusContainerName = %q", got)
	}
	// Status must not reclaim a setup run and vice versa.
	if strings.HasPrefix(statusContainerName("gws"), setupContainerName("gws")+"-") ||
		strings.HasPrefix(setupContainerName("gws"), statusContainerName("gws")) {
		t.Fatal("setup and status prefixes must not overlap")
	}
	if got := sanitizeForName("a b/c"); got != "a-b-c" {
		t.Fatalf("sanitizeForName = %q", got)
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

func TestIntegrationStatusRendersReportOnNonZeroExit(t *testing.T) {
	dir := t.TempDir()
	writeIntegrationManifest(t, dir, []manifest.Integration{{Package: "github:kimus/piccolo-gws", Source: "release", Name: "gws", DisplayName: "Google Workspace", StatusCommand: []string{"piccolo-gws", "status", "--json"}}})
	payload := `{"schema":"piccolo.integration.status.v1","name":"gws","status":"fail","summary":"checks failed","checks":[{"id":"gmail","label":"Gmail","status":"fail","message":"not authenticated","remediation":"run setup"}]}`
	fake := &fakeIntegrationRunner{stdout: payload, code: 1}
	old := newIntegrationRunner
	newIntegrationRunner = func() integrationRunner { return fake }
	defer func() { newIntegrationRunner = old }()
	oldDir := dirFlag
	dirFlag = dir
	defer func() { dirFlag = oldDir }()

	var out bytes.Buffer
	err := runIntegrationStatus(testCommand(&out), "kimus/piccolo-gws", integrationStatusFlags{})
	if err == nil || !strings.Contains(err.Error(), "checks failed") {
		t.Fatalf("expected failed-checks error, got %v", err)
	}
	body := out.String()
	if !strings.Contains(body, "Gmail") || !strings.Contains(body, "not authenticated") {
		t.Fatalf("report not rendered: %q", body)
	}
	if strings.Contains(body, "command exited") {
		t.Fatalf("opaque exit error leaked into output: %q", body)
	}
}

func TestIntegrationStatusJSONOnNonZeroExit(t *testing.T) {
	dir := t.TempDir()
	writeIntegrationManifest(t, dir, []manifest.Integration{{Package: "github:kimus/piccolo-gws", Source: "release", Name: "gws", StatusCommand: []string{"piccolo-gws", "status", "--json"}}})
	payload := `{"schema":"piccolo.integration.status.v1","name":"gws","status":"fail","summary":"checks failed","checks":[{"id":"drive","label":"Drive","status":"fail","message":"api disabled"}]}`
	fake := &fakeIntegrationRunner{stdout: payload, code: 1, stderr: "noise"}
	old := newIntegrationRunner
	newIntegrationRunner = func() integrationRunner { return fake }
	defer func() { newIntegrationRunner = old }()
	oldDir := dirFlag
	dirFlag = dir
	defer func() { dirFlag = oldDir }()

	var out bytes.Buffer
	err := runIntegrationStatus(testCommand(&out), "kimus/piccolo-gws", integrationStatusFlags{json: true})
	if err == nil {
		t.Fatal("expected non-zero exit for failing checks")
	}
	if !strings.Contains(out.String(), "piccolo.integration.status.v1") || !strings.Contains(out.String(), "api disabled") {
		t.Fatalf("json payload not emitted: %q", out.String())
	}
}

func TestIntegrationStatusExitWithoutReportIsInvocationFailure(t *testing.T) {
	dir := t.TempDir()
	writeIntegrationManifest(t, dir, []manifest.Integration{{Package: "github:kimus/piccolo-gws", Source: "release", Name: "gws", StatusCommand: []string{"piccolo-gws", "status", "--json"}}})
	fake := &fakeIntegrationRunner{stdout: "boom: unrecognized subcommand", stderr: "parse error", code: 2}
	old := newIntegrationRunner
	newIntegrationRunner = func() integrationRunner { return fake }
	defer func() { newIntegrationRunner = old }()
	oldDir := dirFlag
	dirFlag = dir
	defer func() { dirFlag = oldDir }()

	var out bytes.Buffer
	err := runIntegrationStatus(testCommand(&out), "kimus/piccolo-gws", integrationStatusFlags{})
	if err == nil || !strings.Contains(err.Error(), "exited 2") {
		t.Fatalf("expected invocation failure, got %v", err)
	}
}

func TestIntegrationStatusHealthyExitRendersNormally(t *testing.T) {
	dir := t.TempDir()
	writeIntegrationManifest(t, dir, []manifest.Integration{{Package: "github:kimus/piccolo-gws", Source: "release", Name: "gws", StatusCommand: []string{"piccolo-gws", "status", "--json"}}})
	payload := `{"schema":"piccolo.integration.status.v1","name":"gws","status":"ok","summary":"all good","checks":[{"id":"gmail","label":"Gmail","status":"ok","message":"connected"}]}`
	fake := &fakeIntegrationRunner{stdout: payload, code: 0}
	old := newIntegrationRunner
	newIntegrationRunner = func() integrationRunner { return fake }
	defer func() { newIntegrationRunner = old }()
	oldDir := dirFlag
	dirFlag = dir
	defer func() { dirFlag = oldDir }()

	var out bytes.Buffer
	if err := runIntegrationStatus(testCommand(&out), "kimus/piccolo-gws", integrationStatusFlags{}); err != nil {
		t.Fatalf("healthy status: %v", err)
	}
	if !strings.Contains(out.String(), "all good") {
		t.Fatalf("output = %q", out.String())
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

func TestExtractIntegrationArchiveAllowsDotDirectory(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "./", Typeflag: tar.TypeDir, Mode: 0o755}); err != nil {
		t.Fatal(err)
	}
	b := []byte("#!/bin/sh\n")
	if err := tw.WriteHeader(&tar.Header{Name: "./bin/piccolo-gws", Typeflag: tar.TypeReg, Mode: 0o755, Size: int64(len(b))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(b); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	root, commands, err := extractIntegrationArchive(buf.Bytes())
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	defer os.RemoveAll(root)
	if len(commands) != 1 || commands[0] != "piccolo-gws" {
		t.Fatalf("commands = %#v", commands)
	}
}

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

// --- no-op install/upgrade: never download, never build, never prompt ---

func TestIntegrationAddSameVersionIsNoOp(t *testing.T) {
	dir := t.TempDir()
	writeIntegrationManifest(t, dir, []manifest.Integration{{
		Package: "github:kimus/piccolo-gws", Requested: "kimus/piccolo-gws", Source: "release", Channel: "latest",
		ResolvedVersion: "3.1.0", ArtifactDigest: "sha256:abc", StatePath: "integrations/kimus/piccolo-gws",
		Name: "gws", DisplayName: "Google Workspace",
		DescriptorCommand: []string{"piccolo-gws", "describe", "--json"},
		SetupCommand:      []string{"piccolo-gws", "setup"},
	}})
	var resolves, downloads int
	rel := fetchedRelease{Bundle: runtimeimage.IntegrationBundle{Package: "github:kimus/piccolo-gws", Root: "/tmp/unused"}}
	fake := &fakeIntegrationRunner{}
	oldRunner, oldFetcher := newIntegrationRunner, newReleaseFetcher
	newIntegrationRunner = func() integrationRunner { return fake }
	newReleaseFetcher = func() releaseFetcher {
		return fakeReleaseFetcher{release: rel, version: "3.1.0", digest: "sha256:abc", resolves: &resolves, downloads: &downloads}
	}
	defer func() { newIntegrationRunner, newReleaseFetcher = oldRunner, oldFetcher }()
	oldDir := dirFlag
	dirFlag = dir
	defer func() { dirFlag = oldDir }()

	var out bytes.Buffer
	if err := runIntegrationAdd(testCommand(&out), "kimus/piccolo-gws", integrationFlags{}); err != nil {
		t.Fatalf("no-op add must succeed: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "Already up to date") || !strings.Contains(got, "3.1.0") {
		t.Fatalf("expected up-to-date message, got %q", got)
	}
	if downloads != 0 {
		t.Fatalf("no-op must not download the artifact (downloads=%d)", downloads)
	}
	if fake.builds != 0 {
		t.Fatalf("no-op must not build an image (builds=%d)", fake.builds)
	}
	if len(fake.runs) != 0 {
		t.Fatalf("no-op must not start containers: %#v", fake.runs)
	}
}

func TestIntegrationAddForceOverridesNoOp(t *testing.T) {
	dir := t.TempDir()
	writeIntegrationManifest(t, dir, []manifest.Integration{{
		Package: "github:kimus/piccolo-gws", Source: "release", Channel: "latest",
		ResolvedVersion: "3.1.0", ArtifactDigest: "sha256:abc", StatePath: "integrations/kimus/piccolo-gws",
		Name: "gws", DisplayName: "Google Workspace",
		DescriptorCommand: []string{"piccolo-gws", "describe", "--json"},
		SetupCommand:      []string{"piccolo-gws", "setup"},
	}})
	m, _ := manifest.Load(filepath.Join(dir, "piccolo.yaml"))
	m.Container.Image = "piccolo/picoclaw-runtime:abc"
	if err := m.Write(filepath.Join(dir, "piccolo.yaml")); err != nil {
		t.Fatal(err)
	}
	var resolves, downloads int
	bundleRoot := makeBundleDir(t, []string{"piccolo-gws"})
	rel := fetchedRelease{
		Ref: mustPackageRef(t, "kimus/piccolo-gws"), Version: "3.1.0", Digest: "sha256:abc",
		Manifest: picintegration.ReleaseManifest{Schema: picintegration.ReleaseManifestSchemaV1, Package: "github:kimus/piccolo-gws", Version: "3.1.0"},
		Bundle:   runtimeimage.IntegrationBundle{Package: "github:kimus/piccolo-gws", Version: "3.1.0", Root: bundleRoot, Commands: []string{"piccolo-gws"}},
	}
	desc := `{"schema":"piccolo.integration.v1","name":"gws","displayName":"Google Workspace","commands":["piccolo-gws"],"setup":{"command":["piccolo-gws","setup"]}}`
	fake := &fakeIntegrationRunner{stdout: desc}
	oldRunner, oldFetcher := newIntegrationRunner, newReleaseFetcher
	newIntegrationRunner = func() integrationRunner { return fake }
	newReleaseFetcher = func() releaseFetcher {
		return fakeReleaseFetcher{release: rel, version: "3.1.0", digest: "sha256:abc", resolves: &resolves, downloads: &downloads}
	}
	defer func() { newIntegrationRunner, newReleaseFetcher = oldRunner, oldFetcher }()
	oldDir := dirFlag
	dirFlag = dir
	defer func() { dirFlag = oldDir }()

	var out bytes.Buffer
	if err := runIntegrationAdd(testCommand(&out), "kimus/piccolo-gws", integrationFlags{yes: true, force: true, tag: "piccolo/picoclaw-runtime:forced"}); err != nil {
		t.Fatalf("--force must rebuild: %v", err)
	}
	if downloads == 0 || fake.builds == 0 {
		t.Fatalf("--force must download and build (downloads=%d builds=%d)", downloads, fake.builds)
	}
}

func TestIntegrationUpgradeSameVersionIsNoOp(t *testing.T) {
	dir := t.TempDir()
	writeIntegrationManifest(t, dir, []manifest.Integration{{
		Package: "github:kimus/piccolo-gws", Source: "release", Channel: "latest",
		ResolvedVersion: "3.1.0", ArtifactDigest: "sha256:abc", StatePath: "integrations/kimus/piccolo-gws",
		Name: "gws", DisplayName: "Google Workspace", SetupCommand: []string{"piccolo-gws", "setup"},
	}})
	var resolves, downloads int
	fake := &fakeIntegrationRunner{}
	oldRunner, oldFetcher := newIntegrationRunner, newReleaseFetcher
	newIntegrationRunner = func() integrationRunner { return fake }
	newReleaseFetcher = func() releaseFetcher {
		return fakeReleaseFetcher{
			release:   fetchedRelease{Bundle: runtimeimage.IntegrationBundle{Package: "github:kimus/piccolo-gws", Root: "/tmp/unused"}},
			version:   "3.1.0",
			digest:    "sha256:abc",
			resolves:  &resolves,
			downloads: &downloads,
		}
	}
	defer func() { newIntegrationRunner, newReleaseFetcher = oldRunner, oldFetcher }()
	oldDir := dirFlag
	dirFlag = dir
	defer func() { dirFlag = oldDir }()

	var out bytes.Buffer
	err := runIntegrationUpgrade(testCommand(&out), "kimus/piccolo-gws", integrationUpgradeFlags{integrationFlags: integrationFlags{yes: true}})
	if err != nil {
		t.Fatalf("no-op upgrade must succeed: %v", err)
	}
	if !strings.Contains(out.String(), "Already up to date") {
		t.Fatalf("output = %q", out.String())
	}
	if resolves == 0 {
		t.Fatal("upgrade should resolve cheaply before deciding")
	}
	if downloads != 0 || fake.builds != 0 {
		t.Fatalf("no-op upgrade must not download or build (downloads=%d builds=%d)", downloads, fake.builds)
	}
}

func TestShortDigest(t *testing.T) {
	cases := map[string]string{
		"":                                  "unknown",
		"sha256:abcdefghijklmnopqrstuvwxyz": "abcdefghijkl",
		"sha256:abc":                        "abc",
	}
	for in, want := range cases {
		if got := shortDigest(in); got != want {
			t.Fatalf("shortDigest(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPrintIntegrationPlanShowsVersions(t *testing.T) {
	dir := t.TempDir()
	writeIntegrationManifest(t, dir, []manifest.Integration{{
		Package: "github:kimus/piccolo-gws", Source: "release", Channel: "latest",
		ResolvedVersion: "0.2.0", ArtifactDigest: "sha256:oldoldoldold", StatePath: "integrations/kimus/piccolo-gws",
		Name: "gws", DisplayName: "Google Workspace", SetupCommand: []string{"piccolo-gws", "setup"},
	}})
	m, err := manifest.Load(filepath.Join(dir, "piccolo.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	printIntegrationPlan(&out, m, mustPackageRef(t, "kimus/piccolo-gws"), releaseResolution{
		Version: "0.3.0", Digest: "sha256:newnewnewnew", Asset: "piccolo-gws_0.3.0_linux_arm64.tar.gz",
	}, installedOf(m, "github:kimus/piccolo-gws"))
	got := out.String()
	for _, want := range []string{"0.2.0 -> 0.3.0", "piccolo-gws_0.3.0_linux_arm64.tar.gz", "base image:", "toolchain:", "preserved"} {
		if !strings.Contains(got, want) {
			t.Fatalf("plan must show %q, got:\n%s", want, got)
		}
	}
}

// The record must describe what the image actually verifies. Recording the
// descriptor's command list instead of the artifact's leaves the drift check
// comparing a plan the build can never reproduce, and no rebuild clears it.
func TestInstalledCommandsPrefersWhatTheImageVerifies(t *testing.T) {
	got := installedCommands([]string{"gws", "piccolo-gws"}, picintegration.CommandList{"piccolo-gws"})
	if strings.Join(got, ",") != "gws,piccolo-gws" {
		t.Errorf("installed commands = %v", got)
	}
	fallback := installedCommands(nil, picintegration.CommandList{"piccolo-gws"})
	if strings.Join(fallback, ",") != "piccolo-gws" {
		t.Errorf("fallback = %v", fallback)
	}
}

func TestRefreshInstalledIdentityAlignsTheRecord(t *testing.T) {
	m := &manifest.Manifest{}
	m.Runtime.Integrations = []manifest.Integration{
		{Package: "github:kimus/piccolo-gws", Source: "release", ResolvedVersion: "0.3.0", ArtifactDigest: "sha256:old", Commands: []string{"piccolo-gws"}},
		{Package: "github:other/x", Source: "build", Ref: "github:other/x@sha"},
	}
	bundles := []runtimeimage.IntegrationBundle{{
		Package: "github:kimus/piccolo-gws", Version: "0.3.1",
		Digest: "sha256:new", Commands: []string{"gws", "piccolo-gws"},
	}}
	refreshInstalledIdentity(m, bundles)

	item := m.Runtime.Integrations[0]
	if item.ResolvedVersion != "0.3.1" || item.ArtifactDigest != "sha256:new" || strings.Join(item.Commands, ",") != "gws,piccolo-gws" {
		t.Errorf("record not aligned with the built image: %#v", item)
	}
	if m.Runtime.Integrations[1].ResolvedVersion != "" {
		t.Error("a source-built entry must not be touched by release refresh")
	}

	// Drift must be gone once the record matches what was built.
	plan, err := desiredRuntimePlan(m)
	if err != nil {
		t.Fatal(err)
	}
	m.Container.Image = plan.Tag
	again, err := desiredRuntimePlan(m)
	if err != nil {
		t.Fatal(err)
	}
	if again.Tag != plan.Tag {
		t.Errorf("recomputing the plan changed the tag: %s vs %s", again.Tag, plan.Tag)
	}
}

// A bundle packaged on macOS carries an AppleDouble sidecar for every file with
// extended attributes. Those are not integration binaries: counting them as
// commands makes the image install resource forks onto PATH and then verify
// them, so the build fails on -- or ships -- junk like ._piccolo-math.
func TestExtractIntegrationArchiveIgnoresPackagingJunk(t *testing.T) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	write := func(name, body string, mode int64) {
		_ = tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: mode, Size: int64(len(body))})
		_, _ = tw.Write([]byte(body))
	}
	write("./bin/piccolo-math", "#!/bin/sh\n", 0o755)
	write("./bin/._piccolo-math", "macos resource fork", 0o644)
	write("./bin/.DS_Store", "finder metadata", 0o644)
	_ = tw.Close()
	_ = zw.Close()

	root, commands, err := extractIntegrationArchive(buf.Bytes())
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	defer os.RemoveAll(root)
	if strings.Join(commands, ",") != "piccolo-math" {
		t.Errorf("commands = %v, want only the real binary", commands)
	}
}

// A remediation link that is parsed and never printed is worse than no field: the
// integration did its job and Piccolo dropped the actionable half.
func TestRenderStatusesPrintsRemediationLink(t *testing.T) {
	var out bytes.Buffer
	renderIntegrationStatuses(&out, []picintegration.StatusResult{{
		Name: "gws", Status: "fail", Summary: "1 of 3 services unreachable",
		Checks: []picintegration.StatusCheck{{
			ID: "drive", Label: "Drive", Status: "fail",
			Message:          "Google Drive API has not been used in project 192902532576",
			Remediation:      "Enable the service in your Google Cloud project.",
			RemediationLabel: "Open the API in Google Cloud Console",
			RemediationURL:   "https://console.developers.google.com/apis/api/drive.googleapis.com/overview?project=192902532576",
		}},
	}})
	got := out.String()
	if !strings.Contains(got, "Open the API in Google Cloud Console: https://console.developers.google.com/apis/api/drive.googleapis.com") {
		t.Errorf("remediation link dropped:\n%s", got)
	}
	// The prose must still be there: colour and links are emphasis, not meaning.
	if !strings.Contains(got, "Enable the service in your Google Cloud project.") {
		t.Errorf("remediation text lost:\n%s", got)
	}
}
