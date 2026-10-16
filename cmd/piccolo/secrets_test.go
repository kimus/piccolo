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

	stdout, err := runSecrets(t, dir, "encrypt", "--yes", "--passphrase", "horse")
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
	// "committed" is the claim that matters: the passphrase now sits in a file the
	// workspace is meant to commit, and the warning must say so rather than let
	// "encrypted" imply protection this arrangement no longer provides.
	for _, want := range []string{"not reversible", "config.json.bak", "piccolo.yaml", "committed"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("warning omitted %q:\n%s", want, stdout)
		}
	}
	// Key + manifest + gitignore, the three pieces the delivery route needs.
	if _, err := os.Stat(filepath.Join(dir, "picoclaw", "keys", "picoclaw_ed25519.key")); err != nil {
		t.Errorf("key file missing: %v", err)
	}
	m, err := manifest.Load(filepath.Join(dir, "piccolo.yaml"))
	if err != nil || m.Passphrase() != "horse" {
		t.Errorf("manifest did not record the passphrase: %v %q", err, m.Passphrase())
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
	if !strings.Contains(err.Error(), "--passphrase") {
		t.Errorf("refusal should name the flag that supplies the passphrase: %v", err)
	}
}

// The key is generated before the warning and the gate in the current code, so
// assert that refusing leaves no half-configured workspace behind: a recorded
// secrets block with a plaintext config would make 'secrets check' look encrypted.
func TestSecretsRefusalLeavesWorkspaceConsistent(t *testing.T) {
	dir := encryptWorkspaceShell(t)

	if _, err := runSecrets(t, dir, "encrypt"); err == nil {
		t.Fatal("expected refusal")
	}
	m, err := manifest.Load(filepath.Join(dir, "piccolo.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if m.Passphrase() != "" {
		t.Error("refused run recorded secrets.passphrase, so the workspace claims to be encrypted")
	}
	cfgData, _ := os.ReadFile(filepath.Join(dir, "picoclaw", "config.json"))
	if strings.Contains(string(cfgData), credential.Scheme) {
		t.Error("refused run wrote ciphertext")
	}
}

// No --passphrase and no terminal means no passphrase. The command must refuse,
// and crucially must not have generated a key first: a key file in a workspace
// whose credentials are still plaintext is a half-configured state that later
// reports and mislead.
func TestSecretsEncryptWithoutPassphraseLeavesNoKey(t *testing.T) {
	dir := encryptWorkspaceShell(t)
	stdout, err := runSecrets(t, dir, "encrypt", "--yes")
	if err == nil {
		t.Fatalf("accepted a run with no way to obtain a passphrase: %s", stdout)
	}
	if !strings.Contains(err.Error(), "--passphrase") {
		t.Errorf("error should name the flag that supplies it: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "picoclaw", "keys")); statErr == nil {
		t.Error("a key directory was created before the missing passphrase was noticed")
	}
}

// A blank passphrase is refused for the same reason: it would seal credentials that
// nothing can later decrypt.
func TestSecretsEncryptRejectsBlankPassphrase(t *testing.T) {
	dir := encryptWorkspaceShell(t)
	stdout, err := runSecrets(t, dir, "encrypt", "--yes", "--passphrase", "   ")
	if err == nil {
		t.Fatalf("accepted a blank passphrase: %s", stdout)
	}
	if !strings.Contains(err.Error(), "decrypt") {
		t.Errorf("error should state the consequence: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "picoclaw", "keys")); statErr == nil {
		t.Error("a key directory was created for a refused passphrase")
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

	if _, err := runSecrets(t, dir, "encrypt", "--yes", "--passphrase", "horse"); err != nil {
		t.Fatal(err)
	}

	// 2. Encrypted and decryptable.
	stdout, err = runSecrets(t, dir, "check")
	if err != nil || !strings.Contains(stdout, "OK") {
		t.Fatalf("check on a healthy encrypted workspace: %v\n%s", err, stdout)
	}

	// 3. Encrypted but the recorded passphrase is wrong — the state a user reaches
	// by editing piccolo.yaml or restoring a stale copy. It otherwise surfaces as a
	// container exit and a health-gate timeout two steps later.
	setPassphraseInManifest(t, dir, "wrong")
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

	if _, err := runSecrets(t, dir, "encrypt", "--yes", "--passphrase", "horse"); err != nil {
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
	// Asserted on substance, not headings: the help must not be allowed to imply
	// that storing the passphrase in piccolo.yaml still protects a copied
	// workspace, however the sections get reworded later.
	for _, want := range []string{"DOES NOT", "passphrase is stored\n", "piccolo.yaml"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("help omitted %q — the honest scope statement is required:\n%s", want, stdout)
		}
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
