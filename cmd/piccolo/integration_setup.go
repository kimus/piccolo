package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/huh"
	"golang.org/x/term"

	"github.com/kimus/piccolo/internal/configdoc"
	"github.com/kimus/piccolo/internal/docker"
	picintegration "github.com/kimus/piccolo/internal/integration"
	"github.com/kimus/piccolo/internal/manifest"
	"github.com/kimus/piccolo/internal/setup"
	"github.com/kimus/piccolo/internal/workspace"
)

// menuEntry is one rendered menu row with its gating decision already applied.
type menuEntry struct {
	Action  picintegration.Action
	Enabled bool
	Reason  string // why it is unavailable when Enabled is false
}

// buildMenu maps descriptor actions through live state facts. An action whose
// required facts are unmet stays visible but unselectable, with the missing
// prerequisite named, so users are never sent into a doomed flow.
func buildMenu(actions []picintegration.Action, state picintegration.State) []menuEntry {
	var out []menuEntry
	for _, a := range actions {
		if a.Role != picintegration.RoleSetup && a.Role != picintegration.RoleDiagnostic {
			continue // unknown roles are ignored, never an error
		}
		if err := a.Supported(); err != nil {
			out = append(out, menuEntry{Action: a, Enabled: false, Reason: err.Error()})
			continue
		}
		missing := state.Missing(a.Requires)
		if len(missing) > 0 {
			out = append(out, menuEntry{
				Action:  a,
				Enabled: false,
				Reason:  fmt.Sprintf("needs %s — run the action that sets it first", strings.Join(missing, ", ")),
			})
			continue
		}
		out = append(out, menuEntry{Action: a, Enabled: true})
	}
	return out
}

// selectability renders the label suffix shown in the menu for disabled rows.
func (e menuEntry) Title() string {
	if e.Enabled {
		return e.Action.Title
	}
	return fmt.Sprintf("%s — unavailable: %s", e.Action.Title, e.Reason)
}

// callbackPortFromURL extracts the loopback port Piccolo must listen on from an
// authorization URL's own redirect_uri. The port is never taken from any other
// field: an integration must not be able to ask Piccolo to bind an arbitrary
// host port.
func callbackPortFromURL(raw string) (int, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return 0, fmt.Errorf("parse authorization url: %w", err)
	}
	if u.Scheme != "https" && !isLoopbackHost(u.Hostname()) {
		return 0, fmt.Errorf("authorization url %q is neither https nor loopback", raw)
	}
	redirect := u.Query().Get("redirect_uri")
	if redirect == "" {
		return 0, fmt.Errorf("authorization url carries no redirect_uri")
	}
	r, err := url.Parse(redirect)
	if err != nil {
		return 0, fmt.Errorf("parse redirect_uri: %w", err)
	}
	if !isLoopbackHost(r.Hostname()) {
		return 0, fmt.Errorf("redirect_uri host %q is not loopback; refusing to bind a non-loopback listener", r.Hostname())
	}
	port, err := strconv.Atoi(r.Port())
	if err != nil || port <= 0 || port > 65535 {
		return 0, fmt.Errorf("redirect_uri port %q is not usable", r.Port())
	}
	return port, nil
}

func isLoopbackHost(host string) bool {
	switch strings.ToLower(host) {
	case "localhost", "127.0.0.1", "::1", "":
		return host != ""
	}
	return strings.HasPrefix(host, "127.")
}

// callbackPath rebuilds the request line Piccolo relays back into the action
// container. The query is carried verbatim: credential material is opaque and
// is never parsed, logged, or stored by Piccolo.
func callbackPath(rawQuery string) string {
	if rawQuery == "" {
		return "/"
	}
	return "/?" + rawQuery
}

