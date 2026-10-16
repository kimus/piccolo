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

// --encrypt-keys and the wizard answer are two separate code paths into the same
// field, and the flag path was silently dropped the first time it was wired: an
// --yes run reported success while writing plaintext. Asserted on the artifact,
// not the flag plumbing, so a future path that loses the opt-in fails here.
func TestInitEncryptKeysCarriesThroughYesPath(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PICCOLO_KEY_PASSPHRASE", "horse")

	stdout, err := execInit(t, dir, "--yes", "--encrypt-keys",
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
		t.Fatalf("api_keys = %v, want one enc:// value (--encrypt-keys was accepted and ignored)", keys)
	}
	if strings.Contains(string(cfgData), "sk-INIT-PLAINTEXT") {
		t.Error("plaintext credential written despite --encrypt-keys")
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
	if m.PassphraseVar() != "PICCOLO_KEY_PASSPHRASE" {
		t.Errorf("piccolo.yaml did not record the variable: %q", m.PassphraseVar())
	}
	if _, err := os.Stat(filepath.Join(dir, "picoclaw", "keys", "picoclaw_ed25519.key")); err != nil {
		t.Errorf("key file missing: %v", err)
	}
	// The two files must agree: an encrypted config.json with no recorded route is
	// a workspace that cannot boot, and vice versa.
	if !strings.Contains(string(cfgData), credential.Scheme) || m.PassphraseVar() == "" {
		t.Error("config.json and piccolo.yaml disagree about encryption")
	}
}

// A workspace that does not ask for encryption is untouched by any of this:
// plaintext as before, no key directory, no secrets block. This is the
// no-opt-in-is-unchanged guarantee.
func TestInitWithoutEncryptKeysIsUnchanged(t *testing.T) {
	dir := t.TempDir()
	stdout, err := execInit(t, dir, "--yes",
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
	if err != nil || m.PassphraseVar() != "" {
		t.Errorf("secrets block recorded without opt-in: %q %v", m.PassphraseVar(), err)
	}
}

// Encryption needs a passphrase before any file exists. With neither a host
// variable nor a terminal, init must stop BEFORE writing — a workspace with an
// encrypted config.json and no way to decrypt it is worse than no workspace.
func TestInitEncryptionWithoutPassphraseWritesNothing(t *testing.T) {
	dir := t.TempDir()
	os.Unsetenv("PICCOLO_KEY_PASSPHRASE")

	stdout, err := execInit(t, dir, "--yes", "--encrypt-keys",
		"--name", "P", "--owner", "K", "--purpose", "personal",
		"--provider", "openai", "--model", "gpt-4o", "--api-key", "sk-x")
	if err == nil {
		t.Fatalf("init succeeded with no way to obtain a passphrase:\n%s", stdout)
	}
	if !strings.Contains(err.Error(), "PICCOLO_KEY_PASSPHRASE") {
		t.Errorf("error should name the variable to export: %v", err)
	}
	if !strings.Contains(err.Error(), "secrets encrypt") {
		t.Errorf("error should offer the after-the-fact route: %v", err)
	}
	// Nothing may have been written.
	if _, err := os.Stat(filepath.Join(dir, "picoclaw", "config.json")); err == nil {
		t.Error("init wrote config.json although it could not obtain a passphrase")
	}
}

// A --passphrase-env that looks like a secret is refused before any key is
// generated, mirroring the manifest validator.
func TestInitRejectsPassphraseShapedVariableName(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SOMETHING", "x")
	stdout, err := execInit(t, dir, "--yes", "--encrypt-keys", "--passphrase-env", "hunter2",
		"--name", "P", "--owner", "K", "--purpose", "personal",
		"--provider", "openai", "--model", "gpt-4o", "--api-key", "sk-x")
	if err == nil {
		t.Fatalf("accepted a passphrase as a variable name:\n%s", stdout)
	}
	if _, err := os.Stat(filepath.Join(dir, "picoclaw", "keys")); err == nil {
		t.Error("a key was generated before the bad name was rejected")
	}
}

// encryptionCandidateInputs is the Inputs shape init holds at the moment
// prepareEncryption runs: opted in, with the variable name chosen and no callback
// installed yet.
func encryptionCandidateInputs() *generate.Inputs {
	return &generate.Inputs{
		EncryptKeys:   true,
		PassphraseEnv: "PICCOLO_KEY_PASSPHRASE",
	}
}

// prepareEncryption is the piece init and secrets encrypt both need; a nil
// Encrypt callback would mean the plan renders plaintext.
func TestPrepareEncryptionInstallsTheCallback(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PICCOLO_KEY_PASSPHRASE", "horse")
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
	t.Setenv("PICCOLO_KEY_PASSPHRASE", "horse")

	// Flags supplied but no --yes and no terminal: init must refuse before writing.
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"init", "-D", dir, "--encrypt-keys",
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
	stdout, err := execInit(t, dir, "--yes",
		"--name", "P", "--owner", "K", "--purpose", "personal",
		"--provider", "openai", "--model", "gpt-4o", "--api-key", "sk-x")
	if err != nil {
		t.Fatalf("init: %v\n%s", err, stdout)
	}
	m, err := manifest.Load(filepath.Join(dir, "piccolo.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if m.PassphraseVar() != "" {
		t.Error("a plaintext workspace recorded a passphrase route it does not use")
	}
	if _, err := os.Stat(filepath.Join(dir, "picoclaw", "keys")); err == nil {
		t.Error("a plaintext workspace has a key directory")
	}
}
