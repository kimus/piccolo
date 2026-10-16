package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/kimus/piccolo/internal/configdoc"
	"github.com/kimus/piccolo/internal/credential"
	"github.com/kimus/piccolo/internal/docker"
	"github.com/kimus/piccolo/internal/generate"
	"github.com/kimus/piccolo/internal/manifest"
	"github.com/kimus/piccolo/internal/picoclaw"
	"github.com/kimus/piccolo/internal/runtimeimage"
	"github.com/kimus/piccolo/internal/setup"
	"github.com/kimus/piccolo/internal/wizard"
	"github.com/kimus/piccolo/internal/workspace"
	workspacepkg "github.com/kimus/piccolo/internal/workspace"
)

func newSetupCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "setup [section] [name]",
		Short: "Edit an existing workspace (channels, models, tools, container)",
		Long: "Interactive counterpart of piccolo init: an arrow-navigable editor for " +
			"config.json settings. Open 'piccolo setup' for the home menu, or deep-link: " +
			"'piccolo setup channels', 'piccolo setup channels discord', " +
			"'piccolo setup models', 'piccolo setup tools', 'piccolo setup container'.\n\n" +
			"Credential encryption is on by default and toggled under 'piccolo setup container'.\n\n" +
			"Every write is a merge-patch behind a diff preview + confirmation: unknown " +
			"keys and untouched subtrees of your user-owned config.json are preserved, " +
			"and the previous file is kept as config.json.bak.",
		Args: cobra.MaximumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !term.IsTerminal(int(os.Stdin.Fd())) {
				return fmt.Errorf("piccolo setup needs an interactive terminal — edit config.json / piccolo.yaml directly")
			}
			return runSetup(cmd, args)
		},
	}
	return cmd
}

// setupForm is `piccolo setup`'s screen constructor. The keymap comes from the
// wizard package so it cannot drift from the other interactive Piccolo surfaces.
func setupForm(groups ...*huh.Group) *huh.Form {
	return wizard.Form(groups...).WithProgramOptions(tea.WithAltScreen())
}

type setupState struct {
	cmd *cobra.Command
	// out is an io.Writer rather than *os.File so the write paths (applyAndConfirm,
	// sealBeforeWrite) are exercisable in tests. Every production call site passes
	// os.Stdout, which satisfies this.
	out   io.Writer
	dir   string
	path  string // config.json
	doc   *configdoc.Doc
	orig  []byte // config.json as loaded (for diff + bak)
	write bool   // any confirmed write happened

	manifestPath string
	manifest     *manifest.Manifest

	// secrets is the workspace's picoclaw/.security.yml. PicoClaw merges that file
	// over config.json on load and it WINS for the fields it carries, so it decides
	// whether a key Piccolo is about to write will be read at all. Read once at
	// startup: Piccolo never writes the sidecar, so nothing here changes it.
	secrets picoclaw.Secrets

	// encrypted is the workspace's credential posture: a key file exists AND
	// piccolo.yaml names where the passphrase comes from. Both, because either half
	// alone describes a workspace that cannot boot, and because a field's help text
	// must say where the value will actually go.
	encrypted bool

	// notice carries a message into the next screen's help line. Printing while a
	// form is on screen is invisible: huh runs in the alt screen, so text written
	// between two screens is covered by the next frame before it can be read —
	// which is exactly when a refusal reason matters most.
	notice string

	pendingDiscordApplicationID string
	pendingDiscordMentionOnly   bool
}

func runSetup(cmd *cobra.Command, args []string) error {
	dir, err := workspace.Require(dirFlag)
	if err != nil {
		return err
	}
	layout := workspace.NewLayout(dir)
	path := layout.Config()
	doc, err := configdoc.Load(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("no config.json in %s — run 'piccolo init' first", dir)
		}
		return fmt.Errorf("config.json does not parse (fix it manually first): %w", err)
	}
	manifestPath := filepath.Join(dir, "piccolo.yaml")
	mf, err := manifest.Load(manifestPath)
	if err != nil {
		return fmt.Errorf("piccolo.yaml does not parse (fix it manually first): %w", err)
	}
	orig, _ := os.ReadFile(path)
	st := &setupState{cmd: cmd, out: os.Stdout, dir: dir, path: path, doc: doc, orig: orig, manifestPath: manifestPath, manifest: mf, secrets: picoclaw.LoadSecrets(path)}
	// Checked once: nothing inside a setup session generates a key or changes the
	// manifest's passphrase route.
	if _, err := os.Stat(workspace.NewLayout(dir).KeyFile()); err == nil && mf.Passphrase() != "" {
		st.encrypted = true
	}

	// Deep links.
	if len(args) >= 1 {
		switch args[0] {
		case "channels":
			name := ""
			if len(args) == 2 {
				name = strings.ToLower(args[1])
			}
			if err := st.channelsScreen(name); err != nil {
				return err
			}
			return st.finish()
		case "models":
			if err := st.modelsScreen(); err != nil {
				return err
			}
			return st.finish()
		case "tools":
			if err := st.toolsScreen(); err != nil {
				return err
			}
			return st.finish()
		case "container":
			if err := st.containerScreen(); err != nil {
				return err
			}
			return st.finish()
		case "agents", "identity":
			return fmt.Errorf("%q is not editable yet — edit %s / piccolo.yaml manually", args[0], "config.json")
		default:
			return fmt.Errorf("unknown setup section %q (want: channels, models, tools, container)", args[0])
		}
	}

	// Home menu loop.
	for {
		choice, err := st.homeMenu()
		if err != nil {
			if isCancel(err) {
				return st.finish()
			}
			return err
		}
		if choice == "__quit__" {
			return st.finish()
		}
		var screenErr error
		switch choice {
		case "channels":
			screenErr = st.channelsScreen("")
		case "models":
			screenErr = st.modelsScreen()
		case "tools":
			screenErr = st.toolsScreen()
		case "container":
			screenErr = st.containerScreen()
		}
		if screenErr != nil {
			if isCancel(screenErr) {
				continue
			}
			return screenErr
		}
	}
}

func (st *setupState) finish() error {
	if !st.write {
		fmt.Fprintln(st.out, "no changes written.")
		return nil
	}
	fmt.Fprintf(st.out, "\n\u2713 config.json updated (previous copy: config.json.bak); piccolo.yaml setup metadata may also be updated\n")
	if running := st.containerRunning(); running {
		fmt.Fprintf(st.out, "warning: the gateway container is RUNNING — config changes are not applied yet.\n"+
			"  piccolo reload    apply config.json in place (channels/models/tools restart inside the gateway)\n"+
			"  piccolo restart   recreate the container (needed for piccolo.yaml changes)\n")
	} else {
		fmt.Fprintln(st.out, "next: piccolo serve   (starts the gateway with your new configuration)")
	}
	fmt.Fprintln(st.out, "check: piccolo doctor")
	return nil
}

func (st *setupState) containerRunning() bool {
	name, _, runner, _, err := containerContext(st.cmd)
	if err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), runner.ProbeTimeout+2*time.Second)
	defer cancel()
	state, err := runner.StateByName(ctx, name)
	return err == nil && state.Found && state.Running
}

// homeMenu renders the section list with live summaries.
func (st *setupState) homeMenu() (string, error) {
	chans := setup.Channels(st.doc)
	chansSummary := "none configured"
	if n := len(chans); n == 1 {
		chansSummary = "1 channel"
	} else if n > 1 {
		chansSummary = fmt.Sprintf("%d channels", n)
	}
	models := setup.Models(st.doc, st.secrets)
	modelSummary := "NONE — doctor will fail"
	if n := len(models); n == 1 {
		modelSummary = "1 model: " + models[0].Alias
	} else if n > 1 {
		modelSummary = fmt.Sprintf("%d models", n)
	}
	tools := setup.GetTools(st.doc)
	toolSummary := toolStateSummary(tools)

	var choice string
	form := setupForm(huh.NewGroup(
		huh.NewSelect[string]().
			Title("Piccolo setup — what do you want to change?").
			Description("↑/↓ move · enter open · esc quit").
			Options(
				huh.NewOption(fmt.Sprintf("Channels   (%s)", chansSummary), "channels"),
				huh.NewOption(fmt.Sprintf("Models     (%s)", modelSummary), "models"),
				huh.NewOption(fmt.Sprintf("Tools      (%s)", toolSummary), "tools"),
				huh.NewOption(fmt.Sprintf("Container  (web console: %s)", yesNo(st.webConsoleEnabled())), "container"),
				huh.NewOption("Agents       (not editable yet — edit config.json manually)", "__agents__"),
				huh.NewOption("Identity     (not editable yet — edit picoclaw/agents/*/SOUL.md manually)", "__identity__"),
				huh.NewOption("Quit", "__quit__"),
			).
			Value(&choice),
	))
	if err := form.Run(); err != nil {
		return "", err
	}
	switch choice {
	case "__agents__", "__identity__":
		fmt.Fprintln(st.out, "Agents/Identity editing is not implemented yet — edit picoclaw/config.json / piccolo.yaml directly; piccolo doctor validates your changes.")
		return "__back__", nil
	}
	return choice, nil
}

func (st *setupState) webConsoleEnabled() bool {
	return webConsoleURL(st.manifest) != ""
}

func (st *setupState) containerScreen() error {
	for {
		enabled := st.webConsoleEnabled()
		status := "off"
		verb := "enable"
		if enabled {
			status = "on"
			verb = "disable"
		}
		encStatus, encVerb := "off", "enable"
		if st.encrypted {
			encStatus, encVerb = "on", "disable"
		}
		var pick string
		form := setupForm(huh.NewGroup(
			huh.NewSelect[string]().
				Title("Container").
				Description("Container launch settings; esc back.").
				Options(
					huh.NewOption(fmt.Sprintf("Web console [%s] — %s browser dashboard", status, verb), "web_console:"+verb),
					huh.NewOption(fmt.Sprintf("Credential encryption [%s] — %s enc:// for this workspace", encStatus, encVerb), "encrypt:"+encVerb),
					huh.NewOption("← Back", "__back__"),
				).
				Value(&pick),
		))
		if err := form.Run(); err != nil {
			return err
		}
		if pick == "__back__" {
			return nil
		}
		feature, verb, ok := strings.Cut(pick, ":")
		if !ok {
			continue
		}
		if feature == "encrypt" {
			// The posture flag is re-derived by the caller loop, so the model list
			// and every credential field's help text follow the new state at once.
			if err := st.applyEncryptionChange(verb == "enable"); err != nil {
				if isCancel(err) {
					continue
				}
				return err
			}
			continue
		}
		if err := st.applyWebConsoleChange(verb == "enable"); err != nil {
			if isCancel(err) {
				continue
			}
			return err
		}
	}
}

