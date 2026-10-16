package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/kimus/piccolo/internal/configdoc"
	"github.com/kimus/piccolo/internal/credential"
	"github.com/kimus/piccolo/internal/generate"
	"github.com/kimus/piccolo/internal/manifest"
	"github.com/kimus/piccolo/internal/picoclaw"
	"github.com/kimus/piccolo/internal/setup"
	"github.com/kimus/piccolo/internal/workspace"
)

// EnvVarDefault aliases the manifest's suggestion so the secrets command and the
// init flag cannot offer different variable names.

func newSecretsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "secrets",
		Short: "Encrypt the credentials a Piccolo workspace stores",
		Long: strings.TrimSpace(`
piccolo secrets encrypt [flags]

PicoClaw accepts credentials as enc://<base64> and decrypts them at load, so a
workspace can stop holding API keys and bot tokens as plaintext.

WHAT THIS DOES: removes a readable API key from config.json AND from
picoclaw/.security.yml — the overlay PicoClaw loads INSTEAD of config.json for
any field it names, so sealing only config.json would leave the workspace
reporting itself encrypted while the gateway still read plaintext. Absent
overlay is left absent: it is never created here.

WHAT THIS DOES NOT DO: protect a copy of this workspace. The passphrase is stored
in piccolo.yaml, which is the one file here meant to be committed and shared, so
anyone holding the directory — a clone, a tarball, a backup — can decrypt it. For
"safe to copy", keep the passphrase outside the workspace instead.

It also does not hide anything from someone with host or Docker access: the
passphrase reaches the gateway through the container environment, visible to
'docker inspect', and a running gateway holds every decrypted credential in memory.

LOSING THE PASSPHRASE IS PERMANENT. There is no recovery path; the credentials have
to be re-entered. The pre-encryption backup config.json.bak keeps the plaintext
values and is not git-ignored, so it is not a safety net unless you keep it
deliberately.`),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	cmd.AddCommand(newSecretsEncryptCmd(), newSecretsCheckCmd())
	return rejectUnknownArgs(cmd)
}

func newSecretsEncryptCmd() *cobra.Command {
	var passphrase string
	var yes bool
	cmd := &cobra.Command{
		Use:   "encrypt",
		Short: "Generate a key if needed and rewrite config.json and .security.yml credentials as enc://",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSecretsEncrypt(cmd, passphrase, yes)
		},
	}
	f := cmd.Flags()
	f.StringVar(&passphrase, "passphrase", "", "passphrase to encrypt with; omit to be prompted (a flag value is visible to `ps` and in shell history, like the piccolo.yaml entry it becomes)")
	f.BoolVar(&yes, "yes", false, "do not ask for confirmation before rewriting config.json")
	return cmd
}

func newSecretsCheckCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "check",
		Short: "Report whether an encrypted workspace can actually be decrypted",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSecretsCheck(cmd)
		},
	}
	return cmd
}

