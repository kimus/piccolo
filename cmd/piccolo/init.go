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
)

type initFlags struct {
	name      string
	owner     string
	purpose   string
	agents    string
	mode      string
	provider  string
	model     string
	modelAlia string
	apiKey    string
	apiBase   string
	web       bool
	shell     bool
	yes       bool
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
	cmd.Flags().StringVar(&f.mode, "mode", "", "terminal|web")
	cmd.Flags().StringVar(&f.provider, "provider", "", "model provider (e.g. openai)")
	cmd.Flags().StringVar(&f.model, "model", "", "model id (e.g. gpt-5.4)")
	cmd.Flags().StringVar(&f.modelAlia, "model-alias", "", "model_name alias in config.json")
	cmd.Flags().StringVar(&f.apiKey, "api-key", "", "model API key")
	cmd.Flags().StringVar(&f.apiBase, "api-base", "", "optional API base URL")
	cmd.Flags().BoolVar(&f.web, "web", true, "enable web search tools")
	cmd.Flags().BoolVar(&f.shell, "shell", false, "enable shell execution")
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
		Mode:          f.mode,
		Provider:      f.provider,
		Model:         f.model,
		ModelAlias:    f.modelAlia,
		APIKey:        f.apiKey,
		APIBase:       f.apiBase,
		WebSearch:     boolPtr(f.web),
		ShellExec:     boolPtr(f.shell),
		Workspace:     dirFlag,
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
		if pre.Mode == "" {
			pre.Mode = string(generate.ModeTerminal)
		}
		if pre.Model == "" {
			return fmt.Errorf("--model is required with --yes")
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
		inputs, err = inputsFromPrefill(pre)
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
	fmt.Fprintf(stdout, "\nWorkspace: %s\nImage:     %s\n", dir, inputs.Manifest().Container.Image)
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
	written, skipped, err := plan.Write(dir, overwrite)
	if err != nil {
		return err
	}
	if changed, err := generate.EnsureGitignore(dir); err != nil {
		return err
	} else if changed {
		written = append(written, ".gitignore")
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

func inputsFromPrefill(pre wizard.Prefill) (generate.Inputs, error) {
	in := generate.Inputs{
		AssistantName: pre.AssistantName,
		OwnerName:     pre.OwnerName,
		Purpose:       pre.Purpose,
		Specialists:   pre.Specialists,
		Mode:          generate.Mode(pre.Mode),
		Model: generate.ModelInput{
			Alias:    pre.ModelAlias,
			Provider: pre.Provider,
			Model:    pre.Model,
			APIKey:   pre.APIKey,
			APIBase:  pre.APIBase,
		},
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
