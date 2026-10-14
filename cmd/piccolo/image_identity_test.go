package main

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/kimus/piccolo/internal/docker"
	"github.com/kimus/piccolo/internal/manifest"
	"github.com/kimus/piccolo/internal/runtimeimage"
	"github.com/kimus/piccolo/internal/runtimeplan"
)

func TestReleaseBundlesFromManifestIsIdentityOnly(t *testing.T) {
	in := []manifest.Integration{
		{Package: "github:kimus/piccolo-gws", Source: "release", ResolvedVersion: "0.3.1", ArtifactDigest: "sha256:131f8cf4172fabcd", Commands: []string{"piccolo-gws", "gws"}},
		{Package: "github:other/tool", Source: "build", Ref: "github:other/tool@sha"},
		{Name: "legacy", Source: "release"},
	}
	got := runtimeplan.Bundles(in)
	if len(got) != 1 {
		t.Fatalf("bundles = %#v, want the one release integration", got)
	}
	if got[0].Root != "" {
		t.Errorf("Root must stay empty: a comparison plan may not imply an artifact download")
	}
	if got[0].Version != "0.3.1" || got[0].Digest != "sha256:131f8cf4172fabcd" {
		t.Errorf("identity not taken from recorded metadata: %#v", got[0])
	}
}

func TestDesiredRuntimePlanNeedsNoArtifactDownload(t *testing.T) {
	m := &manifest.Manifest{}
	m.Container.Image = "piccolo/picoclaw-runtime:whatever"
	m.Runtime.Integrations = []manifest.Integration{{
		Package: "github:kimus/piccolo-gws", Source: "release",
		ResolvedVersion: "0.3.1", ArtifactDigest: "sha256:131f8cf4172fabcd",
		Commands: []string{"piccolo-gws"},
	}}
	plan, err := desiredRuntimePlan(m)
	if err != nil {
		t.Fatalf("desiredRuntimePlan: %v", err)
	}
	if len(plan.Identity) != 1 {
		t.Fatalf("identity = %#v", plan.Identity)
	}
	// The plan is comparable but not buildable: that distinction is what keeps
	// the drift check cheap.
	if _, err := plan.ContextTar(); err == nil {
		t.Error("a metadata-only plan must not be tarred")
	}
}

