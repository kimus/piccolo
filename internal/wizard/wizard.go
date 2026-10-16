// Package wizard collects setup answers interactively using charmbracelet/huh.
// Wizard logic is deliberately separate from file generation (internal/generate)
// so the prompts can be upgraded to a full Bubble Tea flow later.
package wizard

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/huh"

	"github.com/kimus/piccolo/internal/generate"
	"github.com/kimus/piccolo/internal/manifest"
)

// KeyMap is the one keybinding definition for every interactive Piccolo screen:
// the init wizard, `piccolo setup`, and integration setup.
//
// It exists because those screens drifted apart. `piccolo setup` bound esc to
// back/quit while the integration forms kept the library default of ctrl+c
// alone, so the key people press to leave a field did nothing on exactly the
// screen where they were typing a credential -- and the same key meant something
// else one screen earlier. esc now aborts a form everywhere, which the setup
// flow already turns into "back to the menu, or out if you were at the menu".
//
// The binding is safe against the library's own uses of esc: the file picker's
// Close binding ships disabled, so the form-level abort never steals a key a
// field still needs.
func KeyMap() *huh.KeyMap {
	km := huh.NewDefaultKeyMap()
	km.Quit = key.NewBinding(key.WithKeys("ctrl+c", "esc"), key.WithHelp("esc", "back/quit"))
	return km
}

// Form builds an interactive form with the shared KeyMap. Piccolo screens should
// be constructed through here rather than huh.NewForm directly, so a key never
// means two things on two consecutive screens.
func Form(groups ...*huh.Group) *huh.Form {
	return huh.NewForm(groups...).WithKeyMap(KeyMap())
}

// Prefill carries non-interactive flag values into the wizard; empty fields
// are asked interactively.
type Prefill struct {
	AssistantName string
	OwnerName     string
	Purpose       string
	Specialists   []string
	// Dashboard is the optional web-console choice; nil means "ask".
	Dashboard  *bool
	Provider   string
	Model      string
	ModelAlias string
	APIKey     string
	APIBase    string
	WebSearch  *bool
	ShellExec  *bool
	// EncryptKeys is the credential-storage choice; nil means "ask".
	EncryptKeys   *bool
	PassphraseEnv string
	Workspace     string
}

// Run presents the wizard and returns validated generation inputs plus the
// chosen workspace path (relative or absolute, as entered).
// passphraseVar is the host variable name a workspace will read the passphrase
// from. Flag-provided when given, else the default. It is a NAME: the passphrase
// itself is never a value Piccolo stores or passes on a command line.
func passphraseVar(pre Prefill) string {
	if v := strings.TrimSpace(pre.PassphraseEnv); v != "" {
		return v
	}
	return manifest.DefaultPassphraseEnv
}