func runSecretsEncrypt(cmd *cobra.Command, flagPassphrase string, yes bool) error {
	out := cmd.OutOrStdout()
	dir, err := workspace.Require(dirFlag)
	if err != nil {
		return err
	}
	layout := workspace.NewLayout(dir)

	m, err := manifest.Load(layout.Manifest())
	if err != nil {
		return err
	}
	doc, err := configdoc.Load(layout.Config())
	if err != nil {
		return fmt.Errorf("config.json does not parse: %w", err)
	}

	// Read the passphrase before generating anything: an interactive user should
	// not be asked to type a passphrase after a key file appeared for a run that
	// was going to stop anyway.
	// Resolve the passphrase BEFORE generating a key. A run that then refused to
	// supply one would otherwise leave a key file in a workspace whose credentials
	// are still plaintext — a half-configured state nothing later reports cleanly.
	src := "piccolo.yaml"
	passphrase := m.Passphrase()
	if flagPassphrase != "" {
		passphrase, src = flagPassphrase, "--passphrase flag"
	} else if passphrase == "" {
		var err error
		passphrase, err = promptPassphrase(out)
		if err != nil {
			return err
		}
		src = "typed at this terminal"
	}
	if strings.TrimSpace(passphrase) == "" {
		return errors.New("a blank passphrase would encrypt credentials nothing can later decrypt")
	}

	keyPath := layout.KeyFile()
	if _, err := os.Stat(keyPath); err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("inspect key file: %w", err)
		}
		generated, err := credential.Generate(layout.Keys())
		if err != nil {
			return err
		}
		keyPath = generated
		fmt.Fprintf(out, "generated credential key: %s\n", rel(dir, keyPath))
	}

	secrets := picoclaw.LoadSecrets(layout.Config())
	res, err := setup.EncryptWorkspace(doc, secrets, keyPath, passphrase, layout.SidecarFile())
	if err != nil {
		return err
	}

	// The warning is shown before anything is written, and the confirmation gates
	// the write: "warning precedes the rewrite" is a spec requirement, so printing
	// it after os.WriteFile would satisfy nothing.
	printEncryptionWarning(out, res, src, dir, keyPath)
	if !yes {
		if !term.IsTerminal(int(os.Stdin.Fd())) {
			return errors.New("no terminal for the confirmation prompt — review the warning above and re-run with --yes to proceed")
		}
		confirmed, err := confirmDestructive(out, cmd.InOrStdin(), "Encrypt these credentials and rewrite config.json?")
		if err != nil {
			return err
		}
		if !confirmed {
			fmt.Fprintln(out, "discarded; nothing was written.")
			return nil
		}
	}

	if res.NothingChanged() {
		// Still record the passphrase: a workspace with a key and nothing to
		// decrypt with is one that cannot boot once something IS encrypted.
		fmt.Fprintf(out, "%s\n", res.Summary())
	} else {
		// configdoc.Write keeps the file's own mode and leaves config.json.bak.
		if err := configdoc.Write(layout.Config(), doc); err != nil {
			return err
		}
		fmt.Fprintf(out, "wrote %s\n", rel(dir, layout.Config()))
	}

	if err := ensurePassphrase(m, passphrase, layout.Manifest(), out); err != nil {
		return err
	}
	if _, err := generate.EnsureGitignore(dir); err != nil {
		fmt.Fprintf(out, "warning: could not update .gitignore: %v\n", err)
	}

	fmt.Fprintf(out, "\n%s\n", res.Summary())
	for _, r := range res.Refused {
		fmt.Fprintf(out, "  left in plaintext: %s — %s\n", r.Field, r.Reason)
	}
	if len(res.Refused) > 0 {
		fmt.Fprintf(out, "  those fields are decided by picoclaw/%s; edit them there to protect them\n", picoclaw.SecurityFileName)
	}
	fmt.Fprint(out, "\nnext: piccolo serve\n")
	fmt.Fprint(out, "the passphrase is stored in piccolo.yaml, so nothing needs exporting — and that file is the one a workspace commit includes, so treat the directory as a secret.\n")
	fmt.Fprintf(out, "remember: %s holds the plaintext it replaced — delete it or keep it knowingly.\n", rel(dir, layout.Config()+".bak"))
	return nil
}

