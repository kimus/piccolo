package main

import (
	"encoding/json"
	"errors"
	"fmt"
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

// EnvVarDefault is the host variable Piccolo suggests when a workspace opts in.
// It is a suggestion the user can override with --passphrase-env, not something
// Piccolo reads back in: the manifest records whatever name was chosen.
const EnvVarDefault = "PICCOLO_KEY_PASSPHRASE"

func newSecretsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "secrets",
		Short: "Encrypt the credentials a Piccolo workspace stores",
		Long: strings.TrimSpace(`
piccolo secrets encrypt [flags]

PicoClaw accepts credentials as enc://<base64> and decrypts them at load, so a
workspace can stop holding API keys and bot tokens as plaintext.

WHAT THIS PROTECTS: credentials at rest — a copied or backed-up workspace, a
"git add -A" that missed the ignore rule, a config.json pasted into a bug report.

WHAT THIS DOES NOT HIDE: anyone with access to this host or to Docker. The
passphrase reaches the gateway through the container environment, so it is visible
in 'docker inspect', and a running gateway holds every decrypted credential in
memory and on its mounted filesystem.

LOSING THE PASSPHRASE IS PERMANENT. There is no recovery path: the credentials
have to be re-entered. The pre-encryption backup config.json.bak keeps the
plaintext values, so it is not a safety net unless you keep it deliberately.

The passphrase is never written to a file by Piccolo. piccolo.yaml records only
the NAME of the host environment variable that holds it.`),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	cmd.AddCommand(newSecretsEncryptCmd(), newSecretsCheckCmd())
	return rejectUnknownArgs(cmd)
}

func newSecretsEncryptCmd() *cobra.Command {
	var passphraseEnv string
	var yes bool
	cmd := &cobra.Command{
		Use:   "encrypt",
		Short: "Generate a key if needed and rewrite config.json credentials as enc://",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSecretsEncrypt(cmd, passphraseEnv, yes)
		},
	}
	f := cmd.Flags()
	f.StringVar(&passphraseEnv, "passphrase-env", EnvVarDefault, "NAME of the host variable holding the encryption passphrase (not the passphrase)")
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