func Run(pre Prefill) (generate.Inputs, string, error) {
	in := generate.Inputs{
		AssistantName: pre.AssistantName,
		OwnerName:     pre.OwnerName,
		Purpose:       pre.Purpose,
		Specialists:   pre.Specialists,
		EncryptKeys:   pre.EncryptKeys != nil && *pre.EncryptKeys,
		PassphraseEnv: passphraseVar(pre),
		Model: generate.ModelInput{
			Alias:    pre.ModelAlias,
			Provider: pre.Provider,
			Model:    pre.Model,
			APIKey:   pre.APIKey,
			APIBase:  pre.APIBase,
		},
	}
	workspace := pre.Workspace

	dash := false
	if pre.Dashboard != nil {
		dash = *pre.Dashboard
	}

	web := true
	if pre.WebSearch != nil {
		web = *pre.WebSearch
	}
	sh := false
	if pre.ShellExec != nil {
		sh = *pre.ShellExec
	}

	var groups []*huh.Group

	if in.AssistantName == "" {
		groups = append(groups, huh.NewGroup(
			huh.NewInput().Title("Assistant name").Placeholder("Piccolo").Value(&in.AssistantName),
		))
	}
	if in.OwnerName == "" {
		groups = append(groups, huh.NewGroup(
			huh.NewInput().Title("Your name").Value(&in.OwnerName),
		))
	}
	if in.Purpose == "" {
		groups = append(groups, huh.NewGroup(
			huh.NewSelect[string]().
				Title("Primary purpose").
				Options(
					huh.NewOption("Personal assistant", "personal"),
					huh.NewOption("Research assistant", "research"),
					huh.NewOption("Coding/project assistant", "coding"),
					huh.NewOption("Operations assistant", "operations"),
					huh.NewOption("Blank/custom", "blank"),
				).
				Value(&in.Purpose),
		))
	}
	if in.Specialists == nil {
		groups = append(groups, huh.NewGroup(
			huh.NewMultiSelect[string]().
				Title("Specialist agents to create").
				Options(
					huh.NewOption("research", "research"),
					huh.NewOption("planner", "planner"),
					huh.NewOption("coder", "coder"),
					huh.NewOption("ops", "ops"),
				).
				Value(&in.Specialists),
		))
	}
	if pre.Dashboard == nil {
		groups = append(groups, huh.NewGroup(
			huh.NewConfirm().
				Title("Expose the optional web console?").
				Description("The gateway always runs; this adds the browser dashboard at http://127.0.0.1:18800").
				Value(&dash),
		))
	}
	if in.Model.Provider == "" {
		groups = append(groups, huh.NewGroup(
			huh.NewInput().Title("Model provider").Placeholder("openai").Value(&in.Model.Provider),
		))
	}
	if in.Model.Model == "" {
		groups = append(groups, huh.NewGroup(
			huh.NewInput().Title("Model id").Placeholder("gpt-5.4").Value(&in.Model.Model),
		))
	}
	if in.Model.Alias == "" {
		in.Model.Alias = in.Model.Model
		groups = append(groups, huh.NewGroup(
			huh.NewInput().Title("Model alias (model_name in config.json)").
				Placeholder(in.Model.Model).
				Value(&in.Model.Alias),
		))
	}
	if in.Model.APIKey == "" {
		groups = append(groups, huh.NewGroup(
			huh.NewInput().Title("API key").EchoMode(huh.EchoModePassword).
				Description("Leave empty to fill in later (doctor will warn)").
				Value(&in.Model.APIKey),
		))
	}
	if in.Model.APIBase == "" {
		groups = append(groups, huh.NewGroup(
			huh.NewInput().Title("API base URL (optional)").Value(&in.Model.APIBase),
		))
	}
	// The credential-storage choice is asked right after the credential itself, so
	// the user sees the consequence while the value they typed is still the topic.
	// Nothing is encrypted at this point: this records the intent, and init
	// generates the key after the plan is confirmed.
	if pre.EncryptKeys == nil && in.Model.APIKey != "" {
		enc := false
		if pre.EncryptKeys != nil {
			enc = *pre.EncryptKeys
		}
		groups = append(groups, huh.NewGroup(
			huh.NewConfirm().
				Title("Encrypt credentials stored in this workspace?").
				Description("Stores the API key as PicoClaw enc:// ciphertext instead of plaintext.\n\n"+
					"Protects: a copied, backed-up, or accidentally committed workspace.\n"+
					"Does not hide: anything from someone with host or Docker access — the passphrase\n"+
					"  reaches the gateway through the container environment, visible to 'docker inspect'.\n\n"+
					"Losing the passphrase is permanent: the credentials must be re-entered. It is never\n"+
					"stored by piccolo; the workspace records only the NAME of the host variable that\n"+
					"holds it ("+passphraseVar(pre)+").").
				Affirmative("Yes, encrypt").
				Negative("No, plaintext").
				Value(&enc),
		))
		if enc {
			in.EncryptKeys = true
			in.PassphraseEnv = passphraseVar(pre)
		}
	} else if pre.EncryptKeys != nil {
		in.EncryptKeys = *pre.EncryptKeys
		if in.EncryptKeys {
			in.PassphraseEnv = passphraseVar(pre)
		}
	}

	if pre.WebSearch == nil {
		groups = append(groups, huh.NewGroup(
			huh.NewConfirm().Title("Enable web search tools?").Value(&web),
		))
	}
	if pre.ShellExec == nil {
		groups = append(groups, huh.NewGroup(
			huh.NewConfirm().Title("Enable shell execution?").
				Description("Default: no — recommended").
				Value(&sh),
		))
	}
	if workspace == "" {
		workspace = "."
		groups = append(groups, huh.NewGroup(
			huh.NewInput().Title("Workspace path").
				Description("Where piccolo.yaml, picoclaw/, integrations/, and runtime/ are created").
				Placeholder(".").
				Value(&workspace),
		))
	}

	if len(groups) > 0 {
		form := Form(groups...)
		if err := form.Run(); err != nil {
			return generate.Inputs{}, "", fmt.Errorf("wizard cancelled: %w", err)
		}
	}

	in.Dashboard = dash
	in.WebSearch = web
	in.ShellExec = sh
	if strings.TrimSpace(in.Model.Alias) == "" {
		in.Model.Alias = in.Model.Model
	}
	if strings.TrimSpace(workspace) == "" {
		workspace = "."
	}
	if err := in.Validate(); err != nil {
		return generate.Inputs{}, "", err
	}
	return in, workspace, nil
}
