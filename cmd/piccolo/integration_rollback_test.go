package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/kimus/piccolo/internal/manifest"
	"github.com/kimus/piccolo/internal/runtimeimage"
)

func TestResolveRollbackTarget(t *testing.T) {
	m := &manifest.Manifest{}
	m.Container.Image = "piccolo/picoclaw-runtime:new"
	m.Container.PreviousImage = "piccolo/picoclaw-runtime:old"

	current, target, err := resolveRollbackTarget(m, "")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if current != m.Container.Image || target != m.Container.PreviousImage {
		t.Errorf("current=%q target=%q", current, target)
	}

	// Explicit override wins over the recorded slot.
	if _, target, err = resolveRollbackTarget(m, "piccolo/picoclaw-runtime:other"); err != nil || target != "piccolo/picoclaw-runtime:other" {
		t.Errorf("--to override: %q %v", target, err)
	}

	// Nothing recorded: refuse without touching anything.
	fresh := &manifest.Manifest{}
	fresh.Container.Image = "piccolo/picoclaw-runtime:new"
	if _, _, err := resolveRollbackTarget(fresh, ""); err == nil || !strings.Contains(err.Error(), "nothing to roll back to") {
		t.Errorf("expected a refusal, got %v", err)
	}

	// Asking to roll back onto the image already in use is not a no-op success:
	// it would park and restart the gateway for nothing.
	same := &manifest.Manifest{}
	same.Container.Image = "piccolo/picoclaw-runtime:x"
	same.Container.PreviousImage = "piccolo/picoclaw-runtime:x"
	if _, _, err := resolveRollbackTarget(same, ""); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Errorf("expected an already-running refusal, got %v", err)
	}
}

func TestDescribeImageContentsListsIntegrationVersions(t *testing.T) {
	labels := map[string]string{
		runtimeimage.LabelBaseImage:                           "docker.io/sipeed/picoclaw:v0.3.1",
		runtimeimage.LabelToolchain:                           "sha256:6d1e1506b847",
		runtimeimage.IntegrationLabelKey("kimus/piccolo-gws"): "0.3.1+131f8cf4172f",
		runtimeimage.IntegrationLabelKey("acme/other"):        "1.2.3+aaaa",
	}
	var out bytes.Buffer
	describeImageContents(&out, labels)
	got := out.String()
	for _, want := range []string{"base:      docker.io/sipeed/picoclaw:v0.3.1", "toolchain: sha256:6d1e1506b847", "kimus/piccolo-gws 0.3.1+131f8cf4172f", "acme/other 1.2.3+aaaa"} {
		if !strings.Contains(got, want) {
			t.Errorf("description missing %q:\n%s", want, got)
		}
	}
	// Stable order, so the same image always describes itself the same way.
	var again bytes.Buffer
	describeImageContents(&again, labels)
	if again.String() != got {
		t.Error("description is not deterministic")
	}

	var empty bytes.Buffer
	describeImageContents(&empty, map[string]string{})
	if !strings.Contains(empty.String(), "none recorded") {
		t.Errorf("an image with no integrations should say so: %q", empty.String())
	}
}
