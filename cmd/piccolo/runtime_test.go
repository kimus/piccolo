package main

import (
	"strings"
	"testing"

	"github.com/kimus/piccolo/internal/manifest"
	"github.com/kimus/piccolo/internal/runtimeimage"
)

func TestEffectiveToolchainDefaults(t *testing.T) {
	got := effectiveToolchain(&manifest.Manifest{})
	if len(got.Packages) != len(runtimeimage.DefaultPackages) || len(got.Commands) != len(runtimeimage.DefaultCommands) {
		t.Fatalf("defaults = %+v", got)
	}
	if !containsRuntime(got.Commands, "pdfinfo") || !containsRuntime(got.Commands, "magick") || !containsRuntime(got.Commands, "soffice") {
		t.Fatalf("strong defaults missing from commands: %v", got.Commands)
	}
}

func TestEffectiveToolchainCustom(t *testing.T) {
	m := &manifest.Manifest{Runtime: manifest.Runtime{Toolchain: manifest.Toolchain{Packages: []string{"file"}, Commands: []string{"file"}}}}
	got := effectiveToolchain(m)
	if strings.Join(got.Packages, ",") != "file" || strings.Join(got.Commands, ",") != "file" {
		t.Fatalf("custom toolchain overwritten: %+v", got)
	}
}

func TestRuntimeCommandCheckScriptQuotesCommands(t *testing.T) {
	s := runtimeCommandCheckScript([]string{"file", "pdfinfo"})
	for _, want := range []string{"command -v 'file'", "command -v 'pdfinfo'", "command missing"} {
		if !strings.Contains(s, want) {
			t.Fatalf("script missing %q:\n%s", want, s)
		}
	}
}

func containsRuntime(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}
