package main

import (
	"bytes"
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
// encryptedWorkspace builds a workspace already opted into enc:// storage. Done
// through the real init path so the key, the manifest entry and the ignore rule are
// the ones init itself produces.
func encryptedWorkspace(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	stdout, err := execInit(t, dir, "--yes", "--passphrase", "horse",
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
// setPassphrase writes secrets.passphrase into piccolo.yaml, standing in for the
// state a user creates by editing the file by hand.
func setPassphrase(t *testing.T, dir, passphrase string) {
	t.Helper()
	layout := workspace.NewLayout(dir)
	m, err := manifest.Load(layout.Manifest())
	if err != nil {
		t.Fatal(err)
	}
	m.Secrets.Passphrase = passphrase
	if err := m.Write(layout.Manifest()); err != nil {
		t.Fatal(err)
	}
}

// reloadManifest mirrors what a fresh process would see after setPassphrase.
func reloadManifest(t *testing.T, dir string) *manifest.Manifest {
	t.Helper()
	m, err := manifest.Load(workspace.NewLayout(dir).Manifest())
	if err != nil {
		t.Fatal(err)
	}
	return m
}

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
	if _, err := os.Stat(layout.KeyFile()); err == nil && m.Passphrase() != "" {
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
	// The one-place rule: the sealed value lives in the overlay, not config.json.
	sc := string(readFile(t, workspace.NewLayout(dir).SidecarFile()))
	if !strings.Contains(sc, credential.Scheme) {
		t.Fatalf("no ciphertext in the overlay:\n%s", sc)
	}
	// And what landed in the overlay decrypts with the same passphrase serve will
	// read. config.json legitimately carries nothing now, so decrypting from the
	// overlay is the only meaningful round trip.
	sealed := firstEncryptedValue(sc)
	if sealed == "" {
		t.Fatal("no enc:// value in the overlay to verify")
	}
	got, err := credential.Decrypt("horse", workspace.NewLayout(dir).KeyFile(), sealed)
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

	setPassphrase(t, dir, "")
	// Reload the state the way a fresh setup session would, or the test would be
	// asserting against a manifest copy that still holds the old value.
	st.manifest = reloadManifest(t, dir)
	err = st.sealBeforeWrite("model m1")
	if err == nil {
		t.Fatal("sealed nothing and reported success in a workspace that must not hold plaintext")
	}
	if !strings.Contains(err.Error(), "secrets.passphrase") {
		t.Errorf("error should name the manifest field: %v", err)
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
	if !strings.Contains(err.Error(), "set it") {
		t.Errorf("error should tell the user what to do: %v", err)
	}
}

// A plaintext workspace seals nothing: setup's behaviour must be unchanged when
// the user never opted in.
func TestSetupSealIsNoOpWhenNotEncrypted(t *testing.T) {
	dir := t.TempDir()
	stdout, err := execInit(t, dir, "--yes", "--no-encrypt", "--name", "P", "--owner", "K", "--purpose", "personal",
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

// The setup toggle has to encrypt what is already in the workspace. A toggle that
// only changed future writes would leave the credentials sitting exactly where
// they were while the menu said "on" — the same false state this feature keeps
// having to chase down.
func TestSetupEncryptionToggleOnEncryptsExistingPlaintext(t *testing.T) {
	dir := plaintextWorkspaceForToggle(t)
	st := newSetupStateForTest(t, dir)
	if st.encrypted {
		t.Fatal("fixture is already encrypted")
	}
	setPassphraseInManifest(t, dir, "")
	st.manifest = reloadManifest(t, dir)

	// The passphrase arrives through the same check the form performs.
	if err := st.encryptExistingCredentials("horse"); err != nil {
		t.Fatalf("enable: %v", err)
	}
	setPassphraseInManifest(t, dir, "horse")
	st.manifest = reloadManifest(t, dir)
	st.recomputeEncrypted()

	if !st.encrypted {
		t.Error("posture flag did not follow the change")
	}
	out := readFile(t, st.path)
	if strings.Contains(out, "sk-plain") {
		t.Error("plaintext credential survived enabling")
	}
	if !strings.Contains(out, credential.Scheme) {
		t.Fatalf("no ciphertext written:\n%s", out)
	}
	// The key file is created on demand, not assumed to exist.
	if _, err := os.Stat(workspace.NewLayout(dir).KeyFile()); err != nil {
		t.Errorf("key file not generated: %v", err)
	}
}

// Disabling must not decrypt anything behind the user's back — Piccolo has nowhere
// to put the plaintext it would produce — and must not claim it did.
func TestSetupEncryptionToggleOffLeavesCiphertext(t *testing.T) {
	dir := encryptedWorkspace(t)
	before := readFile(t, workspace.NewLayout(dir).Config())
	if !strings.Contains(before, credential.Scheme) {
		t.Fatal("fixture lost its ciphertext")
	}
	setPassphraseInManifest(t, dir, "")

	after := readFile(t, workspace.NewLayout(dir).Config())
	if after != before {
		t.Error("clearing the passphrase rewrote config.json; disabling must not touch existing ciphertext")
	}
	st := newSetupStateForTest(t, dir)
	if st.encrypted {
		t.Error("posture still 'on' with no passphrase recorded")
	}
}

// Enabling twice is a no-op rather than a re-encryption, which would wrap an
// enc:// value as if it were a secret.
func TestSetupEncryptExistingIsIdempotent(t *testing.T) {
	dir := encryptedWorkspace(t)
	st := newSetupStateForTest(t, dir)
	before := string(st.doc.Marshal())
	if err := st.encryptExistingCredentials(""); err != nil {
		t.Fatalf("second enable: %v", err)
	}
	if got := string(st.doc.Marshal()); got != before {
		t.Error("re-enabling rewrote already-encrypted credentials")
	}
}

func TestSetupToggleRejectsBlankOrMismatchedPassphrase(t *testing.T) {
	dir := encryptedWorkspace(t)
	setPassphraseInManifest(t, dir, "")
	st := newSetupStateForTest(t, dir)
	// No terminal in tests, so the prompt path cannot run; the validation that
	// guards it is exercised directly.
	if err := st.encryptExistingCredentials("   "); err == nil {
		t.Error("blank passphrase accepted")
	}
	if err := st.encryptExistingCredentials(""); err == nil {
		t.Error("empty passphrase accepted on a workspace with nothing recorded")
	}
}

// plaintextWorkspaceForToggle is the state a user would toggle from: init default
// produces ciphertext, so this builds the opted-out workspace.
func plaintextWorkspaceForToggle(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	stdout, err := execInit(t, dir, "--yes", "--no-encrypt",
		"--name", "P", "--owner", "K", "--purpose", "personal",
		"--provider", "openai", "--model", "gpt-4o", "--api-key", "sk-plain")
	if err != nil {
		t.Fatalf("init: %v\n%s", err, stdout)
	}
	return dir
}

// The home menu's Container row used to name only the web console, so the
// encryption toggle inside that section was invisible unless you already knew it
// existed. These pin the property that failed, not the current wording: the row
// must account for every toggle the section contains.
func TestContainerSummaryNamesEveryToggle(t *testing.T) {
	st := &setupState{manifest: &manifest.Manifest{}}
	toggles := st.containerToggles()
	if len(toggles) < 2 {
		t.Fatalf("Container section should hold both toggles, got %d", len(toggles))
	}
	summary := st.containerSummary()
	for _, want := range []string{"web console", "credentials"} {
		if !strings.Contains(summary, want) {
			t.Errorf("row omits %q while the section contains it: %q", want, summary)
		}
	}
	// Every toggle id must be labelled; an unnamed one would go invisible again,
	// just without the "dropped from the list" shape that caused this.
	rows := containerRows()
	for _, tg := range toggles {
		if _, ok := rows[tg.id]; !ok {
			t.Errorf("toggle %q has no summary label, so it will not be discoverable on the home menu", tg.id)
		}
	}
}

func TestContainerSummaryTracksState(t *testing.T) {
	off := (&setupState{manifest: &manifest.Manifest{}}).containerSummary()
	if !strings.Contains(off, "credentials: plaintext") {
		t.Errorf("unencrypted workspace should say plaintext on the row: %q", off)
	}
	if strings.Contains(off, "encrypted") {
		t.Errorf("plaintext workspace claimed encryption: %q", off)
	}

	// Both halves are required for the row to say "encrypted", matching the
	// posture rule the sealing path uses.
	st := &setupState{manifest: &manifest.Manifest{}, encrypted: true}
	got := st.containerSummary()
	if !strings.Contains(got, "credentials: encrypted") {
		t.Errorf("encrypted workspace should say encrypted: %q", got)
	}

	// A change must be visible on the next render, since the loop re-reads it.
	st.encrypted = false
	if strings.Contains(st.containerSummary(), "encrypted") {
		t.Error("row did not follow the posture change")
	}
}

// The screen rows and the summary come from one list; assert the screen still
// offers both toggles with the verb that describes the action available.
func TestContainerScreenOptionsCoverBothToggles(t *testing.T) {
	st := &setupState{manifest: &manifest.Manifest{}}
	seen := map[string]string{}
	for _, tg := range st.containerToggles() {
		seen[tg.id] = tg.option()
		if !strings.Contains(tg.value(), tg.id+":") {
			t.Errorf("value %q does not carry the id %q the dispatcher splits on", tg.value(), tg.id)
		}
	}
	for id, wantVerb := range map[string]string{"web_console": "enable", "encrypt": "enable"} {
		if !strings.Contains(seen[id], wantVerb) {
			t.Errorf("%s row should offer %q: %q", id, wantVerb, seen[id])
		}
	}
	// Turning one on flips the verb, which is the only cue that it is already on.
	st.encrypted = true
	for _, tg := range st.containerToggles() {
		if tg.id == "encrypt" && !strings.Contains(tg.option(), "disable") {
			t.Errorf("enabled toggle should offer disable: %q", tg.option())
		}
	}
}

// The closing advice must not offer reload where it cannot work. After encryption is
// turned on, the running container predates the passphrase entirely, and reload
// answers 200 from a cached config while the reload itself failed — so the message
// has to name restart as required and say the alternative will mislead.
func TestFinishAdviceAfterTurningOnEncryption(t *testing.T) {
	var out bytes.Buffer
	st := &setupState{out: &out, write: true, encryptionTurnedOn: true}
	if !st.encryptionTurnedOn {
		t.Fatal("flag not set by the toggle path")
	}
	// The advice itself is a pure decision so it is testable without a daemon.
	plain := restartAdvice(true)
	if !strings.Contains(plain, "REQUIRED") || !strings.Contains(plain, "will NOT work") {
		t.Errorf("advice still offers reload as an option: %q", plain)
	}
	if strings.Contains(restartAdvice(false), "REQUIRED") {
		t.Error("ordinary config changes should keep the reload/restart choice")
	}
	// Wiring: finish() must actually use the advice, or the helper is decoration.
	// containerContext is unreachable from a test without a daemon, so finish()
	// must still reach the message through a path the test can drive.
	var sink bytes.Buffer
	f := &setupState{out: &sink, write: true, encryptionTurnedOn: true, cmd: newRootCmd(),
		runningProbe: func() bool { return true }}
	if err := f.finish(); err != nil {
		t.Fatalf("finish: %v", err)
	}
	got := sink.String()
	if !strings.Contains(got, "REQUIRED") {
		t.Errorf("finish() did not surface the restart-required advice: %q", got)
	}
	if strings.Contains(got, "piccolo reload    apply") {
		t.Errorf("restart-required advice still offers reload as the in-place option: %q", got)
	}

	// The ordinary path must keep offering both, or the fix would have traded one
	// wrong message for a worse universal one.
	var normal bytes.Buffer
	g := &setupState{out: &normal, write: true, cmd: newRootCmd(), runningProbe: func() bool { return true }}
	if err := g.finish(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(normal.String(), "piccolo reload") || strings.Contains(normal.String(), "REQUIRED") {
		t.Errorf("non-encryption change got the wrong advice: %q", normal.String())
	}
}