// stageHostFile copies a user-selected host file into the integration state
// tree at the action-declared destination with owner-only permissions, and
// returns both the host path and the container-side path to hand back.
//
// confirm is asked before replacing an existing staged file: credentials are
// easy to overwrite by accident and the previous value is unrecoverable.
func stageHostFile(hostPath, stateDirRoot, destination, containerState string, confirm func(target string) (bool, error)) (host string, cont string, err error) {
	hostPath = strings.TrimSpace(hostPath)
	destination = strings.TrimSpace(destination)
	if hostPath == "" {
		return "", "", fmt.Errorf("no file selected")
	}
	if destination == "" {
		return "", "", fmt.Errorf("action declared no destination for the selected file")
	}
	info, err := os.Stat(hostPath)
	if err != nil {
		return "", "", fmt.Errorf("selected path %s cannot be read", hostPath)
	}
	if info.IsDir() {
		return "", "", fmt.Errorf("selected path %s is a directory, not a file", hostPath)
	}
	if filepath.IsAbs(destination) || strings.Contains(destination, "..") {
		return "", "", fmt.Errorf("destination %q must be relative to the integration state directory", destination)
	}
	target := filepath.Join(stateDirRoot, filepath.FromSlash(destination))
	if !strings.HasPrefix(target, filepath.Clean(stateDirRoot)+string(os.PathSeparator)) {
		return "", "", fmt.Errorf("destination %q escapes the integration state directory", destination)
	}
	if _, statErr := os.Stat(target); statErr == nil {
		if confirm == nil {
			return "", "", fmt.Errorf("%s already exists; refusing to overwrite it without confirmation", filepath.Base(target))
		}
		ok, cerr := confirm(target)
		if cerr != nil {
			return "", "", cerr
		}
		if !ok {
			return "", "", fmt.Errorf("keeping the existing %s; nothing was changed", filepath.Base(target))
		}
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return "", "", err
	}
	src, err := os.Open(hostPath)
	if err != nil {
		return "", "", fmt.Errorf("open %s: %w", hostPath, err)
	}
	defer src.Close()
	tmp := target + ".piccolo-tmp"
	dst, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return "", "", err
	}
	if _, err := io.Copy(dst, src); err != nil {
		_ = dst.Close()
		_ = os.Remove(tmp)
		return "", "", err
	}
	if err := dst.Close(); err != nil {
		_ = os.Remove(tmp)
		return "", "", err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		_ = os.Remove(tmp)
		return "", "", err
	}
	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp)
		return "", "", err
	}
	cont = containerState + "/" + filepath.ToSlash(destination)
	return target, cont, nil
}

// answersJSON builds the single document delivered to the action's stdin.
// Values travel here — never as argv or env — because both are readable
// through `docker top` and `docker inspect`.
func answersJSON(inputs []picintegration.ActionInput, values map[string]string) ([]byte, error) {
	out := map[string]string{}
	for _, in := range inputs {
		if v, ok := values[in.Key]; ok {
			out[in.Key] = v
		}
	}
	data, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("encode integration answers: %w", err)
	}
	return data, nil
}

// actionWaitBudget bounds the human-in-the-loop wait for a browser callback.
// Deliberately far longer than any machine timeout: a person has to click.
const actionWaitBudget = 5 * time.Minute

// integrationCallRequest is everything needed to run one action. It is a
// struct so the interactive entrypoint and tests drive the same code path.
type integrationCallRequest struct {
	Binary    string
	Args      []string
	Container string
	Answers   []byte
	Spec      docker.Spec
}

// runIntegrationAction starts the action container, streams events, and returns
// the collected reader. onEvent fires per event so the caller can print
// progress, surface links, or start the callback courier.
func runIntegrationAction(ctx context.Context, runner integrationRunner, req integrationCallRequest, out io.Writer, onEvent func(picintegration.Event)) (*picintegration.EventReader, error) {
	proc, err := runner.Run(ctx, req.Spec)
	if err != nil {
		return nil, err
	}
	defer func() { _ = proc.Shutdown(0) }()

	go func() {
		_, _ = io.Copy(proc.Stdin(), strings.NewReader(string(req.Answers)))
		_ = proc.Stdin().Close()
	}()
	// The integration's human logs are not protocol; pass them straight through.
	go func() { _, _ = io.Copy(out, proc.Stderr()) }()

	er := picintegration.NewEventReader(proc.Stdout())
	for {
		ev := er.Next()
		if ev == nil {
			break
		}
		if onEvent != nil {
			onEvent(*ev)
		}
	}
	code, err := proc.Wait(ctx)
	if err != nil {
		return nil, err
	}
	if e := er.ErrEvent(); e != nil {
		return er, fmt.Errorf("integration action failed: %s (%s)", e.Message, e.Code)
	}
	if code != 0 {
		return er, fmt.Errorf("integration action exited %d without reporting a result", code)
	}
	if er.Result() == nil && len(er.Logs) > 0 {
		fmt.Fprintf(out, "(unparsed output: %s)\n", strings.Join(er.Logs, " | "))
	}
	return er, nil
}