// printEncryptionWarning states the irreversible and asymmetric consequences
// before any write. It is a function so the ordering is one place and a future
// refactor cannot move it below the WriteFile call without the test noticing.
func printEncryptionWarning(out io.Writer, res setup.EncryptResult, src, dir, keyPath string) {
	fmt.Fprintln(out, "WARNING — this is not reversible without the passphrase.")
	fmt.Fprint(out, "  * The passphrase goes into piccolo.yaml. That file is committed on purpose, so a copy of\n"+
		"    this workspace can still be decrypted: what this removes is a readable key inside config.json.\n")
	fmt.Fprint(out, "  * A lost or edited-away passphrase cannot be recovered; every credential must be re-entered.\n")
	fmt.Fprintf(out, "  * The key file %s is now part of the secret: losing it loses the credentials too.\n", rel(dir, keyPath))
	fmt.Fprintf(out, "  * config.json.bak will keep the PLAINTEXT values this run replaces.\n")
	fmt.Fprintf(out, "  * The passphrase reaches the container through its environment, so 'docker inspect' shows it.\n")
	fmt.Fprintf(out, "    This protects a copied, backed-up, or committed workspace — not a host someone else controls.\n")
	if len(res.Refused) > 0 {
		fmt.Fprintf(out, "  * %d field(s) cannot be protected from config.json because picoclaw/%s governs them.\n", len(res.Refused), picoclaw.SecurityFileName)
	}
	fmt.Fprintf(out, "  * Passphrase source for this run: %s.\n", src)
	fmt.Fprintln(out)
}

// promptPassphrase asks for the passphrase twice. One prompt was not enough once
// the value became something Piccolo writes down: a typo would be committed into
// piccolo.yaml, and every credential sealed with it becomes permanently
// undecryptable with no recovery path and no way to tell what was typed.
// initPassphrase and this share the rule through confirmPassphrase.
func promptPassphrase(out io.Writer) (string, error) {
	if !stdinIsTerminal() {
		return "", errors.New("no passphrase to encrypt with and no terminal to ask for one — pass --passphrase, or run this interactively")
	}
	fmt.Fprint(out, "Choose a passphrase (it will be stored in piccolo.yaml): ")
	return confirmPassphrase(out)
}

// confirmPassphrase reads a passphrase and its confirmation, rejecting a blank or
// mismatched pair.
func confirmPassphrase(out io.Writer) (string, error) {
	first, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(out)
	if err != nil {
		return "", fmt.Errorf("read passphrase: %w", err)
	}
	fmt.Fprint(out, "Confirm it: ")
	second, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(out)
	if err != nil {
		return "", fmt.Errorf("read passphrase confirmation: %w", err)
	}
	if string(first) != string(second) {
		return "", errors.New("passphrases did not match — nothing was written")
	}
	if string(first) == "" {
		return "", errors.New("an empty passphrase would encrypt credentials nothing can later decrypt")
	}
	return string(first), nil
}

// ensurePassphrase records the passphrase in piccolo.yaml so serve can deliver it
// without the user exporting anything. An existing value is left alone rather than
// silently overwritten: replacing a passphrase here would undecrypt every credential
// already stored under the old one, which is a rotation and belongs in its own
// deliberate command.
func ensurePassphrase(m *manifest.Manifest, passphrase, path string, out io.Writer) error {
	if passphrase == "" {
		return nil
	}
	if current := m.Passphrase(); current != "" {
		if current != passphrase {
			fmt.Fprintln(out, "note: piccolo.yaml already holds a passphrase, left as set. Changing it does not re-encrypt anything — credentials stored under the old one would stop decrypting, so rotation is not offered here.")
		}
		return nil
	}
	m.Secrets.Passphrase = passphrase
	if err := m.Write(path); err != nil {
		return fmt.Errorf("write piccolo.yaml: %w", err)
	}
	fmt.Fprintf(out, "recorded secrets.passphrase in %s\n", rel(".", path))
	return nil
}

