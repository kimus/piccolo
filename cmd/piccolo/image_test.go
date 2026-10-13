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

	"github.com/spf13/cobra"

	"github.com/kimus/piccolo/internal/docker"
	"github.com/kimus/piccolo/internal/manifest"
)

type fakeImageRunner struct {
	pingErr error
	builds  int
	tag     string
}

func (f *fakeImageRunner) Ping(context.Context) error { return f.pingErr }
func (f *fakeImageRunner) BuildImage(_ context.Context, _ io.Reader, opts docker.ImageBuildOptions, out io.Writer) error {
	f.builds++
	f.tag = opts.Tag
	_, _ = fmt.Fprintln(out, `{"stream":"ok"}`)
	return nil
}

func TestRunImageBuildSetManifest(t *testing.T) {
	dir := t.TempDir()
	writeTestManifest(t, dir, "docker.io/sipeed/picoclaw:v0.3.1")

	fake := &fakeImageRunner{}
	old := newImageRunner
	newImageRunner = func() imageRunner { return fake }
	defer func() { newImageRunner = old }()
	oldDir := dirFlag
	dirFlag = dir
	defer func() { dirFlag = oldDir }()

	var out bytes.Buffer
	cmd := testCommand(&out)
	if err := runImageBuild(cmd, imageBuildFlags{tag: "piccolo/picoclaw-runtime:test", setManifest: true, yes: true}); err != nil {
		t.Fatalf("runImageBuild: %v", err)
	}
	if fake.builds != 1 || fake.tag != "piccolo/picoclaw-runtime:test" {
		t.Fatalf("builds=%d tag=%q", fake.builds, fake.tag)
	}
	m, err := manifest.Load(filepath.Join(dir, "piccolo.yaml"))
	if err != nil {
		t.Fatalf("load manifest: %v", err)
	}
	if m.Container.Image != "piccolo/picoclaw-runtime:test" {
		t.Fatalf("manifest image = %q", m.Container.Image)
	}
	if !strings.Contains(out.String(), "Updated piccolo.yaml") {
		t.Fatalf("output missing update: %s", out.String())
	}
}

func TestRunImageBuildNoSetLeavesManifest(t *testing.T) {
	dir := t.TempDir()
	writeTestManifest(t, dir, "docker.io/sipeed/picoclaw:v0.3.1")

	fake := &fakeImageRunner{}
	old := newImageRunner
	newImageRunner = func() imageRunner { return fake }
	defer func() { newImageRunner = old }()
	oldDir := dirFlag
	dirFlag = dir
	defer func() { dirFlag = oldDir }()

	var out bytes.Buffer
	cmd := testCommand(&out)
	if err := runImageBuild(cmd, imageBuildFlags{tag: "piccolo/picoclaw-runtime:test", yes: true}); err != nil {
		t.Fatalf("runImageBuild: %v", err)
	}
	m, err := manifest.Load(filepath.Join(dir, "piccolo.yaml"))
	if err != nil {
		t.Fatalf("load manifest: %v", err)
	}
	if m.Container.Image != "docker.io/sipeed/picoclaw:v0.3.1" {
		t.Fatalf("manifest image changed to %q", m.Container.Image)
	}
	if !strings.Contains(out.String(), "piccolo.yaml unchanged") {
		t.Fatalf("output missing unchanged: %s", out.String())
	}
}

func TestRunImageBuildDockerUnavailableDoesNotModifyManifest(t *testing.T) {
	dir := t.TempDir()
	writeTestManifest(t, dir, "docker.io/sipeed/picoclaw:v0.3.1")

	old := newImageRunner
	newImageRunner = func() imageRunner { return &fakeImageRunner{pingErr: fmt.Errorf("boom")} }
	defer func() { newImageRunner = old }()
	oldDir := dirFlag
	dirFlag = dir
	defer func() { dirFlag = oldDir }()

	var out bytes.Buffer
	cmd := testCommand(&out)
	if err := runImageBuild(cmd, imageBuildFlags{tag: "piccolo/picoclaw-runtime:test", setManifest: true, yes: true}); err == nil {
		t.Fatal("expected docker error")
	}
	m, err := manifest.Load(filepath.Join(dir, "piccolo.yaml"))
	if err != nil {
		t.Fatalf("load manifest: %v", err)
	}
	if m.Container.Image != "docker.io/sipeed/picoclaw:v0.3.1" {
		t.Fatalf("manifest image changed to %q", m.Container.Image)
	}
}

func writeTestManifest(t *testing.T, dir, image string) {
	t.Helper()
	m := manifest.Manifest{Version: manifest.CurrentVersion}
	m.Container.Image = image
	m.Container.Name = "piccolo-gateway"
	m.Container.Network = "bridge"
	if err := m.Write(filepath.Join(dir, "piccolo.yaml")); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
}

func testCommand(out io.Writer) *cobra.Command {
	cmd := &cobra.Command{Use: "test"}
	cmd.SetOut(out)
	cmd.SetErr(io.Discard)
	cmd.SetIn(bytes.NewReader(nil))
	cmd.SetContext(context.Background())
	return cmd
}

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}