// relayCallbackIntoContainer delivers the captured query string into the still
// running action container's own loopback. Piccolo never interprets the query:
// the credential material belongs to the integration.
func relayCallbackIntoContainer(ctx context.Context, runner integrationRunner, container string, port int, rawQuery string) error {
	target := fmt.Sprintf("http://localhost:%d%s", port, callbackPath(rawQuery))
	session, err := runner.Exec(ctx, container, []string{"curl", "-fsS", "-o", "/dev/null", target}, false)
	if err != nil {
		return fmt.Errorf("relay authorization callback into %s: %w", container, err)
	}
	defer session.Close()
	code, err := session.Wait(ctx)
	if err != nil {
		return fmt.Errorf("relay authorization callback: %w", err)
	}
	if code != 0 {
		return fmt.Errorf("integration rejected the relayed callback (exit %d)", code)
	}
	return nil
}

// renderActionInputs builds a huh form from the action's declared inputs and
// returns answer values keyed by input. For a `path` input the returned value
// is the *container-side* path, staged under the integration state directory.
func renderActionInputs(action picintegration.Action, hostStateDir, containerState string, tty bool) (map[string]string, error) {
	if len(action.Inputs) == 0 {
		return map[string]string{}, nil
	}
	if !tty {
		return nil, fmt.Errorf("integration setup needs an interactive terminal — rerun from a shell with a TTY")
	}

	// One holder per input: values are only meaningful after the form runs.
	type holder struct {
		key   string
		kind  string
		text  string
		check bool
		multi []string
		input picintegration.ActionInput
	}
	holders := make([]*holder, 0, len(action.Inputs))
	var groups []*huh.Group

	for _, in := range action.Inputs {
		h := &holder{key: in.Key, kind: in.Type, input: in}
		holders = append(holders, h)
		switch in.Type {
		case "text", "secret":
			field := huh.NewInput().Title(in.Title)
			if in.Type == "secret" || in.Sensitive {
				field = field.EchoMode(huh.EchoModePassword)
			}
			groups = append(groups, huh.NewGroup(field.Value(&h.text)))
		case "confirm":
			groups = append(groups, huh.NewGroup(huh.NewConfirm().Title(in.Title).Value(&h.check)))
		case "select":
			groups = append(groups, huh.NewGroup(huh.NewSelect[string]().Title(in.Title).Options(stringOptions(in.Options, &h.text)...)))
		case "multiselect":
			groups = append(groups, huh.NewGroup(huh.NewMultiSelect[string]().Title(in.Title).Options(multiOptions(in.Options, &h.multi)...)))
		case "path":
			// The picker starts inside the integration's own state directory:
			// the user is choosing a host file to stage there, not browsing the
			// whole filesystem by default.
			picker := huh.NewFilePicker().Title(in.Title).Height(12).Picking(true).ShowHidden(true).ShowPermissions(true).FileAllowed(true).DirAllowed(false).CurrentDirectory(hostStateDir)
			if _, err := os.Stat(hostStateDir); err != nil {
				picker = picker.CurrentDirectory(homeOr(hostStateDir))
			}
			groups = append(groups, huh.NewGroup(picker.Value(&h.text)))
		default:
			return nil, fmt.Errorf("unsupported input type %q for %q", in.Type, in.Key)
		}
	}

	if err := huh.NewForm(groups...).Run(); err != nil {
		return nil, err
	}

	values := map[string]string{}
	for _, h := range holders {
		switch h.kind {
		case "confirm":
			values[h.key] = strconv.FormatBool(h.check)
		case "multiselect":
			values[h.key] = strings.Join(h.multi, ",")
		case "path":
			if strings.TrimSpace(h.text) == "" {
				continue
			}
			// Staging is Piccolo's job: it owns the mount that makes a host
			// file visible inside the container.
			_, cont, err := stageHostFile(h.text, hostStateDir, h.input.Destination, containerState, confirmOverwrite)
			if err != nil {
				return nil, err
			}
			values[h.key] = cont
		default:
			if h.text != "" {
				values[h.key] = h.text
			}
		}
	}
	return values, nil
}

func stringOptions(opts []string, target *string) []huh.Option[string] {
	_ = target
	out := make([]huh.Option[string], 0, len(opts))
	for _, o := range opts {
		out = append(out, huh.NewOption(o, o))
	}
	return out
}

func multiOptions(opts []string, target *[]string) []huh.Option[string] {
	_ = target
	out := make([]huh.Option[string], 0, len(opts))
	for _, o := range opts {
		out = append(out, huh.NewOption(o, o))
	}
	return out
}

