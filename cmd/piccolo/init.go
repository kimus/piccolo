package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/kimus/piccolo/internal/docker"
	"github.com/kimus/piccolo/internal/generate"
	"github.com/kimus/piccolo/internal/wizard"
	workspacepkg "github.com/kimus/piccolo/internal/workspace"
)

type initFlags struct {
	name      string
	owner     string
	purpose   string
	agents    string
	dashboard bool
	provider  string
	model     string
	modelAlia string
	apiKey    string
	apiBase   string
	web       bool
	shell     bool
	// noEncrypt turns off the default. Credentials are encrypted with PicoClaw's
	// enc:// format unless this is set, so the escape hatch has to exist and be
	// discoverable: a default-on behaviour with no way out is a trap for anyone
	// whose threat model does not need it or who cannot manage a passphrase.
	noEncrypt  bool
	passphrase string
	yes        bool
}

func newInitCmd() *cobra.Command {
	var f initFlags
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Create a Piccolo-managed PicoClaw workspace",
		Long: "Interactive wizard that generates piccolo.yaml, config.json, and agent " +
			"workspace files directly in the workspace directory (default: current directory).\n" +
			"Create-only: existing files are never overwritten without explicit confirmation.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runInit(cmd, f)
		},
	}
	cmd.Flags().StringVar(&f.name, "name", "", "assistant name")
	cmd.Flags().StringVar(&f.owner, "owner", "", "owner name")
	cmd.Flags().StringVar(&f.purpose, "purpose", "", "personal|research|coding|operations|blank")
	cmd.Flags().StringVar(&f.agents, "agents", "", "comma-separated specialists: research,planner,coder,ops")
	cmd.Flags().BoolVar(&f.dashboard, "dashboard", false, "expose the optional web console (browser dashboard on :18800)")
	cmd.Flags().StringVar(&f.provider, "provider", "", "model provider (e.g. openai)")
	cmd.Flags().StringVar(&f.model, "model", "", "model id (e.g. gpt-5.4)")
	cmd.Flags().StringVar(&f.modelAlia, "model-alias", "", "model_name alias in config.json")
	cmd.Flags().StringVar(&f.apiKey, "api-key", "", "model API key")
	cmd.Flags().StringVar(&f.apiBase, "api-base", "", "optional API base URL")
	cmd.Flags().BoolVar(&f.web, "web-search", true, "enable web search tools")
	cmd.Flags().BoolVar(&f.shell, "shell", false, "enable shell execution")
	// Credentials are encrypted by default; the flag exists to turn that off, not
	// to opt in. A default-on choice has to be exit-able or it is a trap.
	cmd.Flags().BoolVar(&f.noEncrypt, "no-encrypt", false, "store the API credential as plaintext in config.json instead of enc:// ciphertext")
	cmd.Flags().StringVar(&f.passphrase, "passphrase", "", "passphrase for the encrypted credential; required with --yes, otherwise prompted")
	cmd.Flags().BoolVar(&f.yes, "yes", false, "non-interactive: use flags/defaults, skip prompts, never overwrite")
	return cmd
}

