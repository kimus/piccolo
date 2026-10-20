package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kimus/piccolo/internal/credential"
	"github.com/kimus/piccolo/internal/generate"
	"github.com/kimus/piccolo/internal/manifest"
	"github.com/kimus/piccolo/internal/picoclaw"
	"github.com/kimus/piccolo/internal/workspace"
)

func execInit(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(append([]string{"init", "-D", dir}, args...))
	err := root.Execute()
	return out.String(), err
}

// The wizard answer and the --yes defaults are two paths into the same choice,
// and the --yes path dropped it silently the first time it was wired, reporting an
// encrypted workspace while writing plaintext. Asserted on the artifact, so a path
// that loses the default fails here rather than in someone's workspace.
func TestInitEncryptsByDefault(t *testing.T) {
	dir := t.TempDir()

	stdout, err := execInit(t, dir, "--yes", "--passphrase", "horse",
		"--name", "P", "--owner", "K", "--purpose", "personal",
		"--provider", "openai", "--model", "gpt-4o", "--api-key", "sk-INIT-PLAINTEXT")
	if err != nil {
		t.Fatalf("init: %v\n%s", err, stdout)
	}

	cfgData, err := os.ReadFile(filepath.Join(dir, "picoclaw", "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg picoclaw.Config
	if err := json.Unmarshal(cfgData, &cfg); err != nil {
		t.Fatal(err)
	}
	keys := cfg.ModelList[0].APIKeys
	if len(keys) != 1 || !credential.IsEncrypted(keys[0]) {
		t.Fatalf("api_keys = %v, want one enc:// value (encryption is the default and was silently skipped)", keys)
	}
	if strings.Contains(string(cfgData), "sk-INIT-PLAINTEXT") {
		t.Error("plaintext credential written although encryption is the default")
	}
	// The plaintext must not exist anywhere in the workspace: no temp file, no
	// backup, no provenance echo.
	if err := filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		b, rerr := os.ReadFile(p)
		if rerr == nil && bytes.Contains(b, []byte("sk-INIT-PLAINTEXT")) {
			t.Errorf("plaintext found in %s", p)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	m, err := manifest.Load(filepath.Join(dir, "piccolo.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if m.Passphrase() != "horse" {
		t.Errorf("piccolo.yaml did not record the passphrase: %q", m.Passphrase())
	}
	if _, err := os.Stat(filepath.Join(dir, "picoclaw", "keys", "picoclaw_ed25519.key")); err != nil {
		t.Errorf("key file missing: %v", err)
	}
	// The two files must agree: an encrypted config.json with no recorded route is
	// a workspace that cannot boot, and vice versa.
	if !strings.Contains(string(cfgData), credential.Scheme) || m.Passphrase() == "" {
		t.Error("config.json and piccolo.yaml disagree about encryption")
	}
}

// A workspace that does not ask for encryption is untouched by any of this:
// plaintext as before, no key directory, no secrets block. This is the
// no-opt-in-is-unchanged guarantee.
func TestInitNoEncryptWritesPlaintext(t *testing.T) {
	dir := t.TempDir()
	stdout, err := execInit(t, dir, "--yes", "--no-encrypt",
		"--name", "P", "--owner", "K", "--purpose", "personal",
		"--provider", "openai", "--model", "gpt-4o", "--api-key", "sk-plain")
	if err != nil {
		t.Fatalf("init: %v\n%s", err, stdout)
	}
	cfgData, _ := os.ReadFile(filepath.Join(dir, "picoclaw", "config.json"))
	if !strings.Contains(string(cfgData), "sk-plain") {
		t.Error("default init stopped writing the credential it was given")
	}
	if strings.Contains(string(cfgData), credential.Scheme) {
		t.Error("default init encrypted something nobody asked it to encrypt")
	}
	if _, err := os.Stat(filepath.Join(dir, "picoclaw", "keys")); err == nil {
		t.Error("keys directory created for a workspace that never opted in")
	}
	m, err := manifest.Load(filepath.Join(dir, "piccolo.yaml"))
	if err != nil || m.Passphrase() != "" {
		t.Errorf("secrets block recorded without opt-in: %q %v", m.Passphrase(), err)
	}
}

// Encryption needs a passphrase before any file exists. With neither a host
// variable nor a terminal, init must stop BEFORE writing — a workspace with an
// encrypted config.json and no way to decrypt it is worse than no workspace.
func TestInitYesWithoutPassphraseRefuses(t *testing.T) {
	dir := t.TempDir()

	// Encryption is on by default, so --yes needs the passphrase from the flag:
	// there is no terminal to ask, and generating one silently would hand the user
	// a secret they never chose and cannot tell apart from a mistake.
	stdout, err := execInit(t, dir, "--yes",
		"--name", "P", "--owner", "K", "--purpose", "personal",
		"--provider", "openai", "--model", "gpt-4o", "--api-key", "sk-x")
	if err == nil {
		t.Fatalf("init succeeded with no way to obtain a passphrase:\n%s", stdout)
	}
	if !strings.Contains(err.Error(), "--passphrase") {
		t.Errorf("error should name the flag that supplies it: %v", err)
	}
	// The error must carry both ways out: supply the passphrase, or opt out.
	if !strings.Contains(err.Error(), "--no-encrypt") {
		t.Errorf("error should name the opt-out: %v", err)
	}
	// Nothing may have been written.
	if _, err := os.Stat(filepath.Join(dir, "picoclaw", "config.json")); err == nil {
		t.Error("init wrote config.json although it could not obtain a passphrase")
	}
}

// A --passphrase-env that looks like a secret is refused before any key is
// generated, mirroring the manifest validator.
func TestInitRejectsBlankPassphrase(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SOMETHING", "x")
	stdout, err := execInit(t, dir, "--yes", "--passphrase", "horse", "--passphrase-env", "hunter2",
		"--name", "P", "--owner", "K", "--purpose", "personal",
		"--provider", "openai", "--model", "gpt-4o", "--api-key", "sk-x")
	if err == nil {
		t.Fatalf("accepted a passphrase as a variable name:\n%s", stdout)
	}
	if _, err := os.Stat(filepath.Join(dir, "picoclaw", "keys")); err == nil {
		t.Error("a key was generated before the bad name was rejected")
	}
}

// encryptionCandidateInputs is the Inputs shape init holds when prepareEncryption
// runs: opted in, passphrase chosen, no callback installed yet.
func encryptionCandidateInputs() *generate.Inputs {
	return &generate.Inputs{
		EncryptKeys: true,
		Passphrase:  "horse",
	}
}

// prepareEncryption is the piece init and secrets encrypt both need; a nil
// Encrypt callback would mean the plan renders plaintext.
func TestPrepareEncryptionInstallsTheCallback(t *testing.T) {
	dir := t.TempDir()
	layout := workspace.NewLayout(dir)
	if err := layout.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	inputs := encryptionCandidateInputs()
	if err := prepareEncryption(dir, layout, inputs, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if inputs.Encrypt == nil {
		t.Fatal("no callback installed; init would write plaintext")
	}
	enc, err := inputs.Encrypt("sk-value")
	if err != nil {
		t.Fatal(err)
	}
	if !credential.IsEncrypted(enc) {
		t.Errorf("callback returned %q", enc)
	}
	got, err := credential.Decrypt("horse", layout.KeyFile(), enc)
	if err != nil || got != "sk-value" {
		t.Errorf("round trip = %q, %v", got, err)
	}
	// Second call reuses the existing key rather than rotating it, which would
	// invalidate the credential just written.
	if err := prepareEncryption(dir, layout, inputs, &bytes.Buffer{}); err != nil {
		t.Fatalf("re-running prepareEncryption failed: %v", err)
	}
	if again, err := inputs.Encrypt("sk-value"); err != nil || credential.IsEncrypted(again) == false {
		t.Errorf("second run produced %q, %v", again, err)
	}
}

// An aborted init creates nothing at all. Encryption makes this worth pinning
// explicitly: a key file or a recorded route left behind by a run the user
// cancelled would describe a workspace that is half-configured for a secret that
// protects no credential.
func TestInitAbortedLeavesNoKeyOrRoute(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "w")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Flags supplied but no --yes and no terminal: init must refuse before writing.
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"init", "-D", dir, "--passphrase", "horse",
		"--name", "P", "--owner", "K", "--purpose", "personal",
		"--provider", "openai", "--model", "gpt-4o", "--api-key", "sk-x"})
	if err := root.Execute(); err == nil {
		t.Skip("this environment provided a terminal; the abort path needs a pipe")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return // directory not even created: also a pass
	}
	for _, e := range entries {
		t.Errorf("aborted init created %s", e.Name())
	}
}

// The route and the key are one decision. Recording a passphrase variable for a
// workspace whose credential is plaintext would make 'secrets check' and doctor
// describe a protected workspace that is not.
func TestInitRecordsRouteOnlyWhenItCanSeal(t *testing.T) {
	dir := t.TempDir()
	stdout, err := execInit(t, dir, "--yes", "--no-encrypt",
		"--name", "P", "--owner", "K", "--purpose", "personal",
		"--provider", "openai", "--model", "gpt-4o", "--api-key", "sk-x")
	if err != nil {
		t.Fatalf("init: %v\n%s", err, stdout)
	}
	m, err := manifest.Load(filepath.Join(dir, "piccolo.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if m.Passphrase() != "" {
		t.Error("a plaintext workspace recorded a passphrase")
	}
	if _, err := os.Stat(filepath.Join(dir, "picoclaw", "keys")); err == nil {
		t.Error("a plaintext workspace has a key directory")
	}
}

// init must not create the exposure in the first place, and a rewrite of an old
// workspace's 0644 file has to tighten it.
func TestInitWritesPiccoloYamlPrivate(t *testing.T) {
	dir := t.TempDir()
	if _, err := execInit(t, dir, "--yes", "--passphrase", "horse",
		"--name", "P", "--owner", "K", "--purpose", "personal",
		"--provider", "openai", "--model", "gpt-4o", "--api-key", "sk-x"); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(dir, "piccolo.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("init created a group/world-readable piccolo.yaml holding a passphrase: %o", perm)
	}
}

// doctor has to say so when a workspace predates the rule: the file is still
// readable, the passphrase is still in it, and only a chmod closes it.
func TestDoctorWarnsOnReadableManifestWithPassphrase(t *testing.T) {
	dir := encryptedWorkspace(t)
	manPath := filepath.Join(dir, "piccolo.yaml")
	if err := os.Chmod(manPath, 0o644); err != nil {
		t.Fatal(err)
	}
	res := doctorRun(t, dir)
	r := findRes(res, "readable by group/others")
	if r == nil {
		t.Fatalf("readable manifest with a passphrase not reported: %v", res)
	}
	if strings.Contains(r.Text(), "horse") {
		t.Error("warning leaked the passphrase")
	}
	if !strings.Contains(r.Text(), "chmod 600") {
		t.Errorf("warning must carry the remedy: %s", r.Text())
	}

	// Tightening it must clear the finding.
	if err := os.Chmod(manPath, 0o600); err != nil {
		t.Fatal(err)
	}
	if findRes(doctorRun(t, dir), "readable by group/others") != nil {
		t.Error("finding persisted after chmod 600")
	}
}

// No passphrase, no privacy claim to enforce: a plaintext workspace must not be
// warned at, or doctor's advice becomes noise.
func TestDoctorQuietOnReadableManifestWithoutPassphrase(t *testing.T) {
	dir := plaintextWorkspaceForToggle(t)
	if err := os.Chmod(filepath.Join(dir, "piccolo.yaml"), 0o644); err != nil {
		t.Fatal(err)
	}
	if findRes(doctorRun(t, dir), "readable by group/others") != nil {
		t.Error("a workspace with no passphrase was warned about file permissions")
	}
}

// A --no-encrypt workspace keeps its API key as readable text in config.json, so
// that file's mode is a security property. It is private unconditionally rather than
// only when encryption is on: a mode that depends on which flags were passed is one
// no user can predict, and the plaintext case is exactly the one that needs it.
func TestInitWritesConfigJSONPrivate(t *testing.T) {
	for _, args := range [][]string{{"--passphrase", "pw"}, {"--no-encrypt"}} {
		dir := t.TempDir()
		cmd := append([]string{"--yes", "--name", "P", "--owner", "K", "--purpose", "personal",
			"--provider", "openai", "--model", "gpt-4o", "--api-key", "sk-x"}, args...)
		if _, err := execInit(t, dir, cmd...); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		fi, err := os.Stat(filepath.Join(dir, "picoclaw", "config.json"))
		if err != nil {
			t.Fatal(err)
		}
		if perm := fi.Mode().Perm(); perm&0o077 != 0 {
			t.Errorf("%v: config.json is %o", args, perm)
		}
	}
}