func (st *setupState) applyWebConsoleChange(enabled bool) error {
	currently := st.webConsoleEnabled()
	if currently == enabled {
		fmt.Fprintf(st.out, "web console already %s.\n", yesNo(enabled))
		return nil
	}
	if enabled {
		if !hasContainerPort(st.manifest.Container.Ports, 18800) {
			st.manifest.Container.Ports = append(st.manifest.Container.Ports, generate.LauncherPort)
		}
		if st.manifest.Container.Env == nil {
			st.manifest.Container.Env = map[string]string{}
		}
		st.manifest.Container.Env["PICOCLAW_GATEWAY_HOST"] = "0.0.0.0"
		if runtimeimage.IsLocalRuntimeImage(st.manifest.Container.Image) {
			st.manifest.Container.Image = generate.LauncherImage
			fmt.Fprintln(st.out, "note: switched to the official launcher image so the web console listens on :18800. Run 'piccolo image build --set' after this if you want a local launcher runtime with the file-handling toolchain baked in.")
		} else {
			st.manifest.Container.Image = generate.LauncherImage
		}
	} else {
		st.manifest.Container.Ports = removeContainerPort(st.manifest.Container.Ports, 18800)
		if runtimeimage.IsLocalRuntimeImage(st.manifest.Container.Image) {
			st.manifest.Container.Image = generate.DefaultImage
			fmt.Fprintln(st.out, "note: switched to the official gateway image. Run 'piccolo image build --set' after this if you want a local gateway runtime with the file-handling toolchain baked in.")
		} else {
			st.manifest.Container.Image = generate.DefaultImage
		}
	}
	return st.applyManifestAndConfirm(fmt.Sprintf("web console %s", yesNo(enabled)))
}

func removeContainerPort(ports []string, containerPort int) []string {
	out := ports[:0]
	for _, p := range ports {
		if !hasContainerPort([]string{p}, containerPort) {
			out = append(out, p)
		}
	}
	return out
}

func toolStateSummary(t setup.ToolsState) string {
	on := func(b *bool) string {
		if b != nil && *b {
			return "on"
		}
		return "off"
	}
	return fmt.Sprintf("web:%s exec:%s remote-exec:%s spawn:%s subagent:%s images:%s mcp:%s", on(t.Web), on(t.Exec), on(t.ExecAllowRemote), on(t.Spawn), on(t.Subagent), on(t.LoadImage), on(t.MCP))
}

// ---------- channels ----------

func (st *setupState) channelsScreen(deepLink string) error {
	for {
		chans := setup.Channels(st.doc)
		if deepLink != "" {
			target := deepLink
			deepLink = ""
			entry, exists := st.channelEntry(target)
			if exists {
				chType, _ := entry.GetString("type")
				if chType == "" {
					chType = target
				}
				if t, known := setup.Type(chType); known {
					pre, _ := setup.ReadChannel(st.doc, target)
					if err := st.channelForm(t, target, &pre); err != nil {
						if isCancel(err) {
							continue
						}
						return err
					}
					return nil
				}
				if err := st.manageChannel(target); err != nil {
					return err
				}
				continue
			}
			if t, known := setup.Type(target); known {
				if err := st.channelForm(t, t.Name, nil); err != nil {
					return err
				}
				return nil
			}
			return fmt.Errorf("unknown channel type %q (piccolo setup channels supports: %s)", target, strings.Join(typeNames(), ", "))
		}
		opts := []huh.Option[string]{huh.NewOption("+ Add a channel…", "__add__")}
		for _, c := range chans {
			state := "○ disabled"
			if c.Enabled {
				state = "● enabled"
			}
			label := c.Name
			if c.Unknown {
				label += " (" + c.Type + ", manual)"
			}
			opts = append(opts, huh.NewOption(fmt.Sprintf("%-14s %s", label, state), c.Name))
		}
		opts = append(opts, huh.NewOption("← Back", "__back__"))

		var pick string
		if deepLink != "" {
			pick = deepLink
			deepLink = ""
		} else {
			form := setupForm(huh.NewGroup(
				huh.NewSelect[string]().
					Title("Channels").
					Description("↑/↓ move · enter manage · esc back").
					Options(opts...).
					Value(&pick),
			))
			if err := form.Run(); err != nil {
				return err
			}
		}

		switch pick {
		case "__back__", "":
			return nil
		case "__add__":
			if err := st.addChannel(); err != nil {
				if isCancel(err) {
					continue
				}
				return err
			}
		default:
			if err := st.manageChannel(pick); err != nil {
				if isCancel(err) {
					continue
				}
				return err
			}
		}
	}
}

func (st *setupState) addChannel() error {
	var typeName string
	opts := []huh.Option[string]{huh.NewOption("← Back", "__back__")}
	for _, t := range setup.Types {
		if _, exists := st.channelEntry(t.Name); exists {
			opts = append(opts, huh.NewOption(fmt.Sprintf("%s (already configured)", t.Label), "__taken__"))
			continue
		}
		opts = append(opts, huh.NewOption(t.Label, t.Name))
	}
	form := setupForm(huh.NewGroup(
		huh.NewSelect[string]().Title("Add which channel?").Options(opts...).Value(&typeName),
	))
	if err := form.Run(); err != nil {
		return err
	}
	if typeName == "" || typeName == "__back__" || typeName == "__taken__" {
		return errCancel
	}
	t, ok := setup.Type(typeName)
	if !ok {
		return fmt.Errorf("unknown channel type %q", typeName)
	}
	return st.channelForm(t, t.Name, nil)
}

func (st *setupState) channelEntry(name string) (*configdoc.Doc, bool) {
	cl, ok := st.doc.GetObject("channel_list")
	if !ok {
		return nil, false
	}
	return cl.GetObject(name)
}

func typeNames() []string {
	var out []string
	for _, t := range setup.Types {
		out = append(out, t.Name)
	}
	return out
}

func (st *setupState) manageChannel(name string) error {
	entry, ok := st.channelEntry(name)
	if !ok {
		return fmt.Errorf("channel %q disappeared", name)
	}
	enabled, _ := entry.GetBool("enabled")
	chType, _ := entry.GetString("type")
	if chType == "" {
		chType = name
	}
	t, known := setup.Type(chType)

	var action string
	opts := []huh.Option[string]{huh.NewOption("← Back", "__back__")}
	toggle := "Enable"
	if enabled {
		toggle = "Disable"
	}
	opts = append(opts, huh.NewOption(toggle+" channel", "__toggle__"))
	if known {
		opts = append(opts, huh.NewOption("Edit settings…", "__edit__"))
	}
	form := setupForm(huh.NewGroup(
		huh.NewSelect[string]().Title(fmt.Sprintf("Channel %q (type: %s)", name, chType)).Options(opts...).Value(&action),
	))
	if err := form.Run(); err != nil {
		return err
	}
	switch action {
	case "__toggle__":
		setup.SetChannelEnabled(st.doc, name, !enabled)
		return st.applyAndConfirm(fmt.Sprintf("channel %q %s", name, map[bool]string{true: "enabled", false: "disabled"}[!enabled]))
	case "__edit__":
		existing, _ := setup.ReadChannel(st.doc, name)
		return st.channelForm(t, name, &existing)
	}
	return errCancel
}

