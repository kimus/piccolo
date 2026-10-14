package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/kimus/piccolo/internal/configdoc"
	"github.com/kimus/piccolo/internal/generate"
	"github.com/kimus/piccolo/internal/manifest"
	"github.com/kimus/piccolo/internal/runtimeimage"
	"github.com/kimus/piccolo/internal/setup"
	"github.com/kimus/piccolo/internal/wizard"
	"github.com/kimus/piccolo/internal/workspace"
)

func newSetupCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "setup [section] [name]",
		Short: "Edit an existing workspace (channels, models, tools)",
		Long: "Interactive counterpart of piccolo init: an arrow-navigable editor for " +
			"config.json settings. Open 'piccolo setup' for the home menu, or deep-link: " +
			"'piccolo setup channels', 'piccolo setup channels discord', " +
			"'piccolo setup models', 'piccolo setup tools'.\n\n" +
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
	cmd   *cobra.Command
	out   *os.File
	dir   string
	path  string // config.json
	doc   *configdoc.Doc
	orig  []byte // config.json as loaded (for diff + bak)
	write bool   // any confirmed write happened

	manifestPath string
	manifest     *manifest.Manifest

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
	st := &setupState{cmd: cmd, out: os.Stdout, dir: dir, path: path, doc: doc, orig: orig, manifestPath: manifestPath, manifest: mf}

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
	models := setup.Models(st.doc)
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
		var pick string
		form := setupForm(huh.NewGroup(
			huh.NewSelect[string]().
				Title("Container").
				Description("Container launch settings; esc back.").
				Options(
					huh.NewOption(fmt.Sprintf("Web console [%s] — %s browser dashboard", status, verb), "web_console:"+verb),
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
		_, verb, ok := strings.Cut(pick, ":")
		if !ok {
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
			fmt.Fprintln(st.out, "note: switched to the official launcher image so the web console listens on :18800. Run 'piccolo image build --set' after this if you want a local launcher runtime with Devbox tools.")
		} else {
			st.manifest.Container.Image = generate.LauncherImage
		}
	} else {
		st.manifest.Container.Ports = removeContainerPort(st.manifest.Container.Ports, 18800)
		if runtimeimage.IsLocalRuntimeImage(st.manifest.Container.Image) {
			st.manifest.Container.Image = generate.DefaultImage
			fmt.Fprintln(st.out, "note: switched to the official gateway image. Run 'piccolo image build --set' after this if you want a local gateway runtime with Devbox tools.")
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
	return fmt.Sprintf("web:%s exec:%s remote-exec:%s spawn:%s subagent:%s images:%s", on(t.Web), on(t.Exec), on(t.ExecAllowRemote), on(t.Spawn), on(t.Subagent), on(t.LoadImage))
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
			desc := orDefault(f.Help, "stored plaintext in config.json (git-ignored)")
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
			st.checkDiscordToken(token)
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
		models := setup.Models(st.doc)
		imageModel := strings.TrimSpace(setup.DefaultImageModel(st.doc))
		imageLabel := "not set"
		if imageModel != "" {
			imageLabel = imageModel
		}
		opts := []huh.Option[string]{huh.NewOption("+ Add a model…", "__add__")}
		for _, m := range models {
			state := "no key!"
			if m.KeySet {
				state = "key set"
			}
			opts = append(opts, huh.NewOption(fmt.Sprintf("%-16s %s (%s)", m.Alias, state, modelRef(m)), m.Alias))
		}
		opts = append(opts,
			huh.NewOption(fmt.Sprintf("Set image/vision model… (%s)", imageLabel), "__image_model__"),
			huh.NewOption("← Back", "__back__"),
		)

		var pick string
		form := setupForm(huh.NewGroup(
			huh.NewSelect[string]().
				Title("Models").
				Description("agents use agents.defaults.model_name for text and agents.defaults.image_model for image attachments").
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
		case "__image_model__":
			if err := st.imageModelForm(); err != nil {
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

func (st *setupState) modelForm(alias string) error {
	var provider, model, base, key string
	if alias != "" {
		provider, model, key, base, _ = setup.ReadModel(st.doc, alias)
		// `key` keeps the stored value; a non-empty API key input overrides it.
	}
	var newKey string
	form := setupForm(
		huh.NewGroup(
			huh.NewInput().Title("Model alias (model_name in config.json)").
				Placeholder("gpt-5.4").Value(&alias),
			huh.NewInput().Title("Provider").
				Description("e.g. openai, openrouter, anthropic — sent to config.json as an explicit provider").
				Placeholder("openai").Value(&provider),
			huh.NewInput().Title("Model id").
				Description("the provider-side model id, without the provider prefix").
				Placeholder("gpt-5.4").Value(&model),
			huh.NewInput().Title("API key").
				Description("stored plaintext in config.json (git-ignored)").
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
		key = newKey
	}
	setup.PatchModel(st.doc, alias, strings.TrimSpace(provider), strings.TrimSpace(model), key, strings.TrimSpace(base))
	return st.applyAndConfirm("model " + alias)
}

func (st *setupState) imageModelForm() error {
	models := setup.Models(st.doc)
	if len(models) == 0 {
		return fmt.Errorf("add a model before setting agents.defaults.image_model")
	}
	current := strings.TrimSpace(setup.DefaultImageModel(st.doc))
	if current == "" {
		current = models[0].Alias
	}
	opts := make([]huh.Option[string], 0, len(models))
	for _, m := range models {
		label := fmt.Sprintf("%s (%s)", m.Alias, modelRef(m))
		opts = append(opts, huh.NewOption(label, m.Alias))
	}
	form := setupForm(huh.NewGroup(
		huh.NewSelect[string]().
			Title("Image/vision model").
			Description("Recommended: choose a multimodal model. PicoClaw uses this for image attachments from Discord/Telegram/etc.").
			Options(opts...).
			Value(&current),
	))
	if err := form.Run(); err != nil {
		return err
	}
	current = strings.TrimSpace(current)
	if current == "" {
		return fmt.Errorf("image model alias is required")
	}
	setup.SetDefaultImageModel(st.doc, current)
	return st.applyAndConfirm("image model " + current)
}

// ---------- tools ----------

func (st *setupState) toolsScreen() error {
	for {
		cur := setup.GetTools(st.doc)
		state := map[string]*bool{"web": cur.Web, "exec": cur.Exec, "spawn": cur.Spawn, "subagent": cur.Subagent, "load_image": cur.LoadImage}
		opts := make([]huh.Option[string], 0, len(setup.ToolNames)+3)
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
	return st.applyAgentFileChanges("agent tool allowlists, file-handling skill, and workspace Devbox config", changes)
}

func (st *setupState) syncEnabledToolWorkspaceChanges() ([]agentFileChange, error) {
	setup.EnsureMediaReadConfig(st.doc)
	cur := setup.GetTools(st.doc)
	state := map[string]*bool{"web": cur.Web, "exec": cur.Exec, "spawn": cur.Spawn, "subagent": cur.Subagent, "load_image": cur.LoadImage}
	var enabledTools []string
	for _, spec := range setup.ToolNames {
		if p := state[spec.Key]; p != nil && *p {
			enabledTools = append(enabledTools, spec.Key)
		}
	}
	var changes []agentFileChange
	for _, path := range st.agentMDPaths() {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		content := string(data)
		changed := false
		for _, key := range enabledTools {
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
	skillPath := filepath.Join(st.dir, "skills", generate.FileHandlingSkillName, "SKILL.md")
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
	}
	st.write = true
	fmt.Fprintf(st.out, "✓ wrote %s", what)
	if len(agentChanges) > 0 {
		fmt.Fprintf(st.out, " and %d workspace file(s)", len(agentChanges))
	}
	fmt.Fprintln(st.out)
	return nil
}

func (st *setupState) applyAgentFileChanges(what string, agentChanges []agentFileChange) error {
	return st.applyConfigAndAgentChanges(what, agentChanges)
}

func (st *setupState) agentToolAllowlistChanges(configTool string, enabled bool) []agentFileChange {
	frontTools := frontmatterToolsForConfigTool(configTool)
	if len(frontTools) == 0 {
		return nil
	}
	var changes []agentFileChange
	for _, path := range st.agentMDPaths() {
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
	default:
		return nil
	}
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
