package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/kimus/piccolo/internal/configdoc"
	"github.com/kimus/piccolo/internal/setup"
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

type setupState struct {
	cmd   *cobra.Command
	out   *os.File
	dir   string
	path  string // config.json
	doc   *configdoc.Doc
	orig  []byte // config.json as loaded (for diff + bak)
	write bool   // any confirmed write happened
}

func runSetup(cmd *cobra.Command, args []string) error {
	dir, err := workspace.Require(dirFlag)
	if err != nil {
		return err
	}
	path := filepath.Join(dir, "config.json")
	doc, err := configdoc.Load(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("no config.json in %s — run 'piccolo init' first", dir)
		}
		return fmt.Errorf("config.json does not parse (fix it manually first): %w", err)
	}
	orig, _ := os.ReadFile(path)
	st := &setupState{cmd: cmd, out: os.Stdout, dir: dir, path: path, doc: doc, orig: orig}

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
		case "agents", "container", "identity":
			return fmt.Errorf("%q is not editable yet — edit %s / piccolo.yaml manually", args[0], "config.json")
		default:
			return fmt.Errorf("unknown setup section %q (want: channels, models, tools)", args[0])
		}
	}

	// Home menu loop.
	for {
		choice, err := st.homeMenu()
		if err != nil {
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
	fmt.Fprintf(st.out, "\n\u2713 config.json updated (previous copy: config.json.bak)\n")
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
	form := huh.NewForm(huh.NewGroup(
		huh.NewSelect[string]().
			Title("Piccolo setup — what do you want to change?").
			Description("↑/↓ move · enter open · q quit").
			Options(
				huh.NewOption(fmt.Sprintf("Channels   (%s)", chansSummary), "channels"),
				huh.NewOption(fmt.Sprintf("Models     (%s)", modelSummary), "models"),
				huh.NewOption(fmt.Sprintf("Tools      (%s)", toolSummary), "tools"),
				huh.NewOption("Agents       (not editable yet — edit config.json manually)", "__agents__"),
				huh.NewOption("Container    (not editable yet — edit piccolo.yaml manually)", "__container__"),
				huh.NewOption("Identity     (not editable yet — edit agents/*/SOUL.md manually)", "__identity__"),
				huh.NewOption("Quit", "__quit__"),
			).
			Value(&choice),
	))
	if err := form.Run(); err != nil {
		return "", err
	}
	switch choice {
	case "__agents__", "__container__", "__identity__":
		fmt.Fprintln(st.out, "Agents/Container/Identity editing is not implemented yet — edit config.json / piccolo.yaml directly; piccolo doctor validates your changes.")
		return "__back__", nil
	}
	return choice, nil
}

func toolStateSummary(t setup.ToolsState) string {
	on := func(b *bool) string {
		if b != nil && *b {
			return "on"
		}
		return "off"
	}
	return fmt.Sprintf("web:%s exec:%s spawn:%s subagent:%s", on(t.Web), on(t.Exec), on(t.Spawn), on(t.Subagent))
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
			form := huh.NewForm(huh.NewGroup(
				huh.NewSelect[string]().
					Title("Channels").
					Description("↑/↓ move · enter manage · q back").
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
	form := huh.NewForm(huh.NewGroup(
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
	form := huh.NewForm(huh.NewGroup(
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
	fmt.Fprintf(st.out, "\n\u2500\u2500 Set up %s \u2500\u2500 steps to do FIRST (piccolo cannot do them for you):\n", t.Label)
	for _, s := range t.Steps {
		fmt.Fprintf(st.out, "  %s\n", s)
	}
	fmt.Fprintln(st.out)

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
	groups := []*huh.Group{
		// Navigating away from a confirm commits it, so the hide func
		// reads the fresh restrict value when the next group renders.
		huh.NewGroup(
			huh.NewConfirm().
				Title("Restrict the bot to specific user IDs?").
				Description("Recommended: yes. If NO, ANYONE who can message the bot can talk to your assistant.").
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
		groups = append(groups, huh.NewGroup(
			huh.NewConfirm().
				Title("Require @mentions in group/channel conversations?").
				Description("DMs always work; this controls group channels.").
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

	if err := huh.NewForm(groups...).Run(); err != nil {
		return err
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

// ---------- models ----------

func (st *setupState) modelsScreen() error {
	for {
		models := setup.Models(st.doc)
		opts := []huh.Option[string]{huh.NewOption("+ Add a model…", "__add__")}
		for _, m := range models {
			state := "no key!"
			if m.KeySet {
				state = "key set"
			}
			opts = append(opts, huh.NewOption(fmt.Sprintf("%-16s %s (%s)", m.Alias, state, m.Model), m.Alias))
		}
		opts = append(opts, huh.NewOption("← Back", "__back__"))

		var pick string
		form := huh.NewForm(huh.NewGroup(
			huh.NewSelect[string]().Title("Models").Description("agents pick the default model in config.json agents.defaults").Options(opts...).Value(&pick),
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
	var model, base, key string
	if alias != "" {
		model, key, base, _ = setup.ReadModel(st.doc, alias)
		// `key` keeps the stored value; a non-empty API key input overrides it.
	}
	var newKey string
	form := huh.NewForm(
		huh.NewGroup(
			huh.NewInput().Title("Model alias (model_name in config.json)").
				Placeholder("gpt-5.4").Value(&alias),
			huh.NewInput().Title("Model reference (provider/model)").
				Placeholder("openai/gpt-5.4").Value(&model),
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
	setup.PatchModel(st.doc, alias, strings.TrimSpace(model), key, strings.TrimSpace(base))
	return st.applyAndConfirm("model " + alias)
}

// ---------- tools ----------

func (st *setupState) toolsScreen() error {
	cur := setup.GetTools(st.doc)
	state := map[string]*bool{"web": cur.Web, "exec": cur.Exec, "spawn": cur.Spawn, "subagent": cur.Subagent}
	answers := make([]bool, len(setup.ToolNames))
	var groups []*huh.Group
	for i, spec := range setup.ToolNames {
		val := false
		if p := state[spec.Key]; p != nil {
			val = *p
		}
		answers[i] = val
		groups = append(groups, huh.NewGroup(
			huh.NewConfirm().Title(spec.Label).Description(spec.Help).Value(&answers[i]),
		))
	}
	if err := huh.NewForm(groups...).Run(); err != nil {
		return err
	}
	changed := false
	for i, spec := range setup.ToolNames {
		p := state[spec.Key]
		if p == nil || *p != answers[i] {
			setup.SetTool(st.doc, spec.Key, answers[i])
			changed = true
		}
	}
	if !changed {
		fmt.Fprintln(st.out, "no tool changes.")
		return nil
	}
	return st.applyAndConfirm("tools")
}

// ---------- write path ----------

// applyAndConfirm writes the merge-patched config.json on explicit confirmation
// (no diff preview — the change summary is shown in the confirm title, and the
// previous file is always kept as config.json.bak).
func (st *setupState) applyAndConfirm(what string) error {
	newBytes := st.doc.Marshal()
	if bytes.Equal(st.orig, newBytes) {
		fmt.Fprintf(st.out, "no actual change to config.json (%s).\n", what)
		return nil
	}

	confirm := true
	err := huh.NewForm(huh.NewGroup(
		huh.NewConfirm().
			Title(fmt.Sprintf("Apply %s to %s?", what, filepath.Base(st.path))).
			Description("your previous config.json is kept as config.json.bak").
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
		fmt.Fprintln(st.out, "discarded.")
		return nil
	}
	if err := configdoc.Write(st.path, st.doc); err != nil {
		return err
	}
	st.orig = newBytes
	st.write = true
	fmt.Fprintf(st.out, "\u2713 wrote %s\n", what)
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