func runInit(cmd *cobra.Command, f initFlags) error {
	stdout := cmd.OutOrStdout()
	stdin := cmd.InOrStdin()

	pre := wizard.Prefill{
		AssistantName: f.name,
		OwnerName:     f.owner,
		Purpose:       f.purpose,
		Provider:      f.provider,
		Model:         f.model,
		ModelAlias:    f.modelAlia,
		APIKey:        f.apiKey,
		APIBase:       f.apiBase,
		WebSearch:     boolPtr(f.web),
		ShellExec:     boolPtr(f.shell),
		EncryptKeys:   boolPtr(!f.noEncrypt),
		// Passphrase is not carried through Prefill: init prompts for it after the plan
		// is confirmed, because encryption cannot happen before the key exists.
		Workspace: dirFlag,
	}
	// Only pin the web-console choice when the flag was set explicitly; otherwise
	// leave it nil so the interactive wizard asks (and --yes defaults to false).
	if cmd.Flags().Changed("dashboard") {
		pre.Dashboard = boolPtr(f.dashboard)
	}
	if f.agents != "" {
		for _, a := range strings.Split(f.agents, ",") {
			if a = strings.TrimSpace(a); a != "" {
				pre.Specialists = append(pre.Specialists, a)
			}
		}
	}
	if f.yes {
		// Non-interactive: fill defaults for anything not provided.
		if pre.AssistantName == "" {
			pre.AssistantName = "Piccolo"
		}
		if pre.Purpose == "" {
			pre.Purpose = "personal"
		}
		if pre.Dashboard == nil {
			pre.Dashboard = boolPtr(false)
		}
		if pre.Model == "" {
			return fmt.Errorf("--model is required with --yes")
		}
		// Encryption is on by default, so --yes needs the passphrase from the one
		// place a non-interactive run can get it. Generated silently instead would
		// hand the user a secret they never chose and cannot distinguish from a
		// mistake; refused here means the remedy is in the error text.
		if pre.EncryptKeys != nil && *pre.EncryptKeys && strings.TrimSpace(f.passphrase) == "" {
			return fmt.Errorf("--yes stores the credential as enc:// by default and needs --passphrase VALUE to do it with — pass one, or use --no-encrypt for plaintext in config.json")
		}
		if pre.ModelAlias == "" {
			pre.ModelAlias = pre.Model
		}
		if pre.Workspace == "" {
			pre.Workspace = "."
		}
	}

	// Warn (not fail) when Docker is unreachable.
	runner := docker.New()
	if !runner.Available() {
		fmt.Fprintln(stdout, "[WARN] docker daemon not reachable — you can still init, but serve will fail")
	}

	var (
		inputs    generate.Inputs
		workspace string
		err       error
	)
	if f.yes {
		inputs, err = inputsFromPrefill(pre, f.passphrase)
		if err != nil {
			return err
		}
		workspace = pre.Workspace
	} else {
		// The huh form needs a real terminal (a zero-size pty even panics
		// inside upstream bubbles). Fail with guidance instead.
		if !term.IsTerminal(int(os.Stdin.Fd())) {
			return fmt.Errorf("interactive init requires a terminal — use --yes with flags for non-interactive setup (see --help)")
		}
		inputs, workspace, err = wizard.Run(pre)
		if err != nil {
			return err
		}
	}

	dir, err := filepath.Abs(workspace)
	if err != nil {
		return err
	}

	plan, err := generate.BuildPlan(inputs)
	if err != nil {
		return err
	}

	// Preview the generation plan.
	console := "off"
	if inputs.Dashboard {
		console = "on"
	}
	fmt.Fprintf(stdout, "\nWorkspace:   %s\nImage:       %s\nWeb console: %s\n", dir, inputs.Manifest().Container.Image, console)
	fmt.Fprintln(stdout, "Files to create:")
	for _, file := range plan.Files {
		fmt.Fprintf(stdout, "  %s\n", file.Rel)
	}

	// Create-only: decide overwrites explicitly. A single bufio.Reader must
	// serve every prompt: a second reader on the same stdin would miss
	// type-ahead the first one buffered (Proceed? would then default to yes).
	reader := bufio.NewReader(stdin)
	overwrite := map[string]bool{}
	existing := plan.Existing(dir)
	if len(existing) > 0 {
		fmt.Fprintln(stdout, "\nAlready present:")
		for _, rel := range existing {
			fmt.Fprintf(stdout, "  %s\n", rel)
		}
		if f.yes {
			fmt.Fprintln(stdout, "\n--yes: existing files will be kept")
		} else {
			for _, rel := range existing {
				fmt.Fprintf(stdout, "Overwrite %s? [y/N] ", rel)
				line, _ := reader.ReadString('\n')
				if strings.EqualFold(strings.TrimSpace(line), "y") {
					overwrite[rel] = true
				}
			}
		}
	}

	if !f.yes {
		fmt.Fprint(stdout, "\nProceed? [Y/n] ")
		line, err := reader.ReadString('\n')
		if err != nil && line == "" {
			// No confirmation available (EOF without input): do not proceed.
			fmt.Fprintln(stdout, "\nNo confirmation — aborted, nothing written.")
			return nil
		}
		if answer := strings.ToLower(strings.TrimSpace(line)); answer != "" && answer != "y" && answer != "yes" {
			fmt.Fprintln(stdout, "Aborted — nothing written.")
			return nil
		}
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	layout := workspacepkg.NewLayout(dir)
	if err := layout.EnsureDirs(); err != nil {
		return err
	}

	// The key is generated only after the plan was previewed and confirmed, and the
	// plan is then rebuilt so config.json is rendered with the ciphertext already
	// in it. The ordering carries two guarantees:
	//
	//   * nothing is written before "Proceed?", so init keeps its create-only
	//     contract and an abort leaves no key file and no secrets block behind;
	//   * the plaintext credential never occupies a written byte, because the file
	//     that reaches disk was rendered from the encrypted value.
	//
	// BuildPlan is pure, so running it twice is safe and deterministic.
	if inputs.EncryptKeys {
		if inputs.Passphrase == "" {
			p, err := initPassphrase(f.passphrase, stdout)
			if err != nil {
				return err
			}
			inputs.Passphrase = p
		}
		if err := prepareEncryption(dir, layout, &inputs, stdout); err != nil {
			return err
		}
		plan, err = generate.BuildPlan(inputs)
		if err != nil {
			return err
		}
	}

	written, skipped, err := plan.Write(dir, overwrite)
	if err != nil {
		return err
	}
	if changed, err := generate.EnsureGitignore(dir); err != nil {
		return err
	} else if changed {
		written = append(written, ".gitignore")
	}
	// Record what this run actually wrote, so a later 'piccolo doctor' can tell a
	// hand-edited copy from an untouched one that predates a template fix.
	if err := plan.RecordProvenance(dir, written); err != nil {
		return err
	}

	for _, rel := range written {
		fmt.Fprintf(stdout, "created %s\n", rel)
	}
	for _, rel := range skipped {
		fmt.Fprintf(stdout, "kept    %s\n", rel)
	}
	fmt.Fprintf(stdout, "\nNext: piccolo doctor%s && piccolo serve%s\n", dirHint(dir), dirHint(dir))
	return nil
}

func dirHint(dir string) string {
	cwd, err := os.Getwd()
	if err == nil && dir == cwd {
		return ""
	}
	return fmt.Sprintf(" -D %s", dir)
}

func inputsFromPrefill(pre wizard.Prefill, passphrase string) (generate.Inputs, error) {
	in := generate.Inputs{
		AssistantName: pre.AssistantName,
		OwnerName:     pre.OwnerName,
		Purpose:       pre.Purpose,
		Specialists:   pre.Specialists,
		// The opt-in has to be carried here as well as in the wizard path: --yes
		// never enters wizard.Run, and a flag that silently did nothing would leave
		// the user with a plaintext workspace they believed was encrypted. Mirrors of
		// the same field on two paths are exactly the drift this repo keeps hitting.
		EncryptKeys: pre.EncryptKeys != nil && *pre.EncryptKeys,
		Passphrase:  strings.TrimSpace(passphrase),
		Model: generate.ModelInput{
			Alias:    pre.ModelAlias,
			Provider: pre.Provider,
			Model:    pre.Model,
			APIKey:   pre.APIKey,
			APIBase:  pre.APIBase,
		},
	}
	if pre.Dashboard != nil {
		in.Dashboard = *pre.Dashboard
	}
	if pre.WebSearch != nil {
		in.WebSearch = *pre.WebSearch
	}
	if pre.ShellExec != nil {
		in.ShellExec = *pre.ShellExec
	}
	if err := in.Validate(); err != nil {
		return generate.Inputs{}, err
	}
	return in, nil
}

func boolPtr(b bool) *bool { return &b }
