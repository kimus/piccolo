package main

import (
	"bytes"
	"encoding/json"
	"io"
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

// encryptWorkspaceShell builds a minimal encrypted-capable workspace by hand, so
// a test can state exactly what it expects rather than tracking init's defaults.
func encryptWorkspaceShell(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	// Reuse the real generator so the fixture cannot drift from what init writes.
	in := generate.Inputs{
		AssistantName: "P", OwnerName: "K", Purpose: "personal",
		Specialists: []string{}, Dashboard: false,
		Model:     generate.ModelInput{Alias: "m1", Provider: "openai", Model: "gpt-4o", APIKey: "sk-CLICHET"},
		WebSearch: true,
	}
	plan, err := generate.BuildPlan(in)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := plan.Write(dir, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "integrations"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := generate.EnsureGitignore(dir); err != nil {
		t.Fatal(err)
	}
	return dir
}

// runSecrets drives the command the way a user does: through the root command, so
// flag wiring and workspace resolution are covered, not just the helper.
func runSecrets(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(append([]string{"secrets"}, append(args, "-D", dir)...))
	err := root.Execute()
	return out.String(), err
}

func TestSecretsEncryptWithYesWritesCiphertext(t *testing.T) {
	dir := encryptWorkspaceShell(t)
	t.Setenv("PICCOLO_KEY_PASSPHRASE", "horse")

	stdout, err := runSecrets(t, dir, "encrypt", "--yes")
	if err != nil {
		t.Fatalf("encrypt: %v\n%s", err, stdout)
	}
	cfgData, err := os.ReadFile(filepath.Join(dir, "picoclaw", "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg picoclaw.Config
	if err := json.Unmarshal(cfgData, &cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg.ModelList) == 0 {
		t.Fatal("model_list disappeared")
	}
	keys := cfg.ModelList[0].APIKeys
	if len(keys) != 1 || !credential.IsEncrypted(keys[0]) {
		t.Fatalf("api_keys = %v, want one enc:// value", keys)
	}
	if strings.Contains(string(cfgData), "sk-CLICHET") {
		t.Error("plaintext credential survived in config.json")
	}
	// The warning must be shown, not merely exist somewhere in the code.
	for _, want := range []string{"not reversible", "config.json.bak", "docker inspect"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("warning omitted %q:\n%s", want, stdout)
		}
	}
	// Key + manifest + gitignore, the three pieces the delivery route needs.
	if _, err := os.Stat(filepath.Join(dir, "picoclaw", "keys", "picoclaw_ed25519.key")); err != nil {
		t.Errorf("key file missing: %v", err)
	}
	m, err := manifest.Load(filepath.Join(dir, "piccolo.yaml"))
	if err != nil || m.PassphraseVar() != "PICCOLO_KEY_PASSPHRASE" {
		t.Errorf("manifest did not record the variable: %v %q", err, m.PassphraseVar())
	}
	if missing, _ := generate.MissingGitignoreEntries(dir); len(missing) != 0 {
		t.Errorf("gitignore gaps after encrypt: %v", missing)
	}
	// And the ciphertext round-trips with the key it was written against.
	if got, err := credential.Decrypt("horse", filepath.Join(dir, "picoclaw", "keys", "picoclaw_ed25519.key"), keys[0]); err != nil || got != "sk-CLICHET" {
		t.Errorf("round trip = %q, %v", got, err)
	}
}

// --yes is the non-interactive route; without it and without a terminal the
// command must refuse rather than write, and must not have modified the file.
func TestSecretsEncryptRefusesWithoutTerminal(t *testing.T) {
	dir := encryptWorkspaceShell(t)
	t.Setenv("PICCOLO_KEY_PASSPHRASE", "horse")

	before, _ := os.ReadFile(filepath.Join(dir, "picoclaw", "config.json"))
	// Tests have no tty, so the confirmation path is exactly what runs here.
	stdout, err := runSecrets(t, dir, "encrypt")
	if err == nil {
		t.Fatalf("encrypt wrote without any confirmation: %s", stdout)
	}
	after, _ := os.ReadFile(filepath.Join(dir, "picoclaw", "config.json"))
	if !bytes.Equal(before, after) {
		t.Error("config.json was modified although the confirmation could not happen")
	}
	if !strings.Contains(err.Error(), "--yes") {
		t.Errorf("refusal should name the non-interactive route: %v", err)
	}
}

// The key is generated before the warning and the gate in the current code, so
// assert that refusing leaves no half-configured workspace behind: a recorded
// secrets block with a plaintext config would make 'secrets check' look encrypted.
func TestSecretsRefusalLeavesWorkspaceConsistent(t *testing.T) {
	dir := encryptWorkspaceShell(t)
	t.Setenv("PICCOLO_KEY_PASSPHRASE", "horse")

	if _, err := runSecrets(t, dir, "encrypt"); err == nil {
		t.Fatal("expected refusal")
	}
	m, err := manifest.Load(filepath.Join(dir, "piccolo.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if m.PassphraseVar() != "" {
		t.Error("refused run recorded secrets.passphrase_env, so the workspace claims to be encrypted")
	}
	cfgData, _ := os.ReadFile(filepath.Join(dir, "picoclaw", "config.json"))
	if strings.Contains(string(cfgData), credential.Scheme) {
		t.Error("refused run wrote ciphertext")
	}
}

func TestSecretsEncryptNeedsAPassphrase(t *testing.T) {
	dir := encryptWorkspaceShell(t)
	// Neither the host variable nor a terminal: no way to obtain a passphrase.
	os.Unsetenv("PICCOLO_KEY_PASSPHRASE")
	stdout, err := runSecrets(t, dir, "encrypt", "--yes")
	if err == nil {
		t.Fatalf("accepted an empty passphrase: %s", stdout)
	}
	if !strings.Contains(err.Error(), "PICCOLO_KEY_PASSPHRASE") {
		t.Errorf("error should name the variable to export: %v", err)
	}
}

// A --passphrase-env that looks like a secret must be refused before anything is
// generated, exactly as the manifest validator refuses it in piccolo.yaml.
func TestSecretsEncryptRejectsPassphraseShapedEnvName(t *testing.T) {
	dir := encryptWorkspaceShell(t)
	stdout, err := runSecrets(t, dir, "encrypt", "--yes", "--passphrase-env", "hunter2")
	if err == nil {
		t.Fatalf("accepted a passphrase-shaped variable name: %s", stdout)
	}
	if !strings.Contains(err.Error(), "NAME") {
		t.Errorf("error should explain name-vs-value: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "picoclaw", "keys")); err == nil {
		t.Error("a key directory was generated before the bad name was noticed")
	}
}

func TestSecretsCheckStatesTheThreeStates(t *testing.T) {
	dir := encryptWorkspaceShell(t)

	// 1. Nothing encrypted yet.
	stdout, err := runSecrets(t, dir, "check")
	if err != nil {
		t.Fatalf("check on a plaintext workspace should succeed: %v\n%s", err, stdout)
	}
	if !strings.Contains(stdout, "no enc:// credential") {
		t.Errorf("unexpected check output: %s", stdout)
	}

	t.Setenv("PICCOLO_KEY_PASSPHRASE", "horse")
	if _, err := runSecrets(t, dir, "encrypt", "--yes"); err != nil {
		t.Fatal(err)
	}

	// 2. Encrypted and decryptable.
	stdout, err = runSecrets(t, dir, "check")
	if err != nil || !strings.Contains(stdout, "OK") {
		t.Fatalf("check on a healthy encrypted workspace: %v\n%s", err, stdout)
	}

	// 3. Encrypted but the passphrase is wrong: the state that otherwise shows up
	// as a container exit and a health-gate timeout two steps later.
	t.Setenv("PICCOLO_KEY_PASSPHRASE", "wrong")
	stdout, err = runSecrets(t, dir, "check")
	if err == nil {
		t.Fatal("check passed a workspace that cannot decrypt")
	}
	if !strings.Contains(stdout, "decryption failed") {
		t.Errorf("check should name the failure: %s", stdout)
	}
}

// Losing the key file is indistinguishable from a wrong passphrase at boot, so
// check must call it out while the user can still act on it.
func TestSecretsCheckNamesAMissingKeyFile(t *testing.T) {
	dir := encryptWorkspaceShell(t)
	t.Setenv("PICCOLO_KEY_PASSPHRASE", "horse")
	if _, err := runSecrets(t, dir, "encrypt", "--yes"); err != nil {
		t.Fatal(err)
	}
	keyDir := filepath.Join(dir, "picoclaw", "keys")
	if err := os.RemoveAll(keyDir); err != nil {
		t.Fatal(err)
	}
	stdout, err := runSecrets(t, dir, "check")
	if err == nil {
		t.Fatal("check passed with no key file")
	}
	if !strings.Contains(stdout, "key file") {
		t.Errorf("check should name the missing key file: %s", stdout)
	}
}

func TestSecretsGroupWithoutArgsShowsHelp(t *testing.T) {
	stdout, err := runSecrets(t, t.TempDir())
	if err != nil {
		t.Fatalf("bare group should print help and succeed: %v\n%s", err, stdout)
	}
	if !strings.Contains(stdout, "WHAT THIS DOES NOT HIDE") {
		t.Errorf("help omitted the honest scope statement:\n%s", stdout)
	}
}

func TestWithinMountRoot(t *testing.T) {
	for _, tc := range []struct {
		root, path string
		want       bool
	}{
		{"/w/picoclaw", "/w/picoclaw/keys/k.key", true},
		{"/w/picoclaw", "/w/picoclaw", true},
		{"/w/picoclaw", "/w/integrations/k.key", false},
		{"/w/picoclaw", "/w/k.key", false},
	} {
		if got := within(tc.root, tc.path); got != tc.want {
			t.Errorf("within(%q,%q) = %v", tc.root, tc.path, got)
		}
	}
}

// layout.KeyFile is inside the mounted home by construction; the check that
// guards it must not become dead code the day someone moves the directory.
func TestKeyPathStaysInsideTheMount(t *testing.T) {
	l := workspace.NewLayout("/work/demo")
	if !within(l.PicoclawHome(), l.KeyFile()) {
		t.Fatal("the key file left the mounted picoclaw home; the gateway could not read it")
	}
}

// The answer rule, reachable only because it is split from the tty check: default
// is No, and anything that is not an explicit yes does not confirm.
func TestParseConfirmationDefaultsToNo(t *testing.T) {
	for _, tc := range []struct {
		line string
		err  error
		want bool
	}{
		{"y\n", nil, true},
		{"Y\n", nil, true},
		{"yes\n", nil, true},
		{"  YES  \n", nil, true},
		{"\n", nil, false},
		{"n\n", nil, false},
		{"no\n", nil, false},
		{"maybe\n", nil, false},
		{"", io.EOF, false},           // read failure with nothing buffered: do nothing
		{"y", io.ErrClosedPipe, true}, // an answer already typed is still an answer
	} {
		if got := parseConfirmation(tc.line, tc.err); got != tc.want {
			t.Errorf("parseConfirmation(%q,%v) = %v, want %v", tc.line, tc.err, got, tc.want)
		}
	}
}

// Without a terminal the gate must refuse rather than read the injected reader,
// or a piped invocation could confirm a destructive write nobody asked for.
func TestConfirmDestructiveRefusesWithoutTerminal(t *testing.T) {
	var out bytes.Buffer
	got, err := confirmDestructive(&out, strings.NewReader("y\n"), "encrypt?")
	if err == nil {
		t.Fatal("confirmation read a pipe as if it were a terminal")
	}
	if got {
		t.Error("refusal still reported confirmation")
	}
	if !strings.Contains(err.Error(), "--yes") {
		t.Errorf("error should name the non-interactive route: %v", err)
	}
}