// confirmOverwrite is the interactive gate used when a staged credential would
// replace an existing file.
func confirmOverwrite(target string) (bool, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return false, fmt.Errorf("%s already exists and no terminal is available to confirm replacing it", filepath.Base(target))
	}
	agree := false
	form := huh.NewForm(huh.NewGroup(
		huh.NewConfirm().
			Title(fmt.Sprintf("Replace the existing %s?", filepath.Base(target))).
			Description("the current credentials for this integration will be overwritten").
			Affirmative("Yes, replace").
			Negative("No, keep the current file").
			Value(&agree),
	))
	if err := form.Run(); err != nil {
		return false, err
	}
	return agree, nil
}

// homeOr falls back to the user's home directory when a preferred starting
// point does not exist yet.
func homeOr(fallback string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return home
	}
	return fallback
}

// applyIntegrationPatch validates and applies a proposed patch through the
// existing preview/confirm/backup path. Piccolo is the only config.json writer.
func applyIntegrationPatch(out io.Writer, configPath string, raw json.RawMessage, serverKey string, confirm func() (bool, error)) error {
	if len(raw) == 0 {
		return nil
	}
	if err := setup.ValidateIntegrationPatch(raw, serverKey); err != nil {
		return err
	}
	doc, err := configdoc.Load(configPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", filepath.Base(configPath), err)
	}
	before := doc.Marshal()
	written, err := setup.ApplyIntegrationPatch(doc, raw, serverKey)
	if err != nil {
		return err
	}
	if written == "" {
		return nil
	}
	after := doc.Marshal()
	if string(before) == string(after) {
		fmt.Fprintf(out, "config.json already registers %s; nothing to change\n", serverKey)
		return nil
	}
	fmt.Fprintf(out, "\nPlanned PicoClaw config change:\n%s\n", summarizeJSONDiff(before, after))
	ok, err := confirm()
	if err != nil {
		return err
	}
	if !ok {
		fmt.Fprintln(out, "discarded; integration state was left as-is")
		return nil
	}
	if err := configdoc.Write(configPath, doc); err != nil {
		return err
	}
	fmt.Fprintf(out, "✓ wrote config.json (previous copy: config.json.bak)\n")
	return nil
}

// summarizeJSONDiff reports added/changed top-level leaf paths only; it is a
// preview, not a full diff engine.
func summarizeJSONDiff(before, after []byte) string {
	var b, a map[string]any
	_ = json.Unmarshal(before, &b)
	_ = json.Unmarshal(after, &a)
	var lines []string
	collectDiff(nil, b, a, &lines)
	if len(lines) == 0 {
		return "  (no structural change)"
	}
	return "  " + strings.Join(lines, "\n  ")
}

func collectDiff(prefix []string, before, after map[string]any, lines *[]string) {
	seen := map[string]bool{}
	for _, k := range sortedKeys(before) {
		seen[k] = true
	}
	for _, k := range sortedKeys(after) {
		seen[k] = true
	}
	for _, k := range sortedKeysRaw(seen) {
		path := append(append([]string(nil), prefix...), k)
		bv, bok := before[k]
		av, aok := after[k]
		switch {
		case !bok:
			*lines = append(*lines, fmt.Sprintf("+ %s", strings.Join(path, ".")))
		case !aok:
			*lines = append(*lines, fmt.Sprintf("- %s", strings.Join(path, ".")))
		default:
			bm, bmap := bv.(map[string]any)
			am, amap := av.(map[string]any)
			if bmap && amap {
				collectDiff(path, bm, am, lines)
				continue
			}
			if fmt.Sprint(bv) != fmt.Sprint(av) {
				*lines = append(*lines, fmt.Sprintf("~ %s", strings.Join(path, ".")))
			}
		}
	}
}

