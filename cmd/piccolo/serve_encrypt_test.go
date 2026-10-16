package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kimus/piccolo/internal/manifest"
	"github.com/kimus/piccolo/internal/workspace"
)

// An encrypted workspace missing either half of its unlock must be refused by
// preflight. The alternative is a container that exits at boot, and because serve
// parks the running gateway on the promotion path, "fails later" here means
// "service was displaced by something that could never work".
func TestServePreflightRefusesUndeliverableCredentials(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(t *testing.T, dir string)
		want   string
	}{
		{
			name: "passphrase stripped from piccolo.yaml",
			mutate: func(t *testing.T, dir string) {
				setPassphraseInManifest(t, dir, "")
			},
			want: "secrets.passphrase",
		},
		{
			name: "key file deleted",
			mutate: func(t *testing.T, dir string) {
				os.RemoveAll(filepath.Join(dir, "picoclaw", "keys"))
			},
			want: "key file",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Fresh workspace per case: the mutations are destructive.
			w := encryptedWorkspace(t)
			tc.mutate(t, w)

			err := preflightFails(w)
			if err == nil {
				t.Fatalf("serve started a gateway that cannot decrypt its own config")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error should name %q, got: %v", tc.want, err)
			}
			// Never blame config.json for a missing host variable: that is the
			// misleading half of the failure the user would otherwise chase.
			if strings.Contains(err.Error(), "config.json does not parse") {
				t.Errorf("error misattributed the cause: %v", err)
			}
		})
	}
}

// A workspace that never opted in is unaffected: no credential env, no new
// failure mode, nothing to deliver.
func TestServePreflightUnchangedForPlaintextWorkspace(t *testing.T) {
	dir := t.TempDir()
	stdout, err := execInit(t, dir, "--yes", "--no-encrypt", "--name", "P", "--owner", "K", "--purpose", "personal",
		"--provider", "openai", "--model", "gpt-4o", "--api-key", "sk-plain")
	if err != nil {
		t.Fatalf("init: %v\n%s", err, stdout)
	}
	// The credential guard, not all of preflight: the guard must stay silent here,
	// and it must not need a Docker daemon to say so. Full preflight requires a
	// live daemon, which is a property of the machine running the tests and would
	// turn this positive control into a flaky one.
	if err := checkCredentialDelivery(dir); err != nil {
		t.Errorf("plaintext workspace was refused by the credential guard: %v", err)
	}
}

// The manifest's own env map wins, mirroring withGatewayHost: a user who set
// PICOCLAW_SSH_KEY_PATH by hand is not silently overridden by Piccolo's default.
func TestWithCredentialEnvRespectsExplicitValues(t *testing.T) {
	dir := encryptedWorkspace(t)
	layout := workspace.NewLayout(dir)
	m, err := loadManifestForTest(dir)
	if err != nil {
		t.Fatal(err)
	}
	// The fixture really is encrypted, or this test proves nothing.
	if m.Passphrase() == "" {
		t.Fatal("fixture has no secrets.passphrase")
	}

	env := withCredentialEnv([]string{"A=1"}, layout, m.Passphrase())
	if !contains(env, "PICOCLAW_KEY_PASSPHRASE=horse") {
		t.Errorf("passphrase not injected: %v", env)
	}
	if !contains(env, "PICOCLAW_SSH_KEY_PATH="+layout.ContainerKeyFile()) {
		t.Errorf("key path not injected: %v", env)
	}

	// Explicit values survive.
	kept := withCredentialEnv([]string{"PICOCLAW_SSH_KEY_PATH=/custom/path"}, layout, m.Passphrase())
	if contains(kept, "PICOCLAW_SSH_KEY_PATH="+layout.ContainerKeyFile()) {
		t.Error("Piccolo overwrote an explicit PICOCLAW_SSH_KEY_PATH")
	}
	if !contains(kept, "PICOCLAW_SSH_KEY_PATH=/custom/path") {
		t.Errorf("explicit value lost: %v", kept)
	}

	// An empty passphrase is not injected as an empty assignment: that would set
	// the variable to "" and PicoClaw reports "passphrase required" rather than
	// "you never told me where it came from".
	blank := withCredentialEnv(nil, layout, "")
	if contains(blank, "PICOCLAW_KEY_PASSPHRASE=") {
		t.Error("empty passphrase injected as an assignment")
	}
}

// The key path must be the in-container one. A host path here would name a file
// that does not exist inside the gateway, and every credential would fail to
// decrypt with an error that says "cannot read SSH key".
func TestWithCredentialEnvUsesContainerPath(t *testing.T) {
	dir := encryptedWorkspace(t)
	layout := workspace.NewLayout(dir)
	m, err := loadManifestForTest(dir)
	if err != nil {
		t.Fatal(err)
	}
	env := withCredentialEnv(nil, layout, m.Passphrase())
	var keyArg string
	for _, kv := range env {
		if strings.HasPrefix(kv, "PICOCLAW_SSH_KEY_PATH=") {
			keyArg = strings.TrimPrefix(kv, "PICOCLAW_SSH_KEY_PATH=")
		}
	}
	if !strings.HasPrefix(keyArg, "/root/.picoclaw/") {
		t.Errorf("key path %q is not inside the mounted container home", keyArg)
	}
	if strings.Contains(keyArg, dir) {
		t.Errorf("host path leaked into the container env: %q", keyArg)
	}
	// And it lives under the directory that is actually mounted.
	if filepath.Dir(keyArg) != workspace.ContainerKeysDir {
		t.Errorf("key dir %q != %q", filepath.Dir(keyArg), workspace.ContainerKeysDir)
	}
}

// setPassphraseInManifest rewrites secrets.passphrase, standing in for a user who
// edits piccolo.yaml or clones a workspace whose copy lost it.
func setPassphraseInManifest(t *testing.T, dir, passphrase string) {
	t.Helper()
	path := workspace.NewLayout(dir).Manifest()
	m, err := manifest.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	m.Secrets.Passphrase = passphrase
	if err := m.Write(path); err != nil {
		t.Fatal(err)
	}
}

func contains(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}

// preflightFails runs serve's preflight directly. It must not be reached by
// executing `serve`: preflight is the guard under test, and driving the full
// command would create real containers whenever the guard correctly stays out of
// the way. dirFlag is set and restored because preflight reads it.
func preflightFails(dir string) error {
	saved := dirFlag
	defer func() { dirFlag = saved }()
	dirFlag = dir
	cmd := newServeCmd()
	// preflight reads cmd.Context() and panics on a nil parent; a command built
	// directly is never run through Cobra, so the context is supplied here.
	cmd.SetContext(context.Background())
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	_, err := preflight(cmd)
	return err
}

func loadManifestForTest(dir string) (*manifest.Manifest, error) {
	return manifest.Load(workspace.NewLayout(dir).Manifest())
}