// channelForm prints the guided provider steps, collects values, patches.
func (st *setupState) channelForm(t setup.ChannelType, name string, prefill *setup.ChannelValues) error {
	var discordApplicationID string
	if t.Name == "discord" {
		var err error
		discordApplicationID, err = st.discordApplicationSetup(prefill != nil)
		if err != nil {
			return err
		}
		fmt.Fprintf(st.out, "\n\u2500\u2500 Configure %s runtime settings \u2500\u2500\n", t.Label)
		fmt.Fprintln(st.out, "Piccolo will store the bot token in config.json and the non-secret Application ID in piccolo.yaml.")
		fmt.Fprintln(st.out)
	} else {
		fmt.Fprintf(st.out, "\n\u2500\u2500 Set up %s \u2500\u2500 steps to do FIRST (piccolo cannot do them for you):\n", t.Label)
		for _, s := range t.Steps {
			fmt.Fprintf(st.out, "  %s\n", s)
		}
		fmt.Fprintln(st.out)
	}

	var allowRaw string
	var restrict bool = true
	var mentionOnly bool

	if prefill != nil {
		allowRaw = strings.Join(prefill.AllowFrom, ", ")
		restrict = !(len(prefill.AllowFrom) == 1 && prefill.AllowFrom[0] == "*")
		mentionOnly = prefill.MentionOnly
	}

	allowInput := huh.NewInput().
		Title("Allowed user IDs (comma separated)").
		Placeholder("e.g. 123456789012345678").
		Value(&allowRaw)
	if prefill == nil {
		allowInput.Description("from the provider steps above")
	}
	restrictDesc := "Recommended: yes. If NO, ANYONE who can message the bot can talk to your assistant."
	if prefill != nil {
		restrictDesc = fmt.Sprintf("Current: %s. %s", yesNo(restrict), restrictDesc)
	}
	groups := []*huh.Group{
		// Navigating away from a confirm commits it, so the hide func
		// reads the fresh restrict value when the next group renders.
		huh.NewGroup(
			huh.NewConfirm().
				Title("Restrict the bot to specific user IDs?").
				Description(restrictDesc).
				Value(&restrict),
		),
	}
	// Field help page: shown immediately before the IDs input, only when it
	// would actually help (user hasn't filled IDs yet). Enter advances it.
	if t.AllowFromGuide != "" {
		filled := prefill != nil && strings.TrimSpace(allowRaw) != ""
		note := huh.NewGroup(
			huh.NewNote().
				Title("How to get your user IDs").
				Description(t.AllowFromGuide),
		).WithHideFunc(func() bool { return !restrict || filled })
		groups = append(groups, note)
	}
	groups = append(groups, huh.NewGroup(allowInput).WithHideFunc(func() bool { return !restrict }))
	if t.MentionOnly {
		if prefill == nil {
			mentionOnly = true
		}
		mentionDesc := "DMs always work; this controls group channels."
		if prefill != nil {
			mentionDesc = fmt.Sprintf("Current: %s. %s", yesNo(mentionOnly), mentionDesc)
		}
		groups = append(groups, huh.NewGroup(
			huh.NewConfirm().
				Title("Require @mentions in group/channel conversations?").
				Description(mentionDesc).
				Value(&mentionOnly),
		))
	}
	inputs := make(map[string]*string, len(t.Fields))
	for _, f := range t.Fields {
		p := new(string)
		inputs[f.Key] = p
		var in *huh.Input
		switch f.Type {
		case setup.Secret:
			// Never re-show a stored secret; empty input keeps it (handled after Run).
			desc := st.credentialStorageNote(f.Help)
			if sc := st.secrets.ChannelSecret(name, f.Key); sc.Matched {
				// A sidecar value for this field replaces config.json's on load, so a
				// write here is inert — and unlike the empty-list model case, the value
				// may be perfectly good, which is why the field still renders.
				desc = fmt.Sprintf("INERT for this channel: picoclaw/.security.yml already sets %q for %s — edit it there instead", f.Key, name)
			}
			if prefill != nil {
				desc += "; leave empty to keep the current value"
			}
			in = huh.NewInput().Title(f.Title).Description(desc).
				EchoMode(huh.EchoModePassword).Value(p)
		default:
			if prefill != nil {
				*p = prefill.Fields[f.Key]
			}
			in = huh.NewInput().Title(f.Title).Placeholder(f.Placeholder).Description(f.Help).Value(p)
		}
		groups = append(groups, huh.NewGroup(in))
	}

	if err := setupForm(groups...).Run(); err != nil {
		return err
	}
	if t.Name == "discord" {
		st.pendingDiscordApplicationID = discordApplicationID
		st.pendingDiscordMentionOnly = mentionOnly
		if token := strings.TrimSpace(valueOf(inputs["token"])); token != "" {
			if sc := st.secrets.ChannelSecret("discord", "token"); sc.Matched {
				// Probing a token that is about to be refused would report success for a
				// credential Piccolo is not storing, and the gateway would never use it.
				fmt.Fprintln(st.out, "skipping the Discord token check: config.json cannot deliver a token while picoclaw/.security.yml sets that field")
			} else {
				st.checkDiscordToken(token)
			}
		}
	}

	v := setup.ChannelValues{MentionOnly: mentionOnly, Fields: map[string]string{}}
	allow := strings.TrimSpace(allowRaw)
	if !restrict {
		v.AllowFrom = []string{"*"}
	} else if allow != "" {
		for _, id := range strings.Split(allow, ",") {
			if id = strings.TrimSpace(id); id != "" {
				v.AllowFrom = append(v.AllowFrom, id)
			}
		}
	} else {
		fmt.Fprintln(st.out, "refusing to enable a channel with nobody allowed: set at least one user ID or choose open access")
		return errCancel
	}
	for _, f := range t.Fields {
		p := inputs[f.Key]
		if f.Type == setup.Secret {
			// Empty secret input leaves the stored value untouched
			// (field omitted from the patch).
			if *p != "" {
				if sc := st.secrets.ChannelSecret(name, f.Key); sc.Matched {
					// Same refusal as the model form: a secret written into a field the
					// gateway will not read is a credential in the wrong file and a
					// change that never happens.
					st.setNotice("%s for %q NOT written: picoclaw/.security.yml already sets that field for %s — change the token there",
						f.Key, name, name)
					fmt.Fprintf(st.out, "refusing to write %q to config.json: picoclaw/.security.yml overrides that field for %s\n", f.Key, name)
					continue
				}
				v.Fields[f.Key] = *p
			}
			continue
		}
		v.Fields[f.Key] = *p // non-secret: include empty to clear optional fields
	}
	if restrict && len(v.AllowFrom) == 0 {
		return errCancel
	}
	setup.PatchChannel(st.doc, t, name, v)
	return st.applyAndConfirm("channel " + name)
}

const discordInvitePermissions = "68608" // View Channels + Send Messages + Read Message History.

func (st *setupState) discordApplicationSetup(alreadyConfigured bool) (string, error) {
	existing := strings.TrimSpace(st.manifest.Setup.Channels.Discord.ApplicationID)
	hasApp := existing != "" || alreadyConfigured
	if !alreadyConfigured {
		form := setupForm(huh.NewGroup(
			huh.NewConfirm().
				Title("Do you already have a Discord application for this bot?").
				Description("If not, piccolo will show the creation links before asking for the Application ID.").
				Affirmative("Yes, I already have one").
				Negative("No, help me create one").
				Value(&hasApp),
		))
		if err := form.Run(); err != nil {
			return "", err
		}
	}
	if !hasApp {
		appsURL := "https://discord.com/developers/applications"
		docsURL := "https://discord.com/developers/docs/quick-start/getting-started#configuring-your-bot"
		if err := setupForm(huh.NewGroup(huh.NewNote().
			Title("Create a Discord application first").
			Description(fmt.Sprintf("1. Open %s\n"+
				"2. Click New Application, give it a name, and open it.\n"+
				"3. Copy General Information → Application ID.\n"+
				"4. Open Bot settings, create/reset the bot token, and copy it.\n"+
				"5. Enable Bot → Privileged Gateway Intents → Message Content Intent.\n\n"+
				"Discord docs: %s", terminalLink("Discord Developer Portal", appsURL), terminalLink("Configuring your bot", docsURL))))).Run(); err != nil {
			return "", err
		}
	}
	appID := existing
	form := setupForm(huh.NewGroup(
		huh.NewInput().
			Title("Discord Application ID / Client ID").
			Description(fmt.Sprintf("Required. Open %s and copy General Information → Application ID. Piccolo stores this non-secret ID in piccolo.yaml to regenerate invite/setup links later.", terminalLink("your Discord applications", "https://discord.com/developers/applications"))).
			Placeholder("e.g. 123456789012345678").
			Value(&appID),
	))
	if err := form.Run(); err != nil {
		return "", err
	}
	appID = strings.TrimSpace(appID)
	if appID == "" {
		return "", fmt.Errorf("Discord Application ID is required")
	}
	return appID, nil
}

