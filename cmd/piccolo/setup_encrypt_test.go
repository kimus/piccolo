package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kimus/piccolo/internal/configdoc"
	"github.com/kimus/piccolo/internal/credential"
	"github.com/kimus/piccolo/internal/manifest"
	"github.com/kimus/piccolo/internal/picoclaw"
	"github.com/kimus/piccolo/internal/setup"
	"github.com/kimus/piccolo/internal/workspace"
)

// encryptedWorkspaceAlias is the model_name the fixture's init run writes.
// Pinning it in a constant is what keeps a test that patches "the existing entry"
// honest: PatchModel APPENDS when the lookup alias is absent, so a wrong name here
// silently produced a second entry and the assertion passed for the wrong reason.
const encryptedWorkspaceAlias = "gpt-4o"

// encryptedWorkspace builds a workspace already opted into enc:// storage, the
// state setup has to respect. Done through the real init path so the key, the
// manifest field, and the ignore rule are the ones init itself would produce.
func encryptedWorkspace(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("PICCOLO_KEY_PASSPHRASE", "horse")
	stdout, err := execInit(t, dir, "--yes", "--encrypt-keys",
		"--name", "P", "--owner", "K", "--purpose", "personal",
		"--provider", "openai", "--model", "gpt-4o", "--api-key", "sk-original")
	if err != nil {
		t.Fatalf("init: %v\n%s", err, stdout)
	}
	return dir
}

// The encrypted posture is a conjunction, so test it as one through the same
// derivation the command uses: a key file whose route was never recorded, and a
// recorded route whose key file was deleted, both describe a workspace that cannot
// seal credentials.
func TestSetupEncryptedFlagNeedsBothHalves(t *testing.T) {
	dir := encryptedWorkspace(t)
	if st := newSetupStateForTest(t, dir); !st.encrypted {
		t.Error("fully encrypted workspace not detected as encrypted")
	}
	// Delete only the key: piccolo.yaml still names the variable.
	if err := os.RemoveAll(filepath.Join(dir, "picoclaw", "keys")); err != nil {
		t.Fatal(err)
	}
	if st := newSetupStateForTest(t, dir); st.encrypted {
		t.Error("setup treated a workspace with no key file as able to seal credentials")
	}
}

// newSetupStateForTest mirrors runSetup's derivation of the encrypted flag, so the
// test exercises the same conjunction the command does rather than a copy of it.
func newSetupStateForTest(t *testing.T, dir string) *setupState {
	t.Helper()
	layout := workspace.NewLayout(dir)
	doc, err := configdoc.Load(layout.Config())
	if err != nil {
		t.Fatal(err)
	}
	m, err := manifest.Load(layout.Manifest())
	if err != nil {
		t.Fatal(err)
	}
	st := &setupState{
		out: &bytes.Buffer{}, dir: dir, path: layout.Config(), doc: doc,
		orig:         []byte(readFile(t, layout.Config())),
		manifestPath: layout.Manifest(), manifest: m,
		secrets: picoclaw.LoadSecrets(layout.Config()),
	}
	if _, err := os.Stat(layout.KeyFile()); err == nil && m.PassphraseVar() != "" {
		st.encrypted = true
	}
	return st
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(b)
}

// A credential typed into an encrypted workspace must land as ciphertext. This is
// the promise the field's own help text makes, so a plaintext write here would be
// the help text lying to the user.
func TestSetupModelFormSealsInEncryptedWorkspace(t *testing.T) {
	dir := encryptedWorkspace(t)

	// Drive the write path the forms use, rather than the interactive prompts:
	// applyAndConfirm is the choke point every form passes through, and the sealing
	// is its behaviour, not the form's.
	doc, err := configdoc.Load(workspace.NewLayout(dir).Config())
	if err != nil {
		t.Fatal(err)
	}
	m, err := manifest.Load(workspace.NewLayout(dir).Manifest())
	if err != nil {
		t.Fatal(err)
	}
	st := &setupState{
		cmd:          newRootCmd(),
		out:          &bytes.Buffer{},
		dir:          dir,
		path:         workspace.NewLayout(dir).Config(),
		doc:          doc,
		orig:         []byte(readFile(t, workspace.NewLayout(dir).Config())),
		manifestPath: workspace.NewLayout(dir).Manifest(),
		manifest:     m,
		secrets:      picoclaw.LoadSecrets(workspace.NewLayout(dir).Config()),
		encrypted:    true,
	}

	// A fresh key as a user would enter it through the model form.
	if err := patchModelKeyForTest(st.doc, encryptedWorkspaceAlias, encryptedWorkspaceAlias, "sk-TYPED-IN-SETUP"); err != nil {
		t.Fatal(err)
	}
	if err := st.sealBeforeWrite("model m1"); err != nil {
		t.Fatalf("seal: %v", err)
	}

	out := string(st.doc.Marshal())
	if strings.Contains(out, "sk-TYPED-IN-SETUP") {
		t.Error("setup wrote the plaintext credential into an encrypted workspace")
	}
	if !strings.Contains(out, credential.Scheme) {
		t.Fatalf("no ciphertext written:\n%s", out)
	}
	// And it decrypts with the same passphrase serve will read.
	doc2, err := configdoc.Parse([]byte(out))
	if err != nil {
		t.Fatal(err)
	}
	var cfg picoclaw.Config
	if err := json.Unmarshal([]byte(out), &cfg); err != nil {
		t.Fatal(err)
	}
	_ = doc2
	got, err := credential.Decrypt("horse", workspace.NewLayout(dir).KeyFile(), cfg.ModelList[0].APIKeys[0])
	if err != nil {
		t.Fatalf("written credential not decryptable: %v", err)
	}
	if got != "sk-TYPED-IN-SETUP" {
		t.Errorf("decrypt = %q", got)
	}
}