func runSecretsCheck(cmd *cobra.Command) error {
	out := cmd.OutOrStdout()
	dir, err := workspace.Require(dirFlag)
	if err != nil {
		return err
	}
	layout := workspace.NewLayout(dir)
	m, err := manifest.Load(layout.Manifest())
	if err != nil {
		return err
	}
	data, err := os.ReadFile(layout.Config())
	if err != nil {
		return err
	}
	doc, err := configdoc.Parse(data)
	if err != nil {
		return fmt.Errorf("config.json does not parse: %w", err)
	}
	var cfg picoclaw.Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return fmt.Errorf("config.json does not parse: %w", err)
	}

	// Counted from the document, not the typed config: channel_list is user-owned
	// and absent from picoclaw.Config, so a typed count would tell a workspace with
	// every token encrypted that it has nothing to protect.
	encountered := encryptedCredentialCount(doc)
	if encountered == 0 {
		fmt.Fprintln(out, "config.json holds no enc:// credential — this workspace stores plaintext (piccolo secrets encrypt changes that)")
		return nil
	}
	fmt.Fprintf(out, "config.json holds %d enc:// credential value(s)\n", encountered)

	ok := true
	passphrase := m.Passphrase()
	if passphrase == "" {
		fmt.Fprintln(out, "FAIL: piccolo.yaml has no secrets.passphrase, so nothing can deliver the passphrase")
		ok = false
	} else {
		v := passphrase
		keyPath := layout.KeyFile()
		if _, err := os.Stat(keyPath); err != nil {
			fmt.Fprintf(out, "FAIL: key file %s is missing; every enc:// value depends on its exact bytes\n", rel(dir, keyPath))
			ok = false
		} else if err := verifyOneDecrypts(v, keyPath, &cfg); err != nil {
			fmt.Fprintf(out, "FAIL: could not decrypt a credential with this passphrase and key: %v\n", err)
			ok = false
		} else {
			fmt.Fprintln(out, "OK: the passphrase and key file decrypt this workspace's credentials")
		}
	}

	// The mount the container needs already exists; say whether the key is inside
	// it, because a key outside picoclaw/ is unreachable from the gateway.
	if ok && !within(layout.PicoclawHome(), layout.KeyFile()) {
		_ = within
	}
	if !within(layout.PicoclawHome(), layout.KeyFile()) {
		fmt.Fprintln(out, "FAIL: the key path is outside picoclaw/, so the gateway cannot read it")
		ok = false
	}
	if !ok {
		return errors.New("workspace is not decryptable as configured")
	}
	fmt.Fprintf(out, "serve will inject PICOCLAW_KEY_PASSPHRASE from piccolo.yaml and PICOCLAW_SSH_KEY_PATH=%s\n", layout.ContainerKeyFile())
	return nil
}

// prepareEncryption generates the workspace key and installs the callback that
// renders config.json with ciphertext. Called after the plan is confirmed and
// before it is written, so an aborted init creates no key and a completed init
// writes no plaintext.
//
// The passphrase must exist before anything can be encrypted, which means init
// cannot defer it to first serve: an encrypted config.json with no way to be
// decrypted is a broken workspace, not a partially set up one.
func prepareEncryption(dir string, layout workspace.Layout, inputs *generate.Inputs, out io.Writer) error {
	if strings.TrimSpace(inputs.Passphrase) == "" {
		return errors.New("encryption requested with no passphrase to store")
	}

	keyPath := layout.KeyFile()
	if _, err := os.Stat(keyPath); err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("inspect key file: %w", err)
		}
		generated, err := credential.Generate(layout.Keys())
		if err != nil {
			return err
		}
		keyPath = generated
		fmt.Fprintf(out, "credential key: %s\n", rel(dir, keyPath))
	}

	passphrase := inputs.Passphrase

	pass, key := passphrase, keyPath
	inputs.Encrypt = func(plaintext string) (string, error) {
		return credential.Encrypt(pass, key, plaintext)
	}
	return nil
}

// initPassphrase collects the passphrase for a workspace being created: the flag
// value when given, else a prompt confirmed twice.
func initPassphrase(preset string, out io.Writer) (string, error) {
	if strings.TrimSpace(preset) != "" {
		return preset, nil
	}
	if !stdinIsTerminal() {
		return "", errors.New("encryption was requested with no --passphrase value and there is no terminal to ask for one — " +
			"pass --passphrase, or run 'piccolo secrets encrypt' later")
	}
	fmt.Fprint(out, "Choose a passphrase (it will be stored in piccolo.yaml): ")
	return confirmPassphrase(out)
}
