package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
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

// doneLabel is the loop-exit sentinel offered in the menu; it is not an action.
const doneLabel = "Done"

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

// actionWaitBudget bounds one interactive action end to end: a person has to
// open a URL, sign in, choose an account and approve consent. Machine-scale
// timeouts are the wrong unit here, but an unbounded wait is worse, because it
// pins a container and a terminal. actionWarnBefore is how long before expiry the
// user is told the clock is running out.
const (
	actionWaitBudget = 15 * time.Minute
	actionWarnBefore = 3 * time.Minute
)

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
// runIntegrationAction starts an action container, streams its stdout events
// through onEvent, and returns once the container is gone. Every read is
// interruptible: a hung action must never be able to pin this call, because the
// whole point of the per-action budget is that Piccolo, not the integration,
// decides when an attempt is abandoned.
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
	events := make(chan picintegration.Event, 16)
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		for {
			ev := er.Next()
			if ev == nil {
				return
			}
			select {
			case events <- *ev:
			case <-ctx.Done():
				return
			}
		}
	}()

	lastExit := -1
	for {
		select {
		case ev := <-events:
			if onEvent != nil {
				onEvent(ev)
			}
		case <-readDone:
			code, waitErr := proc.Wait(ctx)
			if waitErr != nil {
				return er, waitErr
			}
			lastExit = code
			if e := er.ErrEvent(); e != nil {
				return er, fmt.Errorf("integration action failed: %s (%s)", e.Message, e.Code)
			}
			if code != 0 {
				return er, fmt.Errorf("integration action exited %d without reporting a result", code)
			}
			return er, nil
		case <-ctx.Done():
			// Force the reader goroutine to stop by tearing the container down.
			_ = proc.Shutdown(0)
			<-readDone
			if lastExit == 0 {
				return er, nil
			}
			if errors.Is(ctx.Err(), context.Canceled) {
				// Cancelled by signal (or by the courier giving up): cleanup ran,
				// so say so instead of implying a timeout.
				return er, errors.New("action interrupted; the setup container was stopped and removed")
			}
			return er, fmt.Errorf("action did not finish within its time budget (%s); the setup container was stopped and removed", actionWaitBudget)
		}
	}
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
func renderActionInputs(action picintegration.Action, inputs []picintegration.ActionInput, hostStateDir, containerState string, tty bool) (map[string]string, error) {
	if len(inputs) == 0 {
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
	holders := make([]*holder, 0, len(inputs))
	var groups []*huh.Group

	for _, in := range inputs {
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

// actionArgv builds the argv for one action invocation. It exists so the
// ordering rule below is directly testable: `piccolo-gws call <id> --config-dir
// <dir>`. Deriving a base first and appending "call" after it put the flag
// before the subcommand, which clap rejects outright.
func actionArgv(setupCommand []string, actionID string) ([]string, error) {
	return deriveArgv(setupCommand, "call", actionID)
}

// runDeclarativeSetup drives an action-based integration: describe → state →
// menu → inputs → call → events → proposed config patch.
func runDeclarativeSetup(ctx context.Context, out io.Writer, runner integrationRunner, layout workspace.Layout, workspaceRoot string, m *manifest.Manifest, found *manifest.Integration, env []string, desc picintegration.Descriptor) error {
	stdinTTY := term.IsTerminal(int(os.Stdin.Fd()))
	if !stdinTTY {
		return fmt.Errorf("integration setup needs an interactive terminal — rerun from a shell with a TTY")
	}
	rel := found.StatePathRelative()
	s := &setupSession{
		runner:          runner,
		layout:          layout,
		out:             out,
		integrationName: found.Name,
		setupCommand:    found.SetupCommand,
		hostStateDir:    layout.Integration(rel),
		contState:       "/root/.piccolo/integrations/" + rel,
		tty:             stdinTTY,
		env:             env,
		specBase: docker.Spec{
			Image:   m.Container.Image,
			Network: m.Container.Network,
			Source:  layout.PicoclawHome(),
			Volumes: integrationSetupBinds(layout),
			Env:     env,
			Labels:  map[string]string{docker.LabelWorkspace: workspaceRoot},
		},
	}
	// Derive the full argv here, not by appending to a pre-derived base: the
	// state-directory flag must trail the subcommand, because clap rejects flags
	// that appear before the subcommand they belong to.
	stateArgv, err := deriveArgv(found.SetupCommand, "state")
	if err != nil {
		return err
	}
	s.stateArgv = stateArgv

	// The menu re-renders after every action: state facts change, so what is
	// available and what is left to do change with them. That is what lets each
	// action do exactly one thing instead of shipping composite entries.
	var pendingPatch json.RawMessage
	for {
		state, err := s.state(ctx)
		if err != nil {
			return err
		}
		menu := buildMenu(desc.Actions, state)
		if len(menu) == 0 {
			return fmt.Errorf("integration declares no usable setup actions")
		}
		chosen, done, err := s.chooseAction(displayIntegration(*found), menu)
		if err != nil {
			return err
		}
		if done {
			break
		}
		patch, err := s.runAction(ctx, chosen, state)
		if err != nil {
			return err
		}
		if len(patch) > 0 {
			pendingPatch = patch
		}
	}
	if len(pendingPatch) == 0 {
		return nil
	}
	return applyIntegrationPatch(s.out, s.layout.Config(), pendingPatch, integrationServerKey(*found, desc), confirmApplyConfig)
}

// setupSession carries the wiring an interactive flow needs, so the loop body
// stays readable and tests can drive it.
type setupSession struct {
	runner          integrationRunner
	layout          workspace.Layout
	out             io.Writer
	integrationName string
	stateArgv       []string
	setupCommand    []string
	specBase        docker.Spec
	hostStateDir    string
	contState       string
	tty             bool
	env             []string
}

func (s *setupSession) state(ctx context.Context) (picintegration.State, error) {
	entry, cmd := splitEntrypointCommand(s.stateArgv)
	spec := s.specBase
	spec.Name = statusContainerName(s.name())
	spec.Entrypoint, spec.Command = entry, cmd
	data, err := runContainerCapture(ctx, s.runner, spec)
	if err != nil {
		return picintegration.State{}, fmt.Errorf("integration state lookup failed; refusing to render a menu from assumed facts: %w", err)
	}
	st, err := picintegration.ParseState(data)
	if err != nil {
		return picintegration.State{}, err
	}
	return st, nil
}

func (s *setupSession) name() string { return s.integrationName }

func (s *setupSession) chooseAction(title string, menu []menuEntry) (*menuEntry, bool, error) {
	// Available actions come first so the user sees what is left to do; gated
	// ones sink to the bottom with their reason, still visible and explanatory.
	order := make([]int, 0, len(menu))
	for i := range menu {
		if menu[i].Enabled {
			order = append(order, i)
		}
	}
	for i := range menu {
		if !menu[i].Enabled {
			order = append(order, i)
		}
	}
	labels := make([]string, 0, len(order)+1)
	indexes := make([]int, 0, len(order)+1)
	for _, i := range order {
		labels = append(labels, menu[i].Title())
		indexes = append(indexes, i)
	}
	labels = append(labels, doneLabel)
	indexes = append(indexes, -1)
	var pick string
	form := huh.NewForm(huh.NewGroup(
		huh.NewSelect[string]().
			Title(title + " — what do you want to do?").
			Description("Credentials identify the app; login authorizes your Google account.").
			Options(huh.NewOptions(labels...)...).
			Value(&pick),
	))
	if err := form.Run(); err != nil {
		return nil, false, err
	}
	for n, label := range labels {
		if label != pick {
			continue
		}
		if indexes[n] < 0 {
			return nil, true, nil
		}
		entry := &menu[indexes[n]]
		if !entry.Enabled {
			return nil, false, fmt.Errorf("action %q is unavailable: %s", entry.Action.ID, entry.Reason)
		}
		return entry, false, nil
	}
	return nil, false, fmt.Errorf("selection %q no longer matches the integration's actions", pick)
}

// inputsFor drops inputs whose whenUnmet fact is already satisfied, so a
// credentials form collapses to a scope prompt once credentials exist.
func inputsFor(action picintegration.Action, state picintegration.State) []picintegration.ActionInput {
	var out []picintegration.ActionInput
	for _, in := range action.Inputs {
		if in.WhenUnmet != "" && state.Facts[in.WhenUnmet] {
			continue
		}
		out = append(out, in)
	}
	return out
}

// runAction executes one chosen action. `state` is the same snapshot the menu
// was built from, so the facts that gated availability are also the facts that
// filter the inputs: one read per iteration, no window where the two disagree,
// and no extra container start per action.
func (s *setupSession) runAction(ctx context.Context, entry *menuEntry, state picintegration.State) (json.RawMessage, error) {
	action := entry.Action
	inputs := inputsFor(action, state)
	values, err := renderActionInputs(action, inputs, s.hostStateDir, s.contState, s.tty)
	if err != nil {
		return nil, err
	}
	answers, err := answersJSON(inputs, values)
	if err != nil {
		return nil, err
	}
	// One deadline AND an interrupt signal per action, shared by the container
	// wait and the courier. Without the signal part, Ctrl-C killed Piccolo while
	// the action container kept running — holding its courier port and the state
	// mount, and only cleaned up by the next run's reclaim sweep.
	sigCtx, stopSignals := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	actCtx, cancelAct := context.WithTimeout(sigCtx, actionWaitBudget)
	defer cancelAct()
	warnOnce := sync.Once{}
	warnDeadline := time.AfterFunc(actionWaitBudget-actionWarnBefore, func() {
		warnOnce.Do(func() {
			fmt.Fprintf(s.out, "\nWaiting for the browser authorization to finish; this attempt ends in %s.\n", actionWarnBefore)
		})
	})
	defer warnDeadline.Stop()
	callArgv, err := actionArgv(s.setupCommand, action.ID)
	if err != nil {
		return nil, err
	}
	bin, cmd := splitEntrypointCommand(callArgv)
	spec := s.specBase
	spec.Name = actionContainerName(s.integrationName, action.ID)
	spec.Ports = nil // callbacks ride the courier; nothing is published
	spec.Entrypoint, spec.Command = bin, cmd
	if removed, err := s.runner.RemoveByPrefix(ctx, docker.SetupContainerPrefix+s.integrationName+"-"); err == nil && len(removed) > 0 {
		fmt.Fprintf(s.out, "Removed %d abandoned integration container(s): %s\n", len(removed), strings.Join(removed, ", "))
	}
	fmt.Fprintf(s.out, "Running %s...\n", action.ID)

	host := detectHostSession(envMapFromList(s.env), s.tty)
	var courierErr error
	onEvent := func(ev picintegration.Event) {
		switch ev.Type {
		case picintegration.EventStep:
			fmt.Fprintf(s.out, "  [%s] %s: %s\n", ev.Status, ev.ID, ev.Message)
		case picintegration.EventOpenURL:
			fmt.Fprintf(s.out, "\n%s\n", host.browserHint(ev.URL))
			if host.shouldLaunchBrowser() {
				if err := openInBrowser(ctx, ev.URL); err != nil {
					fmt.Fprintf(s.out, "  (could not launch a browser: %v)\n", err)
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
			relay := func(rctx context.Context, q string) error {
				return relayCallbackIntoContainer(rctx, s.runner, spec.Name, port, q)
			}
			// The courier shares the action deadline; on expiry or error it cancels
			// the action so the container is torn down and reported, not hung.
			if err := c.await(actCtx, time.Until(time.Now().Add(actionWaitBudget)), relay); err != nil {
				courierErr = err
				// Stop waiting on the container too: it can only be sitting on a
				// browser interaction that is no longer going to arrive.
				cancelAct()
			}
		case picintegration.EventError:
			fmt.Fprintf(s.out, "  x %s\n", ev.Message)
			if ev.Remediation != "" {
				fmt.Fprintf(s.out, "    -> %s\n", ev.Remediation)
			}
			if ev.RemediationURL != "" {
				fmt.Fprintf(s.out, "       %s: %s\n", ev.Label, ev.RemediationURL)
			}
		}
	}

	reader, err := runIntegrationAction(actCtx, s.runner, integrationCallRequest{
		Answers:   answers,
		Container: spec.Name,
		Spec:      spec,
	}, s.out, onEvent)
	if courierErr != nil {
		return nil, fmt.Errorf("browser authorization did not complete: %w", courierErr)
	}
	if err != nil {
		return nil, err
	}
	res := reader.Result()
	if res == nil || res.Result == nil {
		return nil, nil
	}
	return res.Result.ConfigPatch, nil
}

// actionContainerName keeps one deterministic name per integration+action so an
// abandoned run is always reclaimable.
func actionContainerName(integration, action string) string {
	return docker.SetupContainerPrefix + sanitizeForName(integration) + "-" + sanitizeForName(action)
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
func confirmApplyConfig() (bool, error) {
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
