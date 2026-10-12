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