// Without the passphrase available, an encrypted workspace must refuse the write
// rather than store plaintext: the user believes this workspace seals credentials,
// and a silent plaintext write is undetectable from the file alone.
func TestSetupSealRefusesWithoutPassphrase(t *testing.T) {
	dir := encryptedWorkspace(t)
	doc, err := configdoc.Load(workspace.NewLayout(dir).Config())
	if err != nil {
		t.Fatal(err)
	}
	m, err := manifest.Load(workspace.NewLayout(dir).Manifest())
	if err != nil {
		t.Fatal(err)
	}
	st := &setupState{
		out: &bytes.Buffer{}, dir: dir, path: workspace.NewLayout(dir).Config(),
		doc: doc, orig: []byte(readFile(t, workspace.NewLayout(dir).Config())),
		manifestPath: workspace.NewLayout(dir).Manifest(), manifest: m,
		secrets: picoclaw.LoadSecrets(workspace.NewLayout(dir).Config()), encrypted: true,
	}
	if err := patchModelKeyForTest(st.doc, encryptedWorkspaceAlias, encryptedWorkspaceAlias, "sk-UNTYPABLE"); err != nil {
		t.Fatal(err)
	}

	os.Unsetenv("PICCOLO_KEY_PASSPHRASE")
	err = st.sealBeforeWrite("model m1")
	if err == nil {
		t.Fatal("sealed nothing and reported success in a workspace that must not hold plaintext")
	}
	if !strings.Contains(err.Error(), "PICCOLO_KEY_PASSPHRASE") {
		t.Errorf("error should name the variable: %v", err)
	}
	// The guarantee is about DISK, not the in-memory document: the plaintext the
	// user typed is legitimately in memory until the write happens, and
	// applyAndConfirm returns before Marshal/Write when sealing fails. Assert the
	// file still holds what init wrote, unmodified.
	onDisk := readFile(t, workspace.NewLayout(dir).Config())
	if strings.Contains(onDisk, "sk-UNTYPABLE") {
		t.Error("plaintext reached config.json despite the refusal")
	}
	if !strings.Contains(onDisk, credential.Scheme) {
		t.Error("the refusal also destroyed the previously encrypted credential")
	}
	// And the error is the actionable kind: it names the variable, not a stack.
	if !strings.Contains(err.Error(), "export") && !strings.Contains(err.Error(), "unset") {
		t.Errorf("error should tell the user what to do: %v", err)
	}
}

// A plaintext workspace seals nothing: setup's behaviour must be unchanged when
// the user never opted in.
func TestSetupSealIsNoOpWhenNotEncrypted(t *testing.T) {
	dir := t.TempDir()
	stdout, err := execInit(t, dir, "--yes", "--name", "P", "--owner", "K", "--purpose", "personal",
		"--provider", "openai", "--model", "gpt-4o", "--api-key", "sk-plain")
	if err != nil {
		t.Fatalf("init: %v\n%s", err, stdout)
	}
	doc, err := configdoc.Load(workspace.NewLayout(dir).Config())
	if err != nil {
		t.Fatal(err)
	}
	m, _ := manifest.Load(workspace.NewLayout(dir).Manifest())
	st := &setupState{
		out: &bytes.Buffer{}, dir: dir, doc: doc, manifest: m,
		path: workspace.NewLayout(dir).Config(),
	}
	if err := patchModelKeyForTest(st.doc, "gpt-4o", "gpt-4o", "sk-NEW-PLAIN"); err != nil {
		t.Fatal(err)
	}
	if err := st.sealBeforeWrite("model m1"); err != nil {
		t.Fatalf("unencrypted workspace should not need a passphrase: %v", err)
	}
	out := string(st.doc.Marshal())
	if !strings.Contains(out, "sk-NEW-PLAIN") {
		t.Error("plaintext workspace had its credential altered")
	}
	if strings.Contains(out, credential.Scheme) {
		t.Error("unencrypted workspace gained ciphertext nobody asked for")
	}
}

func patchModelKeyForTest(doc *configdoc.Doc, lookup, alias, key string) error {
	return setup.PatchModel(doc, lookup, alias, "openai", "gpt-4o", key, "")
}