func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func sortedKeysRaw(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// openInBrowser launches the user's browser when the session can actually show
// one; otherwise the caller only prints the URL.
func openInBrowser(ctx context.Context, rawURL string) error {
	if _, err := url.Parse(rawURL); err != nil {
		return err
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.CommandContext(ctx, "open", rawURL)
	case "linux":
		cmd = exec.CommandContext(ctx, "xdg-open", rawURL)
	default:
		return fmt.Errorf("no browser launcher for %s", runtime.GOOS)
	}
	return cmd.Run()
}

// configDirFlag extracts the state-directory flag pair from a declared setup
// command so derived `state`/`call` invocations address the same place the
// integration already told Piccolo to use.
func configDirFlag(setupCommand []string) []string {
	for i := 0; i+1 < len(setupCommand); i++ {
		if strings.HasPrefix(setupCommand[i], "-") && strings.Contains(strings.ToLower(setupCommand[i]), "config") {
			return []string{setupCommand[i], setupCommand[i+1]}
		}
	}
	return nil
}

// deriveArgv builds `<binary> <sub...> [config-dir pair]` from the integration's
// own declared setup command, so Piccolo never hardcodes an integration's
// binary name.
func deriveArgv(setupCommand []string, sub ...string) ([]string, error) {
	if len(setupCommand) == 0 {
		return nil, fmt.Errorf("integration declares no setup command to derive from")
	}
	argv := []string{setupCommand[0]}
	argv = append(argv, sub...)
	argv = append(argv, configDirFlag(setupCommand)...)
	return argv, nil
}

// runDeclarativeSetup drives an action-based integration: describe → state →
// menu → inputs → call → events → proposed config patch.
func runDeclarativeSetup(ctx context.Context, out io.Writer, runner integrationRunner, layout workspace.Layout, workspaceRoot string, m *manifest.Manifest, found *manifest.Integration, env []string, desc picintegration.Descriptor) error {
	bind := integrationSetupBinds(layout)
	source := layout.PicoclawHome()
	stateHost := layout.Integration(found.StatePathRelative())
	containerState := "/root/.piccolo/integrations/" + found.StatePathRelative()
	stdinTTY := term.IsTerminal(int(os.Stdin.Fd()))
	host := detectHostSession(envMapFromList(env), stdinTTY)
	// Refuse before any container work: without a terminal the menu cannot be
	// rendered, so fetching state first would only cost a cold start.
	if !stdinTTY {
		return fmt.Errorf("integration setup needs an interactive terminal — rerun from a shell with a TTY")
	}

	// 1. Live facts gate the menu.
	stateArgv, err := deriveArgv(found.SetupCommand, "state")
	if err != nil {
		return err
	}
	entry, cmdArgs := splitEntrypointCommand(stateArgv)
	stateSpec := docker.Spec{
		Name:       statusContainerName(found.Name),
		Image:      m.Container.Image,
		Source:     source,
		Volumes:    bind,
		Entrypoint: entry,
		Command:    cmdArgs,
		Labels:     map[string]string{docker.LabelWorkspace: workspaceRoot},
	}
	stateData, err := runContainerCapture(ctx, runner, stateSpec)
	if err != nil {
		return fmt.Errorf("integration state lookup failed; refusing to render a menu from assumed facts: %w", err)
	}
	state, err := picintegration.ParseState(stateData)
	if err != nil {
		return err
	}

	// 2. Menu.
	menu := buildMenu(desc.Actions, state)
	if len(menu) == 0 {
		return fmt.Errorf("integration declares no usable setup actions")
	}
	labels := make([]string, 0, len(menu))
	for _, e := range menu {
		labels = append(labels, e.Title())
	}
	var chosenLabel string
	form := huh.NewForm(huh.NewGroup(
		huh.NewSelect[string]().
			Title(fmt.Sprintf("%s — what do you want to do?", displayIntegration(*found))).
			Description("Credentials identify the app; login authorizes your Google account.").
			Options(huh.NewOptions(labels...)...).
			Value(&chosenLabel),
	))
	if err := form.Run(); err != nil {
		return err
	}
	var chosen *menuEntry
	for i := range menu {
		if menu[i].Title() == chosenLabel {
			chosen = &menu[i]
			break
		}
	}
	if chosen == nil {
		return fmt.Errorf("menu selection %q no longer matches the integration's actions", chosenLabel)
	}
	if !chosen.Enabled {
		return fmt.Errorf("action %q is unavailable: %s", chosen.Action.ID, chosen.Reason)
	}

	// 3. Inputs.
	values, err := renderActionInputs(chosen.Action, stateHost, containerState, stdinTTY)
	if err != nil {
		return err
	}
	answers, err := answersJSON(chosen.Action.Inputs, values)
	if err != nil {
		return err
	}

	// 4. Invoke, streaming events.
	callArgv, err := deriveArgv(found.SetupCommand, "call", chosen.Action.ID)
	if err != nil {
		return err
	}
	entry, cmdArgs = splitEntrypointCommand(callArgv)
	container := setupContainerName(found.Name)
	spec := docker.Spec{
		Name:       container,
		Image:      m.Container.Image,
		Network:    m.Container.Network,
		Source:     source,
		Ports:      found.SetupPorts,
		Env:        env,
		Volumes:    bind,
		Entrypoint: entry,
		Command:    cmdArgs,
		Labels:     map[string]string{docker.LabelWorkspace: workspaceRoot},
	}
	if removed, err := runner.RemoveByPrefix(ctx, docker.SetupContainerPrefix+sanitizeForName(found.Name)); err == nil && len(removed) > 0 {
		fmt.Fprintf(out, "Removed %d abandoned integration container(s): %s\n", len(removed), strings.Join(removed, ", "))
	}
	fmt.Fprintf(out, "Running %s for %s...\n", chosen.Action.ID, displayIntegration(*found))

	var courierErr error
	deadline := time.Now().Add(actionWaitBudget)
	onEvent := func(ev picintegration.Event) {
		switch ev.Type {
		case picintegration.EventStep:
			fmt.Fprintf(out, "  [%s] %s: %s\n", ev.Status, ev.ID, ev.Message)
		case picintegration.EventOpenURL:
			fmt.Fprintf(out, "\n%s\n", host.browserHint(ev.URL))
			if host.shouldLaunchBrowser() {
				if err := openInBrowser(ctx, ev.URL); err != nil {
					fmt.Fprintf(out, "  (could not launch a browser: %v)\n", err)
				}
			}
		case picintegration.EventWaitRedirect:
			port, err := callbackPortFromURL(ev.URL)
			if err != nil {
				courierErr = err
				return
			}
			c, err := newCourier(port)
			if err != nil {
				courierErr = err
				return
			}
			remaining := time.Until(deadline)
			if remaining < 0 {
				remaining = time.Second
			}
			relay := func(rctx context.Context, q string) error {
				return relayCallbackIntoContainer(rctx, runner, container, port, q)
			}
			if err := c.await(ctx, remaining, relay); err != nil {
				courierErr = err
			}
		case picintegration.EventError:
			fmt.Fprintf(out, "  ✗ %s\n", ev.Message)
			if ev.Remediation != "" {
				fmt.Fprintf(out, "    → %s\n", ev.Remediation)
			}
			if ev.RemediationURL != "" {
				fmt.Fprintf(out, "      %s %s\n", ev.Label, ev.RemediationURL)
			}
		}
	}

	reader, err := runIntegrationAction(ctx, runner, integrationCallRequest{
		Answers:   answers,
		Container: container,
		Spec:      spec,
	}, out, onEvent)
	if courierErr != nil {
		return fmt.Errorf("browser authorization did not complete: %w", courierErr)
	}
	if err != nil {
		return err
	}

	// 5. Configuration: the integration proposed, Piccolo applies.
	res := reader.Result()
	if res == nil || res.Result == nil {
		return nil
	}
	serverKey := integrationServerKey(*found, desc)
	confirm := func() (bool, error) { return confirmApplyConfig(out) }
	return applyIntegrationPatch(out, layout.Config(), res.Result.ConfigPatch, serverKey, confirm)
}

// integrationServerKey derives the MCP server key an integration owns, so the
// allowlist can pin the patch to exactly that entry and nothing else.
func integrationServerKey(in manifest.Integration, desc picintegration.Descriptor) string {
	if key := strings.TrimSpace(in.MCPServerKey); key != "" {
		return key
	}
	if key := strings.TrimSpace(desc.MCPServerKey); key != "" {
		return key
	}
	return in.Name
}

// confirmApplyConfig is the single confirmation gate for writing the
// user-owned config.json.
func confirmApplyConfig(out io.Writer) (bool, error) {
	confirm := false
	form := huh.NewForm(huh.NewGroup(
		huh.NewConfirm().
			Title("Apply this change to config.json?").
			Description("your previous config.json is kept as config.json.bak").
			Affirmative("Yes, write it").
			Negative("No, discard").
			Value(&confirm),
	))
	if err := form.Run(); err != nil {
		return false, err
	}
	return confirm, nil
}

func envMapFromList(env []string) map[string]string {
	out := map[string]string{}
	for _, pair := range env {
		if i := strings.Index(pair, "="); i > 0 {
			out[pair[:i]] = pair[i+1:]
		}
	}
	// The host environment is what actually decides browser/SSH policy.
	for _, key := range []string{"SSH_CONNECTION", "SSH_CLIENT", "SSH_TTY", "DISPLAY", "WAYLAND_DISPLAY"} {
		if v := os.Getenv(key); v != "" {
			out[key] = v
		}
	}
	return out
}