func TestDriftReasonsNamesWhatMoved(t *testing.T) {
	plan, err := runtimeimage.BuildPlan(runtimeimage.Options{
		BaseImage: "docker.io/sipeed/picoclaw:v0.3.1",
		Packages:  []string{"file"},
		Commands:  []string{"file"},
		Bundles: []runtimeimage.IntegrationBundle{{
			Package: "github:kimus/piccolo-gws", Version: "0.3.1", Digest: "sha256:cccc",
			Commands: []string{"piccolo-gws"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	current := plan.Labels()
	delete(current, runtimeimage.LabelIntegrationPrefix+"github_kimus/piccolo-gws")
	current[runtimeimage.LabelToolchain] = "sha256:000000000000"
	current[runtimeimage.LabelBaseImage] = "docker.io/sipeed/picoclaw:launcher"
	current[runtimeimage.LabelIntegrationPrefix+"github_old/left"] = "0.1.0+aaaa"

	reasons := runtimeplan.Reasons(plan, current)
	joined := strings.Join(reasons, "\n")
	for _, want := range []string{
		"base image:",
		"toolchain package list changed",
		"piccolo-gws",
		"is not in the running image",
		"still contains integration",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("reasons missing %q; got:\n%s", want, joined)
		}
	}

	// Same content → nothing to report.
	if got := runtimeplan.Reasons(plan, plan.Labels()); len(got) != 0 {
		t.Errorf("identical content reported reasons: %v", got)
	}
}

func TestProbeExecutedClassifiesExecutability(t *testing.T) {
	cases := []struct {
		name string
		out  string
		code int
		want bool
	}{
		{"healthy state", `{"auth":"authorized"}`, 0, true},
		// Unconfigured is a setup gap, not a broken image: the binary ran.
		{"runs and reports no credentials", `{"auth":"none","reason":"no credentials"}`, 1, true},
		{"binary missing", "", 127, false},
		{"not executable", "", 126, false},
		{"loader missing", "Error loading shared library ld-linux-x86-64.so.2: No such file or directory", 127, false},
		{"silent failure", "", 2, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := probeExecuted(tc.out, tc.code); got != tc.want {
				t.Errorf("probeExecuted(%q, %d) = %v, want %v", tc.out, tc.code, got, tc.want)
			}
		})
	}
}

func TestIntegrationProbeArgvPrefersStatusCommand(t *testing.T) {
	item := manifest.Integration{
		StatusCommand:     []string{"piccolo-gws", "state", "--config-dir", "/root/.piccolo/integrations/kimus/piccolo-gws"},
		DescriptorCommand: []string{"piccolo-gws", "describe", "--json"},
		Commands:          []string{"piccolo-gws"},
	}
	got := strings.Join(integrationProbeArgv(item), " ")
	if !strings.HasPrefix(got, "piccolo-gws state ") {
		t.Errorf("probe argv = %q, want the recorded status command", got)
	}
	fallback := integrationProbeArgv(manifest.Integration{Commands: []string{"piccolo-gws"}})
	if strings.Join(fallback, " ") != "piccolo-gws describe --json" {
		t.Errorf("fallback probe = %v", fallback)
	}
	if len(integrationProbeArgv(manifest.Integration{Name: "empty"})) != 0 {
		t.Error("an integration with no recorded command must be skipped, not guessed")
	}
}

// The readiness gate decides whether the gateway can RUN its integrations, so a
// workspace with none installed must pass it trivially — and one that cannot
// execute a binary must not be adopted.
func TestVerifyIntegrationsExecutableUsesTheProbeSeam(t *testing.T) {
	orig := execInGateway
	defer func() { execInGateway = orig }()

	sc := &serveContext{spec: docker.Spec{Name: "piccolo-gateway"}, stdout: io.Discard}
	sc.manifest = &manifest.Manifest{}
	sc.manifest.Runtime.Integrations = []manifest.Integration{
		{Name: "gws", Package: "github:kimus/piccolo-gws", DisplayName: "Google Workspace",
			StatusCommand: []string{"piccolo-gws", "state"}},
		{Name: "broken", Package: "github:acme/broken", DisplayName: "Broken",
			StatusCommand: []string{"broken-tool", "state"}},
		{Name: "quiet", Package: "github:acme/quiet", DisplayName: "Quiet"},
	}

	var seen [][]string
	execInGateway = func(_ context.Context, _ *docker.Runner, _ string, argv []string) (string, int, error) {
		seen = append(seen, argv)
		if argv[0] == "piccolo-gws" {
			return `{"auth":"authorized"}`, 0, nil
		}
		return "", 127, nil
	}
	failures := verifyIntegrationsExecutable(context.Background(), sc)
	if len(failures) != 1 || !strings.Contains(failures[0], "exit 127") || !strings.Contains(failures[0], "Broken") {
		t.Fatalf("failures = %v", failures)
	}
	// The integration with no recorded command is skipped, not guessed at.
	if len(seen) != 2 {
		t.Errorf("expected two probes, saw %d: %v", len(seen), seen)
	}

	// Exec error (missing binary, hidden interpreter) is a failure too.
	execInGateway = func(context.Context, *docker.Runner, string, []string) (string, int, error) {
		return "", -1, errors.New("exec: piccolo-gws: executable file not found in $PATH")
	}
	sc.manifest.Runtime.Integrations = sc.manifest.Runtime.Integrations[:1]
	failures = verifyIntegrationsExecutable(context.Background(), sc)
	if len(failures) != 1 || !strings.Contains(failures[0], "executable file not found") {
		t.Fatalf("failures = %v", failures)
	}

	// No integrations: nothing to probe.
	sc.manifest.Runtime.Integrations = nil
	if got := verifyIntegrationsExecutable(context.Background(), sc); len(got) != 0 {
		t.Errorf("workspace without integrations reported %v", got)
	}
}
