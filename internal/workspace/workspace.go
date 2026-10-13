// Package workspace resolves the Piccolo workspace directory shared by all
// commands: the -D/--dir flag when given, otherwise the current directory.
package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ManifestName is the Piccolo manifest file marking a workspace.
const ManifestName = "piccolo.yaml"

const (
	PicoclawDir     = "picoclaw"
	IntegrationsDir = "integrations"
	RuntimeDir      = "runtime"
	RuntimeDevbox   = "devbox"
	RuntimeNix      = "nix"
	RuntimeMedia    = "media"
	ConfigName      = "config.json"
	PidFileName     = ".picoclaw.pid"

	ContainerPicoclawHome = "/root/.picoclaw"
	ContainerPiccoloHome  = "/root/.piccolo"
	ContainerIntegrations = ContainerPiccoloHome + "/integrations"
	ContainerDevbox       = "/root/.local/share/devbox/global/default"
	ContainerNix          = "/nix"
	ContainerMedia        = "/tmp/picoclaw_media"
)

// Layout centralizes host and container paths for a Piccolo workspace.
type Layout struct{ Root string }

func NewLayout(root string) Layout              { return Layout{Root: root} }
func (l Layout) Manifest() string               { return filepath.Join(l.Root, ManifestName) }
func (l Layout) PicoclawHome() string           { return filepath.Join(l.Root, PicoclawDir) }
func (l Layout) Config() string                 { return filepath.Join(l.PicoclawHome(), ConfigName) }
func (l Layout) PidFile() string                { return filepath.Join(l.PicoclawHome(), PidFileName) }
func (l Layout) Agents() string                 { return filepath.Join(l.PicoclawHome(), "agents") }
func (l Layout) Integrations() string           { return filepath.Join(l.Root, IntegrationsDir) }
func (l Layout) Integration(name string) string { return filepath.Join(l.Integrations(), name) }
func (l Layout) Runtime() string                { return filepath.Join(l.Root, RuntimeDir) }
func (l Layout) RuntimeDevbox() string          { return filepath.Join(l.Runtime(), RuntimeDevbox) }
func (l Layout) RuntimeNix() string             { return filepath.Join(l.Runtime(), RuntimeNix) }
func (l Layout) RuntimeMedia() string           { return filepath.Join(l.Runtime(), RuntimeMedia) }

func (l Layout) RuntimeDirs() []string {
	return []string{l.RuntimeDevbox(), l.RuntimeNix(), l.RuntimeMedia(), l.Integrations(), l.PicoclawHome()}
}

func (l Layout) IntegrationBind() string {
	return l.Integrations() + ":" + ContainerIntegrations + ":rw"
}

func (l Layout) RuntimeBinds() []string {
	return []string{
		l.RuntimeDevbox() + ":" + ContainerDevbox + ":rw",
		l.RuntimeNix() + ":" + ContainerNix + ":rw",
		l.RuntimeMedia() + ":" + ContainerMedia + ":rw",
	}
}

func (l Layout) EnsureDirs() error {
	for _, dir := range l.RuntimeDirs() {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return nil
}

func IsLegacyFlat(root string) bool {
	if _, err := os.Stat(filepath.Join(root, ConfigName)); err != nil {
		return false
	}
	if _, err := os.Stat(filepath.Join(root, "agents")); err != nil {
		return false
	}
	if _, err := os.Stat(filepath.Join(root, PicoclawDir, ConfigName)); err == nil {
		return false
	}
	return true
}

// Resolve returns the absolute workspace path: dirFlag when non-empty,
// otherwise the current working directory.
func Resolve(dirFlag string) (string, error) {
	dir := strings.TrimSpace(dirFlag)
	if dir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("resolve current directory: %w", err)
		}
		return cwd, nil
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("resolve workspace %q: %w", dir, err)
	}
	return abs, nil
}

// HasManifest reports whether dir looks like an initialized workspace.
func HasManifest(dir string) bool {
	st, err := os.Stat(filepath.Join(dir, ManifestName))
	return err == nil && !st.IsDir()
}

// Require resolves the workspace and fails with an actionable error when it
// is not initialized.
func Require(dirFlag string) (string, error) {
	dir, err := Resolve(dirFlag)
	if err != nil {
		return "", err
	}
	if !HasManifest(dir) {
		return "", fmt.Errorf("no piccolo workspace found in %s — run 'piccolo init' first", dir)
	}
	return dir, nil
}