func runSecretsEncrypt(cmd *cobra.Command, passphraseEnv string, yes bool) error {
	out := cmd.OutOrStdout()
	dir, err := workspace.Require(dirFlag)
	if err != nil {
		return err
	}
	layout := workspace.NewLayout(dir)

	if !manifest.IsEnvVarName(strings.TrimSpace(passphraseEnv)) {
		return fmt.Errorf("--passphrase-env %q must be the NAME of an uppercase host environment variable, not a passphrase", passphraseEnv)
	}
	passphraseEnv = strings.TrimSpace(passphraseEnv)

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
	passphrase, src, err := readPassphrase(passphraseEnv, out)
	if err != nil {
		return err
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
	res, err := setup.EncryptWorkspace(doc, secrets, keyPath, passphrase)
	if err != nil {
		return err
	}

	// The warning is shown before anything is written, and the confirmation gates
	// the write: "warning precedes the rewrite" is a spec requirement, so printing
	// it after os.WriteFile would satisfy nothing.
	printEncryptionWarning(out, res, src, passphraseEnv, dir, keyPath)
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
		// Still record the delivery route: a workspace with a key and no
		// passphrase_env is one that cannot boot once something IS encrypted.
		fmt.Fprintf(out, "%s\n", res.Summary())
	} else {
		// configdoc.Write keeps the file's own mode and leaves config.json.bak.
		if err := configdoc.Write(layout.Config(), doc); err != nil {
			return err
		}
		fmt.Fprintf(out, "wrote %s\n", rel(dir, layout.Config()))
	}

	if err := ensurePassphraseEnv(m, passphraseEnv, layout.Manifest(), out); err != nil {
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
	fmt.Fprintf(out, "\nnext: export the passphrase in the environment that runs piccolo serve\n")
	fmt.Fprintf(out, "  export %s='the passphrase you just entered'\n", passphraseEnv)
	fmt.Fprintf(out, "then 'piccolo serve'. It will not start without that variable, "+
		"and 'piccolo secrets check' verifies the workspace end to end.\n")
	fmt.Fprintf(out, "remember: %s holds the plaintext it replaced — delete it or keep it knowingly.\n", rel(dir, layout.Config()+".bak"))
	return nil
}

// printEncryptionWarning states the irreversible and asymmetric consequences
// before any write. It is a function so the ordering is one place and a future
// refactor cannot move it below the WriteFile call without the test noticing.
func printEncryptionWarning(out interface{ Write([]byte) (int, error) }, res setup.EncryptResult, src, passphraseEnv, dir, keyPath string) {
	fmt.Fprintln(out, "WARNING — this is not reversible without the passphrase.")
	fmt.Fprintf(out, "  * A lost %s cannot be recovered; every enc:// credential then has to be re-entered.\n", passphraseEnv)
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

// readPassphrase takes the passphrase from the named host variable when it is
// set, else from the terminal. Never from a flag: a command-line passphrase is
// visible in shell history and to any process listing, which is the exposure
// this feature exists to reduce.
func readPassphrase(envVar string, out interface{ Write([]byte) (int, error) }) (value, source string, err error) {
	if v, ok := os.LookupEnv(envVar); ok && v != "" {
		return v, "host variable " + envVar, nil
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return "", "", fmt.Errorf("host variable %s is not set and there is no terminal to ask for it — export %s or run this command interactively", envVar, envVar)
	}
	fmt.Fprintf(out, "Enter the passphrase for %s (it will not be stored by Piccolo): ", envVar)
	b, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", "", fmt.Errorf("read passphrase: %w", err)
	}
	v := strings.TrimSpace(string(b))
	if v == "" {
		return "", "", fmt.Errorf("an empty passphrase would encrypt nothing that the gateway could later decrypt")
	}
	return v, "typed at this terminal", nil
}

// ensurePassphraseEnv records the variable name in piccolo.yaml, leaving an
// existing different name alone rather than silently re-routing a workspace that
// already works.
func ensurePassphraseEnv(m *manifest.Manifest, envVar, path string, out interface{ Write([]byte) (int, error) }) error {
	current := m.PassphraseVar()
	switch {
	case current == envVar:
		return nil
	case current != "":
		fmt.Fprintf(out, "note: piccolo.yaml already names %s; leaving it as set. Edit secrets.passphrase_env to change the route.\n", current)
		return nil
	}
	m.Secrets.PassphraseEnv = envVar
	if err := m.Write(path); err != nil {
		return fmt.Errorf("write piccolo.yaml: %w", err)
	}
	fmt.Fprintf(out, "recorded secrets.passphrase_env: %s in %s\n", envVar, rel(".", path))
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
	envVar := m.PassphraseVar()
	if encountered == 0 {
		fmt.Fprintln(out, "config.json holds no enc:// credential — this workspace stores plaintext (piccolo secrets encrypt changes that)")
		return nil
	}
	fmt.Fprintf(out, "config.json holds %d enc:// credential value(s)\n", encountered)

	ok := true
	if envVar == "" {
		fmt.Fprintf(out, "FAIL: piccolo.yaml has no secrets.passphrase_env, so nothing can deliver the passphrase\n")
		ok = false
	} else if v, found := os.LookupEnv(envVar); !found || v == "" {
		fmt.Fprintf(out, "FAIL: host variable %s is unset or empty\n", envVar)
		ok = false
	} else {
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
	if !within(layout.PicoclawHome(), layout.KeyFile()) {
		fmt.Fprintf(out, "FAIL: the key path is outside picoclaw/, so the gateway cannot read it\n")
		ok = false
	}
	if !ok {
		return errors.New("workspace is not decryptable as configured")
	}
	fmt.Fprintf(out, "serve will inject PICOCLAW_KEY_PASSPHRASE from %s and PICOCLAW_SSH_KEY_PATH=%s\n", envVar, layout.ContainerKeyFile())
	return nil
}