func (st *setupState) checkDiscordToken(token string) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://discord.com/api/v10/users/@me", nil)
	if err != nil {
		return
	}
	req.Header.Set("Authorization", "Bot "+token)
	req.Header.Set("User-Agent", "piccolo setup")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Fprintf(st.out, "warning: could not validate Discord token: %v\n", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		fmt.Fprintf(st.out, "warning: Discord token check failed (%s). Make sure you copied the Bot token, not the Client Secret.\n", resp.Status)
		return
	}
	var body struct {
		Username      string `json:"username"`
		Discriminator string `json:"discriminator"`
		ID            string `json:"id"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	name := strings.TrimSpace(body.Username)
	if body.Discriminator != "" && body.Discriminator != "0" {
		name += "#" + body.Discriminator
	}
	if name == "" {
		name = body.ID
	}
	fmt.Fprintf(st.out, "\u2713 Discord token validated: authenticated as %s\n", name)
}

func (st *setupState) printDiscordNextSteps(appID string, mentionOnly bool) {
	invite := fmt.Sprintf("https://discord.com/oauth2/authorize?client_id=%s&scope=bot&permissions=%s", appID, discordInvitePermissions)
	botURL := fmt.Sprintf("https://discord.com/developers/applications/%s/bot", appID)
	oauthURL := fmt.Sprintf("https://discord.com/developers/applications/%s/oauth2/url-generator", appID)
	fmt.Fprintln(st.out)
	fmt.Fprintln(st.out, "Discord next steps:")
	fmt.Fprintf(st.out, "  1. Invite the bot to your server:\n     %s\n", terminalLink("Open invite URL", invite))
	fmt.Fprintln(st.out, "     Permissions requested: View Channels, Send Messages, Read Message History.")
	fmt.Fprintf(st.out, "  2. Verify Message Content Intent is enabled:\n     %s\n", terminalLink("Open Bot settings", botURL))
	fmt.Fprintf(st.out, "  3. OAuth2 URL Generator, if you want to inspect the invite manually:\n     %s\n", terminalLink("Open OAuth2 URL Generator", oauthURL))
	if st.containerRunning() {
		fmt.Fprintln(st.out, "  4. Apply the new channel config: piccolo reload  (or piccolo restart if reload fails)")
	} else {
		fmt.Fprintln(st.out, "  4. Start the gateway: piccolo serve")
	}
	if mentionOnly {
		fmt.Fprintln(st.out, "  5. Test: DM the bot, or mention it in a server channel: @YourBot hello")
	} else {
		fmt.Fprintln(st.out, "  5. Test: DM the bot or send a message in a server channel it can read.")
	}
	fmt.Fprintln(st.out, "  6. Debug: piccolo doctor; piccolo logs -f")
	fmt.Fprintln(st.out)
}

func valueOf(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func terminalLink(label, url string) string {
	// OSC 8 hyperlinks are supported by many modern terminals. Keep the raw URL
	// visible too so the output remains useful in terminals that ignore OSC 8.
	return fmt.Sprintf("\x1b]8;;%s\x1b\\%s\x1b]8;;\x1b\\ (%s)", url, label, url)
}

func yesNo(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

// ---------- models ----------

// modelRef renders a model entry as "provider/model" for display (or just
// "model" when no explicit provider is set).
func modelRef(m setup.ModelInfo) string {
	if m.Provider != "" {
		return m.Provider + "/" + m.Model
	}
	return m.Model
}

func (st *setupState) modelsScreen() error {
	for {
		models := setup.Models(st.doc, st.secrets)
		defaultModel := strings.TrimSpace(setup.DefaultModel(st.doc))
		imageModel := strings.TrimSpace(setup.DefaultImageModel(st.doc))
		opts := []huh.Option[string]{huh.NewOption("+ Add a model…", "__add__")}
		for _, m := range models {
			opts = append(opts, huh.NewOption(modelListLabel(m, defaultModel, imageModel), m.Alias))
		}
		// A pick-list of models is meaningless without models: offering it and
		// then erroring out would abort the whole setup session.
		if len(models) > 0 {
			opts = append(opts,
				huh.NewOption(fmt.Sprintf("Set default text model… (%s)", modelSlotLabel(defaultModel)), "__default_model__"),
				huh.NewOption(fmt.Sprintf("Set image/vision model… (%s)", modelSlotLabel(imageModel)), "__image_model__"),
				huh.NewOption("- Delete a model…", "__delete__"),
			)
		}
		opts = append(opts, huh.NewOption("← Back", "__back__"))

		var pick string
		desc := "agents use agents.defaults.model_name for text and agents.defaults.image_model for image attachments"
		if st.notice != "" {
			desc, st.notice = st.notice, ""
		}
		form := setupForm(huh.NewGroup(
			huh.NewSelect[string]().
				Title("Models").
				Description(desc).
				Options(opts...).
				Value(&pick),
		))
		if err := form.Run(); err != nil {
			return err
		}
		switch pick {
		case "__back__":
			return nil
		case "__add__":
			if err := st.modelForm(""); err != nil {
				if isCancel(err) {
					continue
				}
				return err
			}
		case "__default_model__":
			if err := st.defaultModelForm(); err != nil {
				if isCancel(err) {
					continue
				}
				return err
			}
		case "__image_model__":
			if err := st.imageModelForm(); err != nil {
				if isCancel(err) {
					continue
				}
				return err
			}
		case "__delete__":
			if err := st.deleteModelForm(); err != nil {
				if isCancel(err) {
					continue
				}
				return err
			}
		default:
			if err := st.modelForm(pick); err != nil {
				if isCancel(err) {
					continue
				}
				return err
			}
		}
	}
}

// modelListLabel renders one model_list entry for a select, marking the entries
// config.json currently points at so a rename or delete shows what it will touch
// before the user commits to it.
func modelListLabel(m setup.ModelInfo, defaultModel, imageModel string) string {
	state := "no key!"
	switch {
	case m.Shield.Matched && m.Shield.HasKeys:
		// config.json shows nothing for this entry and the gateway still has a key.
		state = "key in .security.yml"
	case m.Shield.Matched && !m.Shield.HasKeys:
		// The sidecar has a key for this model and it is empty, so config.json's own
		// api_keys — if any — is discarded. This reads as configured and is not.
		state = "no key (.security.yml clears it)"
	case m.EffectiveKeySet():
		state = "key set"
	}
	roles := ""
	if m.Alias == defaultModel {
		roles += " [default]"
	}
	if m.Alias == imageModel {
		roles += " [image]"
	}
	return fmt.Sprintf("%-16s %s (%s)%s", m.Alias, state, modelRef(m), roles)
}

// modelSlotLabel renders the current value of a model field that may be unset.
func modelSlotLabel(alias string) string {
	if alias == "" {
		return "not set"
	}
	return alias
}

func (st *setupState) modelForm(alias string) error {
	// lookup is the alias being edited; alias may become a new one. They differ
	// exactly when this is a rename.
	lookup := alias
	var provider, model, base, key string
	if alias != "" {
		provider, model, key, base, _ = setup.ReadModel(st.doc, alias)
		// `key` keeps the stored value; a non-empty API key input overrides it.
	}
	shield := setup.ModelShield(st.doc, st.secrets, lookup)
	var newKey string
	keyDesc := st.credentialStorageNote("")
	if shield.Matched {
		// The one case where typing a key here cannot work: PicoClaw replaces
		// config.json's api_keys with the sidecar's whenever that model has a key
		// there, so a config.json edit is inert. Say it BEFORE the input, because a
		// warning after the fact is how the user finds out on their next failed turn.
		keyDesc = fmt.Sprintf("INERT for this model: picoclaw/.security.yml %s overrides config.json's api_keys — edit that key to change this model's credential", shield.Key)
	}
	form := setupForm(
		huh.NewGroup(
			huh.NewInput().Title("Model alias (model_name in config.json)").
				Description("renaming re-points every reference: agents.defaults.model_name, image_model, the fallback lists, and agents.list[].model").
				Placeholder("gpt-5.4").Value(&alias),
			huh.NewInput().Title("Provider").
				Description("e.g. openai, openrouter, anthropic — sent to config.json as an explicit provider").
				Placeholder("openai").Value(&provider),
			huh.NewInput().Title("Model id").
				Description("the provider-side model id, without the provider prefix").
				Placeholder("gpt-5.4").Value(&model),
			huh.NewInput().Title("API key").
				Description(keyDesc).
				EchoMode(huh.EchoModePassword).Value(&newKey),
			huh.NewInput().Title("API base URL (optional)").
				Placeholder("empty = provider default").Value(&base),
		),
	)
	if err := form.Run(); err != nil {
		return err
	}
	alias = strings.TrimSpace(alias)
	if alias == "" {
		return fmt.Errorf("model alias is required")
	}
	if newKey != "" {
		// Resolve against the document as it now stands: the entry this key belongs
		// to is the one about to be written, which after a rename carries a name the
		// pre-form check never saw.
		final := setup.ShieldAfterWrite(st.doc, st.secrets, lookup, alias)
		if final.Matched {
			// Do not write a secret into a field the gateway will not read: that is the
			// inert-write bug with a fresh credential in it. `key` keeps the value read
			// from config.json, so the other fields still move and nothing new is stored
			// somewhere it cannot take effect.
			st.setNotice("API key for %q NOT written: picoclaw/.security.yml %s overrides config.json for this model — change the key there",
				alias, final.Key)
			fmt.Fprintf(st.out, "refusing to write an API key config.json cannot deliver: model %q is overridden by picoclaw/.security.yml key %s\n", alias, final.Key)
			if lookup != "" {
				fmt.Fprintf(st.out, "  provider, model and base URL were still updated; the key stays wherever it is today\n")
			}
		} else {
			key = newKey
		}
	}
	if err := setup.PatchModel(st.doc, lookup, alias, strings.TrimSpace(provider), strings.TrimSpace(model), key, strings.TrimSpace(base)); err != nil {
		return err
	}
	what := "model " + alias
	if lookup != "" && lookup != alias {
		refs := setup.RenameModelRefs(st.doc, lookup, alias)
		what = fmt.Sprintf("model %s (renamed from %s", alias, lookup)
		if len(refs) > 0 {
			what += "; re-pointing " + strings.Join(refs, ", ")
		} else {
			what += "; nothing referenced it"
		}
		what += ")"
		// Both findings belong on the next screen, so they are collected rather than
		// each overwriting the last: setNotice carries one message, and a rename that
		// trips both must not lose either.
		var notes []string
		// An AGENT.md `model:` pin outranks config.json for that agent, and
		// renaming through it would rewrite a file the user owns.
		if pins := st.agentFilesPinningModel(lookup); len(pins) > 0 {
			notes = append(notes, fmt.Sprintf("%s pin model %q in their own frontmatter — those files are yours to edit",
				strings.Join(pins, ", "), lookup))
		}
		// A rename moves the model out from under its own sidecar key, which is named
		// after the OLD alias: the key keeps guarding a model that is no in
		// model_list, and the renamed entry loses the credential that used to cover
		// it. Nothing in config.json shows this, and every reference still resolves,
		// so the only symptom is an authentication failure on the next turn.
		//
		// A rename onto a name model_list already carries is refused by PatchModel, so
		// the destination alias is always new and its first occurrence key is
		// "<alias>:0" — the one bare `<alias>` also covers, since PicoClaw falls back
		// to it only when the indexed key is absent.
		if shield.Matched {
			notes = append(notes, fmt.Sprintf("renaming %s left its picoclaw/.security.yml key behind: %s guards a model that is no longer in model_list, and %q now has no key there — move that key to %q (or bare %q) to keep the credential",
				lookup, shield.Key, alias, alias+":0", alias))
			fmt.Fprintf(st.out, "NOTE: picoclaw/.security.yml key %s still guards the OLD alias — %q will run with no API key until you move it\n", shield.Key, alias)
		}
		if len(notes) > 0 {
			st.setNotice("%s", strings.Join(notes, " ; "))
		}
	}
	return st.applyAndConfirm(what)
}

func (st *setupState) imageModelForm() error {
	pick, err := st.pickModelForm(
		"Image/vision model",
		"Recommended: choose a multimodal model. PicoClaw uses agents.defaults.image_model for image attachments from Discord/Telegram/etc.",
		strings.TrimSpace(setup.DefaultImageModel(st.doc)),
	)
	if err != nil {
		return err
	}
	setup.SetDefaultImageModel(st.doc, pick)
	return st.applyAndConfirm("image model " + pick)
}

// defaultModelForm writes agents.defaults.model_name, the alias every agent
// inherits. This slot had no editor at all before: repointing an entry or
// hand-editing config.json were the only ways to move the default.
func (st *setupState) defaultModelForm() error {
	pick, err := st.pickModelForm(
		"Default text model",
		"agents.defaults.model_name is what agents use unless their AGENT.md frontmatter or their agents.list entry pins a model of its own. PicoClaw falls back to model_fallbacks when the chosen model has no endpoint — free models vanish without notice, so listing one or two here is cheap insurance.",
		strings.TrimSpace(setup.DefaultModel(st.doc)),
	)
	if err != nil {
		return err
	}
	setup.SetDefaultModel(st.doc, pick)
	return st.applyAndConfirm("default model " + pick)
}

// pickModelForm asks for one alias among the configured models, starting from
// current (the first entry when current is unset or points at a model that no
// longer exists — a select whose value matches no option renders nothing).
func (st *setupState) pickModelForm(title, description, current string) (string, error) {
	models := setup.Models(st.doc, st.secrets)
	if len(models) == 0 {
		return "", fmt.Errorf("add a model first")
	}
	known := false
	for _, m := range models {
		if m.Alias == current {
			known = true
			break
		}
	}
	if !known {
		current = models[0].Alias
	}
	opts := make([]huh.Option[string], 0, len(models))
	for _, m := range models {
		opts = append(opts, huh.NewOption(fmt.Sprintf("%s (%s)", m.Alias, modelRef(m)), m.Alias))
	}
	pick := current
	form := setupForm(huh.NewGroup(
		huh.NewSelect[string]().
			Title(title).
			Description(description).
			Options(opts...).
			Value(&pick),
	))
	if err := form.Run(); err != nil {
		return "", err
	}
	pick = strings.TrimSpace(pick)
	if pick == "" {
		return "", fmt.Errorf("model alias is required")
	}
	return pick, nil
}

// deleteModelForm removes one model_list entry. setup.RemoveModel refuses while
// the alias is still load-bearing — deleting the model a turn is about to use
// would leave config.json pointing at an alias that no longer exists, which
// doctor can only report after the fact.
func (st *setupState) deleteModelForm() error {
	models := setup.Models(st.doc, st.secrets)
	if len(models) == 0 {
		fmt.Fprintln(st.out, "no models configured to delete.")
		return nil
	}
	defaultModel := strings.TrimSpace(setup.DefaultModel(st.doc))
	imageModel := strings.TrimSpace(setup.DefaultImageModel(st.doc))
	opts := make([]huh.Option[string], 0, len(models))
	for _, m := range models {
		label := modelListLabel(m, defaultModel, imageModel)
		if n := len(setup.ModelRefPaths(st.doc, m.Alias)); n > 0 {
			label += fmt.Sprintf(" — %d reference(s)", n)
		}
		opts = append(opts, huh.NewOption(label, m.Alias))
	}
	var pick string
	form := setupForm(huh.NewGroup(
		huh.NewSelect[string]().
			Title("Delete which model?").
			Description("A model still named as the default, the image model, or an agent's model cannot be deleted — move those first, then delete it. Entries in the fallback lists are dropped along with it.").
			Options(opts...).
			Value(&pick),
	))
	if err := form.Run(); err != nil {
		return err
	}
	before := len(models)
	if err := setup.RemoveModel(st.doc, pick); err != nil {
		st.setNotice("cannot delete %s: %v", pick, err)
		if pick == defaultModel {
			st.notice += " — choose another default first with 'Set default text model…'"
		}
		if pick == imageModel {
			st.notice += " — choose another image model first with 'Set image/vision model…'"
		}
		return nil
	}
	if err := st.applyAndConfirm(fmt.Sprintf("delete model %s", pick)); err != nil {
		return err
	}
	// The sidecar outlives the entry it was written for. Nothing removes the key
	// with the model, and an orphan key is invisible to the user, who will later
	// re-add the name and find config.json's own key ignored by a leftover they
	// never saw. Doctor reports the same state.
	var notes []string
	for _, m := range models {
		if m.Alias == pick && m.Shield.Matched {
			notes = append(notes, fmt.Sprintf("picoclaw/.security.yml still carries %s for the model you deleted — remove that key too, or config.json's api_keys will be ignored the moment a model named %q exists again",
				m.Shield.Key, pick))
			break
		}
	}
	if before == 1 && len(setup.Models(st.doc, st.secrets)) == 0 {
		notes = append(notes, "model_list is now empty — doctor will FAIL and no agent can answer until you add a model")
	}
	if len(notes) > 0 {
		st.setNotice("%s", strings.Join(notes, " ; "))
	}
	return nil
}

var modelLineRE = regexp.MustCompile(`(?m)^model:[ \t]*(\S*)`)

// agentFrontmatterModel reads the model key out of an AGENT.md YAML header.
// "" when the file has no frontmatter or no such key: Piccolo never generates
// one, so a model pin is always something a person wrote.
func agentFrontmatterModel(content string) string {
	if !strings.HasPrefix(content, "---\n") {
		return ""
	}
	rest := content[len("---\n"):]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return ""
	}
	m := modelLineRE.FindStringSubmatch(rest[:end])
	if m == nil {
		return ""
	}
	return strings.Trim(strings.TrimSpace(m[1]), "\"'")
}

// agentFilesPinningModel lists the workspace-relative AGENT.md paths whose
// frontmatter pins alias. A rename reports them instead of rewriting them.
func (st *setupState) agentFilesPinningModel(alias string) []string {
	var out []string
	for _, path := range st.agentMDPaths() {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if agentFrontmatterModel(string(data)) != alias {
			continue
		}
		rel, err := filepath.Rel(st.dir, path)
		if err != nil {
			rel = path
		}
		out = append(out, filepath.ToSlash(rel))
	}
	return out
}

// ---------- tools ----------

func (st *setupState) toolsScreen() error {
	for {
		cur := setup.GetTools(st.doc)
		state := map[string]*bool{"web": cur.Web, "exec": cur.Exec, "spawn": cur.Spawn, "subagent": cur.Subagent, "load_image": cur.LoadImage}
		opts := make([]huh.Option[string], 0, len(setup.ToolNames)+4)
		opts = append(opts, huh.NewOption("Sync AGENT.md allowlists and file-handling skill…", "__sync__"))
		for _, spec := range setup.ToolNames {
			enabled := false
			if p := state[spec.Key]; p != nil {
				enabled = *p
			}
			status := "off"
			verb := "enable"
			if enabled {
				status = "on"
				verb = "disable"
			}
			if enabled && len(st.agentToolAllowlistChanges(spec.Key, true)) > 0 {
				status += ", md out-of-sync"
			}
			opts = append(opts, huh.NewOption(
				fmt.Sprintf("%-22s [%s] — %s", spec.Label, status, spec.Help),
				spec.Key+":"+verb,
			))
		}
		feedbackEnabled := false
		if p := setup.ToolFeedbackEnabled(st.doc); p != nil {
			feedbackEnabled = *p
		}
		feedbackStatus := "off"
		feedbackVerb := "enable"
		if feedbackEnabled {
			feedbackStatus = "on"
			feedbackVerb = "disable"
		}
		opts = append(opts, huh.NewOption(
			fmt.Sprintf("%-22s [%s] — %s", "Tool feedback", feedbackStatus, "show tool calls/args in chat for debugging and audit"),
			"tool_feedback:"+feedbackVerb,
		))

		remoteEnabled := false
		if cur.ExecAllowRemote != nil {
			remoteEnabled = *cur.ExecAllowRemote
		}
		remoteStatus := "off"
		remoteVerb := "enable"
		if remoteEnabled {
			remoteStatus = "on"
			remoteVerb = "disable"
		}
		opts = append(opts, huh.NewOption(
			fmt.Sprintf("%-22s [%s] — %s", "Remote shell execution", remoteStatus, "allow exec from Discord/Telegram/etc.; use only for trusted chats"),
			"exec_remote:"+remoteVerb,
		))

		// Absent means restricted: PicoClaw defaults the flag to true
		// (pkg/config/defaults.go:29), so show the default as ON, not off.
		restricted := true
		if p := setup.WorkspaceRestricted(st.doc); p != nil {
			restricted = *p
		}
		restrictStatus := "off"
		restrictVerb := "enable"
		if restricted {
			restrictStatus = "on"
			restrictVerb = "disable"
		}
		opts = append(opts, huh.NewOption(
			fmt.Sprintf("%-22s [%s] — %s", "Workspace path guard", restrictStatus, "when ON, shell commands and writes naming paths outside an agent workspace are blocked"),
			"restrict_paths:"+restrictVerb,
		))
		opts = append(opts, huh.NewOption("← Back", "__back__"))

		var pick string
		form := setupForm(huh.NewGroup(
			huh.NewSelect[string]().
				Title("Tools").
				Description("Pick one tool to toggle; esc back.").
				Options(opts...).
				Value(&pick),
		))
		if err := form.Run(); err != nil {
			return err
		}
		if pick == "__back__" {
			return nil
		}
		if pick == "__sync__" {
			if err := st.syncEnabledToolAllowlists(); err != nil {
				if isCancel(err) {
					continue
				}
				return err
			}
			continue
		}
		key, verb, ok := strings.Cut(pick, ":")
		if !ok {
			continue
		}
		enabled := verb == "enable"
		if key == "tool_feedback" {
			if err := st.applyToolFeedbackChange(enabled); err != nil {
				if isCancel(err) {
					continue
				}
				return err
			}
			continue
		}
		if key == "exec_remote" {
			if err := st.applyRemoteExecChange(enabled); err != nil {
				if isCancel(err) {
					continue
				}
				return err
			}
			continue
		}
		if key == "restrict_paths" {
			if err := st.applyWorkspaceRestrictionChange(enabled); err != nil {
				if isCancel(err) {
					continue
				}
				return err
			}
			continue
		}
		if key == "mcp" && enabled {
			// Turning MCP on is the one tool toggle that can break every message, so it
			// does not go through the generic path: it verifies first. Handling it here
			// also keeps the toggle from being written to the document before the check.
			if err := st.applyMCPEnableChange(); err != nil {
				if isCancel(err) {
					continue
				}
				return err
			}
			continue
		}
		setup.SetTool(st.doc, key, enabled)
		if err := st.applyToolChange(key, enabled); err != nil {
			if isCancel(err) {
				continue
			}
			return err
		}
	}
}

type agentFileChange struct {
	path    string
	content []byte
}

func (st *setupState) syncEnabledToolAllowlists() error {
	changes, err := st.syncEnabledToolWorkspaceChanges()
	if err != nil {
		return err
	}
	return st.applyAgentFileChanges("agent tool allowlists and the file-handling skill", changes)
}

func (st *setupState) syncEnabledToolWorkspaceChanges() ([]agentFileChange, error) {
	setup.EnsureMediaReadConfig(st.doc)
	cur := setup.GetTools(st.doc)
	state := map[string]*bool{"web": cur.Web, "exec": cur.Exec, "spawn": cur.Spawn, "subagent": cur.Subagent, "load_image": cur.LoadImage, "mcp": cur.MCP}
	var enabledTools []string
	for _, spec := range setup.ToolNames {
		if p := state[spec.Key]; p != nil && *p {
			enabledTools = append(enabledTools, spec.Key)
		}
	}
	var changes []agentFileChange
	coord := st.coordinatorMDPath()
	for _, path := range st.agentMDPaths() {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		content := string(data)
		changed := false
		for _, key := range enabledTools {
			if path == coord {
				// See agentToolAllowlistChanges: a declared list on the coordinator is
				// a strict allowlist, and it would drop every dynamically-named MCP tool.
				break
			}
			updated, oneChanged := updateAgentToolsFrontmatter(content, frontmatterToolsForConfigTool(key), true)
			if oneChanged {
				content = updated
				changed = true
			}
		}
		updated, skillChanged := updateAgentSkillsFrontmatter(content, []string{generate.FileHandlingSkillName}, true)
		if skillChanged {
			content = updated
			changed = true
		}
		if changed {
			changes = append(changes, agentFileChange{path: path, content: []byte(content)})
		}
	}
	skillPath := st.skillPath()
	skillContent := []byte(generate.FileHandlingSkillMD())
	if data, err := os.ReadFile(skillPath); err != nil || !bytes.Equal(data, skillContent) {
		changes = append(changes, agentFileChange{path: skillPath, content: skillContent})
	}
	return changes, nil
}

func (st *setupState) applyToolChange(key string, enabled bool) error {
	changes := st.agentToolAllowlistChanges(key, enabled)
	return st.applyConfigAndAgentChanges(fmt.Sprintf("tool %s %s", key, yesNo(enabled)), changes)
}

func (st *setupState) applyToolFeedbackChange(enabled bool) error {
	setup.SetToolFeedback(st.doc, enabled)
	return st.applyConfigAndAgentChanges(fmt.Sprintf("tool feedback %s", yesNo(enabled)), nil)
}

// applyMCPEnableChange turns tools.mcp.enabled on only after the registered
// integration servers have actually completed an MCP initialize handshake.
//
// The registration path already verifies this, but the toggle can also be flipped
// here, and a manual enable that skips verification is how a workspace ends up with
// every chat message failing: PicoClaw aborts the whole turn when a registered server
// cannot connect. It also checks the container that is ACTUALLY SERVING TRAFFIC, not
// just the image piccolo.yaml names, because the two can differ — `integration add`
// bakes a new image and rewrites the manifest while the running gateway keeps the old
// binaries until a restart, and config.json is re-read immediately. Observed live:
// the new image answered initialize cleanly while the running 0.3.4 binary still
// replied with LSP framing, so the toggle read as harmless and every turn failed.
func (st *setupState) applyMCPEnableChange() error {
	servers := st.registeredMCPServers()
	if len(servers) == 0 {
		// Nothing registered means nothing can fail the handshake, and refusing here
		// would teach the user that MCP is broken when it is merely unused.
		fmt.Fprintln(st.out, "note: no MCP server is registered yet, so there is nothing to verify — an integration registers one when it is added ('piccolo integration add <pkg>').")
		return st.writeMCPEnabled(true, "mcp tool enabled (no servers registered yet)")
	}

	_, dir, runner, ctx, err := containerContext(st.cmd)
	if err != nil {
		return err
	}
	layout := workspace.NewLayout(dir)
	m, err := manifest.Load(layout.Manifest())
	if err != nil {
		return err
	}

	problems, verified, unverifiable := verifyMCPEnableChange(ctx, runner, m.Container.Name, layout, m, servers, st.out)
	if unverifiable {
		return st.confirmUnverifiedMCPEnable(fmt.Errorf("no container engine reachable, so these servers could not be verified"), servers)
	}

	if len(problems) == 0 {
		for _, line := range verified {
			fmt.Fprintf(st.out, "  ✓ %s\n", line)
		}
		return st.writeMCPEnabled(true, "mcp tool enabled")
	}

	fmt.Fprintln(st.out, "x handshake verification failed:")
	for _, p := range problems {
		fmt.Fprintf(st.out, "    - %s\n", p)
	}
	for _, line := range verified {
		fmt.Fprintf(st.out, "    - %s\n", line)
	}
	return st.confirmUnverifiedMCPEnable(errors.New(strings.Join(problems, "; ")), servers)
}

// mcpVerifyEngine is the daemon surface verifyMCPEnableChange needs, kept narrow so
// the safety-critical decision is unit-testable without Docker — the same isolation
// rule the rest of Piccolo's docker access follows.
type mcpVerifyEngine interface {
	Ping(context.Context) error
	StateByName(context.Context, string) (docker.State, error)
	Run(context.Context, docker.Spec) (docker.Process, error)
	RemoveByPrefix(context.Context, string) ([]string, error)
}

// verifyMCPEnableChange handshakes every registered server in EACH image that could
// end up serving it: the one piccolo.yaml names, and the one the gateway is running
// when they differ. Probing only the manifest image is what would have let the real
// failure through — the new image answered initialize while the still-running old
// container replied in LSP framing, so a passing check sat beside a gateway that
// failed every message.
//
// It returns the problems found and the servers verified, each naming the image it
// was verified in: "verified" without a target is the claim that misled here.
func verifyMCPEnableChange(ctx context.Context, eng mcpVerifyEngine, containerName string, layout workspacepkg.Layout, m *manifest.Manifest, servers []mcpServerEntry, out io.Writer) (problems, verified []string, unverifiable bool) {
	if err := eng.Ping(ctx); err != nil {
		return nil, nil, true
	}

	type target struct{ label, image string }
	targets := []target{{"piccolo.yaml", m.Container.Image}}
	// Only a RUNNING container is a second target: a stopped leftover will not serve
	// the toggle, so probing its binaries would report a failure that cannot happen.
	if state, serr := eng.StateByName(ctx, containerName); serr == nil && state.Found && state.Running && state.Image != "" && state.Image != m.Container.Image {
		targets = append(targets, target{"the running gateway, which needs a restart", state.Image})
	}

	for _, t := range targets {
		probe := dockerMCPProbe{runner: eng, base: docker.Spec{
			Image:   t.image,
			Network: m.Container.Network,
			Source:  layout.PicoclawHome(),
			Volumes: integrationSetupBinds(layout),
			Labels:  map[string]string{docker.LabelWorkspace: layout.Root},
		}, out: out}
		for _, srv := range servers {
			detail, perr := probe.Probe(ctx, srv.key, srv.argv)
			if perr != nil {
				problems = append(problems, fmt.Sprintf("%s in %s (%s): %v", srv.key, t.label, t.image, perr))
				continue
			}
			verified = append(verified, fmt.Sprintf("%s answered initialize as %s — verified in %s (%s)", srv.key, detail, t.label, t.image))
		}
	}
	return problems, verified, false
}

// confirmUnverifiedMCPEnable puts the decision where it belongs — with the user — but
// defaults to No, because the known consequence of enabling against a server that
// cannot answer is that every chat message fails, not just this integration's.
func (st *setupState) confirmUnverifiedMCPEnable(reason error, servers []mcpServerEntry) error {
	fmt.Fprintf(st.out, "\nEnabling MCP anyway means PicoClaw fails EVERY chat message while %d server(s) cannot initialize — not only the ones that use them.\n", len(servers))
	confirm := false
	form := setupForm(huh.NewGroup(
		huh.NewConfirm().
			Title("Enable tools.mcp.enabled despite the failed verification?").
			Description(quoteForLog(reason.Error()) + "\n" + escBackHint + " — esc leaves MCP off").
			Affirmative("Yes, enable anyway").
			Negative("No, keep MCP off").
			Value(&confirm),
	))
	if err := form.Run(); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			// Backing out of a yes/no gate is a no, same as the config-write gate:
			// esc must not sound like a crash.
			fmt.Fprintln(st.out, "MCP left off.")
			return nil
		}
		return err
	}
	if !confirm {
		fmt.Fprintln(st.out, "MCP left off.")
		return nil
	}
	fmt.Fprintln(st.out, "enabled at your request; 'piccolo restart' re-checks the container that will serve traffic after any image change.")
	return st.writeMCPEnabled(true, "mcp tool enabled (verification declined by you)")
}

// writeMCPEnabled is the single place this screen turns the MCP tool on.
func (st *setupState) writeMCPEnabled(enabled bool, what string) error {
	setup.SetTool(st.doc, "mcp", enabled)
	return st.applyConfigAndAgentChanges(what, nil)
}

type mcpServerEntry struct {
	key  string
	argv []string
}

// registeredMCPServers lists every tools.mcp.servers entry Piccolo can spawn, in
// stable key order so a workspace with several integrations verifies them in the same
// sequence every time.
func (st *setupState) registeredMCPServers() []mcpServerEntry {
	return mcpServersInDoc(st.doc)
}

// mcpServersInDoc is the same list without a setupState, so the registry read is
// testable on its own: an entry with no spawnable command (sse, disabled, blank) is
// skipped rather than reported as a failure it cannot have.
func mcpServersInDoc(doc *configdoc.Doc) []mcpServerEntry {
	tools, ok := doc.GetObject("tools")
	if !ok {
		return nil
	}
	mcp, ok := tools.GetObject("mcp")
	if !ok {
		return nil
	}
	servers, ok := mcp.GetObject("servers")
	if !ok {
		return nil
	}
	var out []mcpServerEntry
	for _, key := range servers.Keys() {
		if argv := setup.MCPServerArgv(doc, key); len(argv) > 0 {
			out = append(out, mcpServerEntry{key: key, argv: argv})
		}
	}
	// Sorted, so a workspace with several integrations verifies them in the same order
	// every time: JSON object order is the user's, and a nondeterministic verification
	// sequence makes both logs and comparisons unreliable.
	sort.Slice(out, func(i, j int) bool { return out[i].key < out[j].key })
	return out
}

func (st *setupState) applyRemoteExecChange(enabled bool) error {
	setup.SetExecAllowRemote(st.doc, enabled)
	var changes []agentFileChange
	if enabled {
		setup.SetTool(st.doc, "exec", true)
		changes = st.agentToolAllowlistChanges("exec", true)
		setup.SetToolFeedback(st.doc, true)
		fmt.Fprintln(st.out, "warning: remote shell execution allows Discord/Telegram/etc. chats to request shell commands inside the container subject to PicoClaw safety checks; enable only for trusted channels/users. Tool feedback was enabled for audit/debug visibility.")
	}
	return st.applyConfigAndAgentChanges(fmt.Sprintf("remote exec %s", yesNo(enabled)), changes)
}

// applyWorkspaceRestrictionChange writes agents.defaults.restrict_to_workspace.
// Disabling it is Piccolo's generated default and its recommendation: the
// container is the security boundary, and PicoClaw's exec guard trips on ordinary
// commands (any absolute path) while leaving the agent with an error it cannot
// read. Enabling it is a legitimate hardening choice, so only the loosening
// direction carries a note.
func (st *setupState) applyWorkspaceRestrictionChange(enabled bool) error {
	setup.SetWorkspaceRestricted(st.doc, enabled)
	if !enabled {
		fmt.Fprintln(st.out, "note: agents can now read, write, and run commands anywhere in the container; the container boundary is the only guard. Apply with: piccolo reload (or piccolo restart if reload fails)")
	} else {
		fmt.Fprintln(st.out, "note: with the path guard ON, PicoClaw refuses shell commands naming paths outside each agent workspace, and allow_read_outside_workspace does not relax that exec check. Apply with: piccolo reload (or piccolo restart if reload fails)")
	}
	return st.applyConfigAndAgentChanges(fmt.Sprintf("workspace path guard %s", yesNo(enabled)), nil)
}

func (st *setupState) applyConfigAndAgentChanges(what string, agentChanges []agentFileChange) error {
	newBytes := st.doc.Marshal()
	configChanged := !bytes.Equal(st.orig, newBytes)
	if !configChanged && len(agentChanges) == 0 {
		fmt.Fprintf(st.out, "no actual change to workspace config (%s).\n", what)
		return nil
	}
	confirm := true
	desc := "updates config.json and matching AGENT.md tool allowlists; previous config.json is kept as config.json.bak"
	if !configChanged {
		desc = "updates matching AGENT.md tool allowlists only"
	} else if len(agentChanges) == 0 {
		desc = "your previous config.json is kept as config.json.bak"
	}
	if err := setupForm(huh.NewGroup(
		huh.NewConfirm().
			Title(fmt.Sprintf("Apply %s to workspace config?", what)).
			Description(desc).
			Affirmative("Yes, write it").
			Negative("No, discard").
			Value(&confirm),
	)).Run(); err != nil {
		return err
	}
	if !confirm {
		reverted, perr := configdoc.Parse(st.orig)
		if perr != nil {
			return perr
		}
		st.doc = reverted
		fmt.Fprintln(st.out, "discarded.")
		return nil
	}
	if configChanged {
		if err := configdoc.Write(st.path, st.doc); err != nil {
			return err
		}
		st.orig = newBytes
	}
	for _, ch := range agentChanges {
		if err := os.MkdirAll(filepath.Dir(ch.path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(ch.path, ch.content, 0o644); err != nil {
			return err
		}
		// Only content Piccolo RENDERED is worth a provenance entry. The agent
		// allowlist edits are merges into the user's file, so recording them would
		// label a user-customised file as pristine generation output — the one
		// distinction this record exists to keep.
		if rel, ok := st.renderedSkillRel(ch.path); ok {
			if err := generate.RecordGenerated(st.dir, rel, generate.SourceFileHandlingSkill, ch.content); err != nil {
				return err
			}
		}
	}
	st.write = true
	fmt.Fprintf(st.out, "✓ wrote %s", what)
	if len(agentChanges) > 0 {
		fmt.Fprintf(st.out, " and %d workspace file(s)", len(agentChanges))
	}
	fmt.Fprintln(st.out)
	return nil
}

// skillPath is the one generated skill root Piccolo loads: PicoClaw's home, not
// the workspace root. Deriving it here keeps the sync target and the provenance
// match on the same rule.
func (st *setupState) skillPath() string {
	return filepath.Join(workspacepkg.NewLayout(st.dir).Skills(), generate.FileHandlingSkillName, "SKILL.md")
}

// renderedSkillRel reports whether an absolute path is the workspace's generated
// file-handling skill, and returns its workspace-relative form.
func (st *setupState) renderedSkillRel(path string) (string, bool) {
	want := filepath.Join(workspacepkg.PicoclawDir, "skills", generate.FileHandlingSkillName, "SKILL.md")
	got, err := filepath.Rel(st.dir, path)
	if err != nil {
		return "", false
	}
	if filepath.ToSlash(filepath.Clean(got)) == filepath.ToSlash(filepath.Clean(want)) {
		return want, true
	}
	return "", false
}

func (st *setupState) applyAgentFileChanges(what string, agentChanges []agentFileChange) error {
	return st.applyConfigAndAgentChanges(what, agentChanges)
}

func (st *setupState) agentToolAllowlistChanges(configTool string, enabled bool) []agentFileChange {
	frontTools := frontmatterToolsForConfigTool(configTool)
	if len(frontTools) == 0 {
		return nil
	}
	coord := st.coordinatorMDPath()
	var changes []agentFileChange
	for _, path := range st.agentMDPaths() {
		if path == coord {
			// The coordinator declares no allowlist on purpose, so that every tool
			// config.json enables — including the mcp_<server>_<tool> names no static
			// list can predict — reaches it. Writing a name here would convert "no
			// allowlist" into a strict one and silently revoke every integration tool.
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		updated, changed := updateAgentToolsFrontmatter(string(data), frontTools, enabled)
		if changed {
			changes = append(changes, agentFileChange{path: path, content: []byte(updated)})
		}
	}
	return changes
}

func frontmatterToolsForConfigTool(configTool string) []string {
	switch configTool {
	case "web":
		return []string{"web_search", "web_fetch"}
	case "exec":
		return []string{"exec"}
	case "spawn":
		return []string{"spawn"}
	case "subagent":
		return []string{"subagent"}
	case "load_image":
		return []string{"load_image"}
	case "mcp":
		// No frontmatter name exists for MCP. The registry matches tool names
		// exactly (pkg/tools/registry.go:182), and MCP tools are named
		// mcp_<server>_<tool> at runtime, so writing "mcp" here would declare an
		// allowlist that admits nothing. Enabling the toggle is the whole action.
		return nil
	default:
		return nil
	}
}

// coordinatorMDPath returns the AGENT.md of the default agent — the coordinator
// Piccolo generates as `main` — or "" when no entry is marked default.
func (st *setupState) coordinatorMDPath() string {
	agents, ok := st.doc.GetObject("agents")
	if !ok {
		return ""
	}
	defaults, _ := agents.GetObject("defaults")
	defaultWS := ""
	if defaults != nil {
		defaultWS, _ = defaults.GetString("workspace")
	}
	listRaw, ok := agents.Get("list")
	if !ok {
		return ""
	}
	list, ok := listRaw.([]configdoc.Value)
	if !ok {
		return ""
	}
	for _, item := range list {
		ag, ok := item.(*configdoc.Doc)
		if !ok {
			continue
		}
		if isDefault, ok := ag.GetBool("default"); !ok || !isDefault {
			continue
		}
		ws, _ := ag.GetString("workspace")
		if ws == "" {
			ws = defaultWS
		}
		host, ok := setupWorkspacePath(st.dir, ws)
		if !ok {
			return ""
		}
		return filepath.Join(host, "AGENT.md")
	}
	return ""
}

func (st *setupState) agentMDPaths() []string {
	var paths []string
	for _, dir := range st.agentWorkspaceDirs() {
		paths = append(paths, filepath.Join(dir, "AGENT.md"))
	}
	return paths
}

func (st *setupState) agentWorkspaceDirs() []string {
	agents, ok := st.doc.GetObject("agents")
	if !ok {
		return nil
	}
	defaults, _ := agents.GetObject("defaults")
	defaultWS := ""
	if defaults != nil {
		defaultWS, _ = defaults.GetString("workspace")
	}
	listRaw, ok := agents.Get("list")
	if !ok {
		return nil
	}
	list, ok := listRaw.([]configdoc.Value)
	if !ok {
		return nil
	}
	seen := map[string]bool{}
	var dirs []string
	for _, item := range list {
		ag, ok := item.(*configdoc.Doc)
		if !ok {
			continue
		}
		ws, _ := ag.GetString("workspace")
		if ws == "" {
			ws = defaultWS
		}
		host, ok := setupWorkspacePath(st.dir, ws)
		if !ok {
			continue
		}
		if !seen[host] {
			dirs = append(dirs, host)
			seen[host] = true
		}
	}
	return dirs
}

func setupWorkspacePath(workspaceRoot, containerPath string) (string, bool) {
	p := strings.TrimSpace(containerPath)
	layout := workspace.NewLayout(workspaceRoot)
	for _, prefix := range []string{"~/.picoclaw/", "/root/.picoclaw/"} {
		if rest, ok := strings.CutPrefix(p, prefix); ok {
			return filepath.Join(layout.PicoclawHome(), filepath.FromSlash(rest)), true
		}
	}
	return "", false
}

var (
	toolsLineRE  = regexp.MustCompile(`(?m)^tools:\s*\[([^\]]*)\]\s*$`)
	skillsLineRE = regexp.MustCompile(`(?m)^skills:\s*\[([^\]]*)\]\s*$`)
)

func updateAgentToolsFrontmatter(content string, tools []string, enabled bool) (string, bool) {
	return updateAgentFrontmatterList(content, toolsLineRE, "tools", tools, enabled)
}

func updateAgentSkillsFrontmatter(content string, skills []string, enabled bool) (string, bool) {
	return updateAgentFrontmatterList(content, skillsLineRE, "skills", skills, enabled)
}

func updateAgentFrontmatterList(content string, lineRE *regexp.Regexp, key string, values []string, enabled bool) (string, bool) {
	if !strings.HasPrefix(content, "---\n") {
		return content, false
	}
	rest := content[len("---\n"):]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return content, false
	}
	fm := rest[:end]
	body := rest[end:]
	match := lineRE.FindStringSubmatchIndex(fm)
	if match == nil {
		if !enabled {
			return content, false
		}
		line := key + ": [" + strings.Join(values, ", ") + "]"
		return "---\n" + strings.TrimRight(fm, "\n") + "\n" + line + body, true
	}
	currentRaw := fm[match[2]:match[3]]
	var current []string
	present := map[string]bool{}
	for _, part := range strings.Split(currentRaw, ",") {
		t := strings.TrimSpace(part)
		if t == "" {
			continue
		}
		current = append(current, t)
		present[t] = true
	}
	changed := false
	if enabled {
		for _, t := range values {
			if !present[t] {
				current = append(current, t)
				present[t] = true
				changed = true
			}
		}
	} else {
		remove := map[string]bool{}
		for _, t := range values {
			remove[t] = true
		}
		kept := current[:0]
		for _, t := range current {
			if remove[t] {
				changed = true
				continue
			}
			kept = append(kept, t)
		}
		current = kept
	}
	if !changed {
		return content, false
	}
	newLine := key + ": [" + strings.Join(current, ", ") + "]"
	newFM := fm[:match[0]] + newLine + fm[match[1]:]
	return "---\n" + newFM + body, true
}

// ---------- write path ----------

// setNotice stages a message for the next screen's help line.
// credentialStorageNote says where the value being typed will land. The static
// help string claimed plaintext unconditionally, which became false the moment a
// workspace opted into enc:// storage — and a user who believes a field holds
// plaintext behaves differently around it (sharing it, backing it up, pasting it
// into an issue) than one who believes it is sealed.
// sealBeforeWrite converts any plaintext credential the forms just stored into the
// enc:// form, for a workspace that opted in. It is a no-op when the workspace is
// not encrypted, and it uses the same passphrase serve will deliver — the one in
// piccolo.yaml — so there is no second passphrase to reconcile.
func (st *setupState) sealBeforeWrite(what string) error {
	if !st.encrypted {
		return nil
	}
	pass := st.manifest.Passphrase()
	if pass == "" {
		// Loud, because silently writing plaintext in an encrypted workspace is the
		// one outcome the user cannot detect later.
		return errors.New("this workspace stores credentials as enc:// but piccolo.yaml has no secrets.passphrase — " +
			"set it and repeat the change, so the credential written here is one the gateway can decrypt")
	}
	key := workspace.NewLayout(st.dir).KeyFile()
	res, err := setup.EncryptWorkspace(st.doc, st.secrets, key, pass)
	if err != nil {
		return fmt.Errorf("encrypt %s: %w", what, err)
	}
	for _, r := range res.Refused {
		// The field was already refused by the form-level guard in most cases; this
		// catches anything that reached the document another way.
		fmt.Fprintf(st.out, "left unencrypted: %s — %s\n", r.Field, r.Reason)
	}
	if len(res.Changed) > 0 {
		fmt.Fprintf(st.out, "encrypted %d credential(s) for this workspace\n", len(res.Changed))
	}
	return nil
}

func (st *setupState) credentialStorageNote(fallback string) string {
	if st.encrypted {
		return "encrypted as PicoClaw enc:// before it is written; the passphrase lives in piccolo.yaml"
	}
	if fallback != "" {
		return fallback
	}
	return "stored plaintext in config.json (git-ignored); 'piccolo secrets encrypt' switches this workspace to enc://"
}

func (st *setupState) setNotice(format string, args ...any) {
	st.notice = fmt.Sprintf(format, args...)
}

func (st *setupState) applyManifestAndConfirm(what string) error {
	confirm := true
	err := setupForm(huh.NewGroup(
		huh.NewConfirm().
			Title(fmt.Sprintf("Apply %s to piccolo.yaml?", what)).
			Description("updates container launch settings; restart required").
			Affirmative("Yes, write it").
			Negative("No, discard").
			Value(&confirm),
	)).Run()
	if err != nil {
		return err
	}
	if !confirm {
		fresh, err := manifest.Load(st.manifestPath)
		if err != nil {
			return err
		}
		st.manifest = fresh
		fmt.Fprintln(st.out, "discarded.")
		return nil
	}
	if err := st.manifest.Write(st.manifestPath); err != nil {
		return fmt.Errorf("write piccolo.yaml: %w", err)
	}
	st.write = true
	fmt.Fprintf(st.out, "✓ wrote %s\n", what)
	return nil
}

// applyAndConfirm writes the merge-patched config.json on explicit confirmation
// (no diff preview — the change summary is shown in the confirm title, and the
// previous file is always kept as config.json.bak).
func (st *setupState) applyAndConfirm(what string) error {
	// An encrypted workspace writes ciphertext, so the value a form collected in
	// plaintext is sealed here rather than at each call site: the model form and the
	// channel form both arrive through this function, and encrypting in only one of
	// them is how two paths start disagreeing about the same workspace posture.
	// Refusing to encrypt a credential it cannot protect is worse than writing
	// plaintext nobody asked to seal, so a failure here aborts the write.
	if err := st.sealBeforeWrite(what); err != nil {
		return err
	}
	newBytes := st.doc.Marshal()
	configChanged := !bytes.Equal(st.orig, newBytes)
	manifestChanged := st.pendingDiscordApplicationID != "" && st.pendingDiscordApplicationID != strings.TrimSpace(st.manifest.Setup.Channels.Discord.ApplicationID)
	if !configChanged && !manifestChanged {
		fmt.Fprintf(st.out, "no actual change to workspace config (%s).\n", what)
		if st.pendingDiscordApplicationID != "" {
			st.printDiscordNextSteps(st.pendingDiscordApplicationID, st.pendingDiscordMentionOnly)
			st.pendingDiscordApplicationID = ""
		}
		return nil
	}

	confirm := true
	desc := "your previous config.json is kept as config.json.bak"
	if !configChanged {
		desc = "updates piccolo.yaml setup metadata only"
	} else if manifestChanged {
		desc = "updates config.json and piccolo.yaml; your previous config.json is kept as config.json.bak"
	}
	err := setupForm(huh.NewGroup(
		huh.NewConfirm().
			Title(fmt.Sprintf("Apply %s to workspace config?", what)).
			Description(desc).
			Affirmative("Yes, write it").
			Negative("No, discard").
			Value(&confirm),
	)).Run()
	if err != nil {
		return err
	}
	if !confirm {
		// Revert in-memory to the last written state.
		reverted, perr := configdoc.Parse(st.orig)
		if perr != nil {
			return perr
		}
		st.doc = reverted
		st.pendingDiscordApplicationID = ""
		fmt.Fprintln(st.out, "discarded.")
		return nil
	}
	if configChanged {
		if err := configdoc.Write(st.path, st.doc); err != nil {
			return err
		}
		st.orig = newBytes
	}
	if manifestChanged {
		st.manifest.Setup.Channels.Discord.ApplicationID = st.pendingDiscordApplicationID
		if err := st.manifest.Write(st.manifestPath); err != nil {
			return fmt.Errorf("write piccolo.yaml: %w", err)
		}
	}
	st.write = true
	fmt.Fprintf(st.out, "\u2713 wrote %s\n", what)
	if st.pendingDiscordApplicationID != "" {
		st.printDiscordNextSteps(st.pendingDiscordApplicationID, st.pendingDiscordMentionOnly)
		st.pendingDiscordApplicationID = ""
	}
	return nil
}

// errCancel signals "back to parent screen" from any nested flow.
var errCancel = fmt.Errorf("cancelled")

func isCancel(err error) bool {
	if err == nil {
		return false
	}
	return err == errCancel || strings.Contains(err.Error(), "cancelled") || strings.Contains(err.Error(), "user aborted")
}

var _ = term.IsTerminal // ensure x/term stays imported

func orDefault(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}

// applyEncryptionChange turns enc:// credential storage on or off for this
// workspace.
//
// Enabling asks for a passphrase, then encrypts whatever plaintext is already in
// config.json — a toggle that only affected future writes would leave the
// credentials sitting exactly where they were while the menu said "on".
//
// Disabling is deliberately incomplete: Piccolo cannot decrypt the existing
// enc:// values back to plaintext without also rewriting the file, and it has no
// way to hand them to anything else first. So it records the choice, leaves the
// ciphertext alone, and says so. Reverting a workspace to plaintext is
// re-entering the credentials, which is the user's call to make knowingly rather
// than something a toggle does underneath them.
func (st *setupState) applyEncryptionChange(enable bool) error {
	if !enable {
		if st.manifest.Passphrase() == "" {
			fmt.Fprintln(st.out, "credential encryption is already off.")
			return nil
		}
		confirm := false
		err := setupForm(huh.NewGroup(
			huh.NewConfirm().
				Title("Turn off credential encryption?").
				Description("Existing enc:// values stay as they are — Piccolo will not decrypt and rewrite them. " +
					"Credentials entered from now on would be plaintext in config.json.").
				Affirmative("Yes, turn it off").
				Negative("No, keep it on").
				Value(&confirm),
		)).Run()
		if err != nil {
			return err
		}
		if !confirm {
			return errCancel
		}
		st.manifest.Secrets.Passphrase = ""
		if err := st.manifest.Write(st.manifestPath); err != nil {
			return fmt.Errorf("write piccolo.yaml: %w", err)
		}
		st.recomputeEncrypted()
		fmt.Fprintln(st.out, "✓ encryption off; any enc:// credential still in config.json stays encrypted and keeps working")
		st.setNotice("Encryption is off, but existing enc:// values were not rewritten. To return to plaintext, re-enter each credential through 'piccolo setup' → Models")
		st.write = true
		return nil
	}

	if st.manifest.Passphrase() != "" {
		// Already on. Offer to encrypt anything added since, rather than pretending
		// the toggle did nothing.
		if err := st.encryptExistingCredentials(""); err != nil {
			return err
		}
		st.recomputeEncrypted()
		return nil
	}

	passphrase, err := st.promptForNewPassphrase()
	if err != nil {
		return err
	}
	if err := st.encryptExistingCredentials(passphrase); err != nil {
		return err
	}
	st.manifest.Secrets.Passphrase = passphrase
	if err := st.manifest.Write(st.manifestPath); err != nil {
		return fmt.Errorf("write piccolo.yaml: %w", err)
	}
	st.recomputeEncrypted()
	st.write = true
	fmt.Fprintln(st.out, "✓ recorded secrets.passphrase in piccolo.yaml")
	fmt.Fprintln(st.out, "  note: that file is committed on purpose, so a copy of this workspace can still be decrypted.")
	return nil
}

// encryptExistingCredentials generates the key if the workspace has none and
// seals every plaintext credential in config.json. Passing "" uses the passphrase
// already recorded, which is the re-run path.
func (st *setupState) encryptExistingCredentials(passphrase string) error {
	if passphrase == "" {
		passphrase = st.manifest.Passphrase()
	}
	if strings.TrimSpace(passphrase) == "" {
		return errors.New("no passphrase to encrypt with")
	}
	layout := workspace.NewLayout(st.dir)
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
		fmt.Fprintf(st.out, "credential key: %s\n", rel(st.dir, keyPath))
	}
	res, err := setup.EncryptWorkspace(st.doc, st.secrets, keyPath, passphrase)
	if err != nil {
		return err
	}
	if res.NothingChanged() {
		fmt.Fprintln(st.out, "nothing left to encrypt: every credential in config.json is already ciphertext or empty.")
	} else {
		if err := configdoc.Write(st.path, st.doc); err != nil {
			return err
		}
		st.orig = st.doc.Marshal()
		fmt.Fprintf(st.out, "✓ encrypted %d credential(s) in %s\n", len(res.Changed), rel(st.dir, st.path))
	}
	for _, r := range res.Refused {
		fmt.Fprintf(st.out, "  left in plaintext: %s — %s\n", r.Field, r.Reason)
	}
	return nil
}

// promptForNewPassphrase asks twice: the value is about to be written into a file
// the user may commit without thinking again, and a typo becomes the only way in.
func (st *setupState) promptForNewPassphrase() (string, error) {
	var pass, conf string
	err := setupForm(
		huh.NewGroup(huh.NewInput().
			Title("Passphrase for this workspace's credentials").
			Description("Stored in piccolo.yaml so the gateway starts without any host setup.\n"+
				"Losing it is permanent: the credentials must then be re-entered.").
			EchoMode(huh.EchoModePassword).Value(&pass)),
		huh.NewGroup(huh.NewInput().
			Title("Confirm the passphrase").
			EchoMode(huh.EchoModePassword).Value(&conf)),
	).Run()
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(pass) == "" {
		return "", errors.New("a blank passphrase would seal credentials nothing can later decrypt")
	}
	if pass != conf {
		return "", errors.New("the passphrase and its confirmation did not match")
	}
	return pass, nil
}

// recomputeEncrypted re-derives the posture flag after piccolo.yaml or the key
// file moved, so menus, field help and sealing all follow the new state.
func (st *setupState) recomputeEncrypted() {
	_, statErr := os.Stat(workspace.NewLayout(st.dir).KeyFile())
	st.encrypted = statErr == nil && st.manifest.Passphrase() != ""
}
