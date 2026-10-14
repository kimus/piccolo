package main

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kimus/piccolo/internal/docker"
	"github.com/kimus/piccolo/internal/manifest"
	"github.com/kimus/piccolo/internal/workspace"
)

// fakeRetentionRunner records removals so the floor can be asserted rather than
// trusted: the test fails if the image in use, the rollback target, or anything
// a container references is even offered to RemoveImage.
type fakeRetentionRunner struct {
	images     []docker.ImageRecord
	containers map[string][]string
	removed    []string
	failOn     string
	pingErr    error
}

func (f *fakeRetentionRunner) Ping(context.Context) error { return f.pingErr }

func (f *fakeRetentionRunner) ListImages(context.Context) ([]docker.ImageRecord, error) {
	return f.images, nil
}

func (f *fakeRetentionRunner) ContainerImages(context.Context) (map[string][]string, error) {
	return f.containers, nil
}

func (f *fakeRetentionRunner) RemoveImage(_ context.Context, ref string) error {
	if f.failOn == ref {
		return errors.New("image is being used by a container")
	}
	f.removed = append(f.removed, ref)
	return nil
}

func retentionFixture() *fakeRetentionRunner {
	big := int64(8) << 30
	return &fakeRetentionRunner{
		images: []docker.ImageRecord{
			{ID: "sha256:a", RepoTags: []string{"piccolo/picoclaw-runtime:cur"}, Size: big},
			{ID: "sha256:b", RepoTags: []string{"piccolo/picoclaw-runtime:prev"}, Size: big},
			{ID: "sha256:c", RepoTags: []string{"piccolo/picoclaw-runtime:held"}, Size: big},
			{ID: "sha256:d", RepoTags: []string{"piccolo/picoclaw-runtime:gone1"}, Size: big},
			{ID: "sha256:e", RepoTags: []string{"piccolo/picoclaw-runtime:gone2"}, Size: big},
			// A base image is never a candidate, tagged or dangling.
			{ID: "sha256:f", RepoTags: []string{"docker.io/sipeed/picoclaw:launcher"}, Size: big},
		},
		containers: map[string][]string{
			"piccolo/picoclaw-runtime:cur":  {"piccolo-gateway"},
			"piccolo/picoclaw-runtime:held": {"piccolo-gateway.prev"},
		},
	}
}

func retentionManifest() *manifest.Manifest {
	m := &manifest.Manifest{Version: 1}
	m.Container = manifest.Container{
		Image:         "piccolo/picoclaw-runtime:cur",
		Name:          "piccolo-gateway",
		Network:       "bridge",
		PreviousImage: "piccolo/picoclaw-runtime:prev",
	}
	return m
}

func TestCollectRetentionFloorsTheRemovalSet(t *testing.T) {
	plan, err := collectRetention(context.Background(), retentionFixture(), retentionManifest())
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range plan.Removable {
		got = append(got, r.Ref)
	}
	want := "piccolo/picoclaw-runtime:gone1 piccolo/picoclaw-runtime:gone2"
	if strings.Join(got, " ") != want {
		t.Errorf("removable = %v, want [%s]", got, want)
	}
	// Both held images must appear in the report with their reason: the floor is
	// only auditable if the output says why something survived.
	text := ""
	for _, k := range plan.Kept {
		text += k.Info.Ref + "=" + k.Reason + " "
	}
	for _, wantReason := range []string{"piccolo/picoclaw-runtime:cur=in use", "piccolo/picoclaw-runtime:prev=rollback target", "piccolo/picoclaw-runtime:held=referenced by container piccolo-gateway.prev"} {
		if !strings.Contains(text, wantReason) {
			t.Errorf("kept report missing %q; got %q", wantReason, text)
		}
	}
}

func TestImagePruneRemovesOnlyUnreferenced(t *testing.T) {
	dir := t.TempDir()
	writeRetentionManifest(t, dir, retentionManifest())
	fake := retentionFixture()
	oldRunner, oldDir := newRetentionRunner, dirFlag
	newRetentionRunner = func() retentionRunner { return fake }
	dirFlag = dir
	defer func() { newRetentionRunner, dirFlag = oldRunner, oldDir }()

	var out bytes.Buffer
	cmd := testCommand(&out)
	if err := runImagePrune(cmd, true); err != nil {
		t.Fatalf("prune: %v\n%s", err, out.String())
	}
	if strings.Join(fake.removed, " ") != "piccolo/picoclaw-runtime:gone1 piccolo/picoclaw-runtime:gone2" {
		t.Fatalf("removed = %v", fake.removed)
	}
	report := out.String()
	for _, must := range []string{"Would remove 2", "Kept (retention floor)", "rollback target", "in use"} {
		if !strings.Contains(report, must) {
			t.Errorf("prune report missing %q:\n%s", must, report)
		}
	}
}

// Without --yes and without a terminal, nothing may be removed. A destructive
// command that assumes consent from a pipe is the failure mode here.
func TestImagePruneNeedsConfirmationWithoutTTY(t *testing.T) {
	dir := t.TempDir()
	writeRetentionManifest(t, dir, retentionManifest())
	fake := retentionFixture()
	oldRunner, oldDir := newRetentionRunner, dirFlag
	newRetentionRunner = func() retentionRunner { return fake }
	dirFlag = dir
	defer func() { newRetentionRunner, dirFlag = oldRunner, oldDir }()

	var out bytes.Buffer
	cmd := testCommand(&out)
	cmd.SetIn(strings.NewReader("y\n"))
	if err := runImagePrune(cmd, false); err == nil {
		t.Fatal("expected a refusal without a terminal")
	}
	if len(fake.removed) != 0 {
		t.Fatalf("removed despite no confirmation: %v", fake.removed)
	}
}

// A single refusal must not abandon the remaining images, and must not be silent
// in the summary.
func TestImagePruneContinuesPastOneFailure(t *testing.T) {
	dir := t.TempDir()
	writeRetentionManifest(t, dir, retentionManifest())
	fake := retentionFixture()
	fake.failOn = "piccolo/picoclaw-runtime:gone1"
	oldRunner, oldDir := newRetentionRunner, dirFlag
	newRetentionRunner = func() retentionRunner { return fake }
	dirFlag = dir
	defer func() { newRetentionRunner, dirFlag = oldRunner, oldDir }()

	var out bytes.Buffer
	cmd := testCommand(&out)
	err := runImagePrune(cmd, true)
	if err == nil {
		t.Fatal("expected a non-zero result when an image remained")
	}
	if strings.Join(fake.removed, " ") != "piccolo/picoclaw-runtime:gone2" {
		t.Errorf("removed = %v", fake.removed)
	}
	if !strings.Contains(out.String(), "[WARN]") {
		t.Errorf("refusal not reported:\n%s", out.String())
	}
}

func writeRetentionManifest(t *testing.T, dir string, m *manifest.Manifest) {
	t.Helper()
	if err := m.Write(filepath.Join(dir, workspace.ManifestName)); err != nil {
		t.Fatal(err)
	}
}
