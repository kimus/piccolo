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
	"github.com/kimus/piccolo/internal/wizard"
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
//
// Which keys are present is meaningful and is decided by renderActionInputs:
// a present empty value says clear, an absent key says keep. This function only
// restricts the document to declared keys, so an internal value can never leak
// into an action through a stray map entry.
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
func runIntegrationAction(ctx context.Context, runner integrationRunner, req integrationCallRequest, out io.Writer, onEvent func(picintegration.Event)) (*picintegration.EventReader, actionOutcome, error) {
	proc, err := runner.Run(ctx, req.Spec)
	if err != nil {
		return nil, outcomeCrashed, err
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
				return er, outcomeCrashed, waitErr
			}
			lastExit = code
			// A reported failure and a crash are different events, and the exit
			// code decides between them only when the stream stayed silent.
			return er, classifyAction(er.ErrEvent(), code, nil), nil
		case <-ctx.Done():
			// Force the reader goroutine to stop by tearing the container down.
			_ = proc.Shutdown(0)
			<-readDone
			if lastExit == 0 {
				return er, outcomeSucceeded, nil
			}
			// Piccolo ended this attempt, not the integration. Blaming the
			// integration for a cancel it never agreed to is how a user learns to
			// disbelieve every message.
			return er, classifyAction(nil, lastExit, ctx.Err()), nil
		}
	}
}

// actionOutcome is what an action's ending means to the menu loop.
type actionOutcome int

const (
	outcomeSucceeded actionOutcome = iota
	// outcomeReported: the integration said what happened and meant it. The
	// action failed; Piccolo did not.
	outcomeReported
	// outcomeCrashed: a non-zero exit with no terminal event. Piccolo cannot know
	// whether side effects landed, so this is the one ending that is not data.
	outcomeCrashed
	// outcomeCancelled: the user or the courier gave up. Nobody's fault.
	outcomeCancelled
	// outcomeTimedOut: Piccolo's own budget expired. Also nobody's fault, but it
	// is worth saying the deadline out loud.
	outcomeTimedOut
)

func (o actionOutcome) String() string {
	switch o {
	case outcomeSucceeded:
		return "succeeded"
	case outcomeReported:
		return "reported failure"
	case outcomeCrashed:
		return "crashed"
	case outcomeCancelled:
		return "cancelled"
	case outcomeTimedOut:
		return "timed out"
	}
	return "unknown"
}

// terminal reports whether the outcome belongs in a closing summary as something
// the user should still know about. A cancel is excluded: the user chose it.
func (o actionOutcome) terminal() bool {
	return o == outcomeReported || o == outcomeCrashed || o == outcomeTimedOut
}

// classifyAction decides what an ending means.
//
// The order is the whole point: a terminal event outranks the exit code, because
// gws (and any integration following the protocol) exits non-zero deliberately
// after reporting. So `error` event plus exit 11 is a report, exit 101 with no
// event is a death, and a context we cancelled is neither.
func classifyAction(errEvent *picintegration.Event, exitCode int, ctxErr error) actionOutcome {
	switch {
	case errors.Is(ctxErr, context.DeadlineExceeded):
		return outcomeTimedOut
	case ctxErr != nil:
		return outcomeCancelled
	case errEvent != nil:
		return outcomeReported
	case exitCode != 0:
		return outcomeCrashed
	default:
		return outcomeSucceeded
	}
}

// actionReport is one action's result as the loop needs to see it.
type actionReport struct {
	action  string
	outcome actionOutcome
	patch   json.RawMessage
	summary string
}

// attemptLog is what the session knows about each action, keyed by action id.
//
// Recording every attempt as its own line made a retry lie: check_connectivity
// failing on a disabled API and then passing after the user enabled it printed
// "check_connectivity: reported failure" at the end of a run that ended fine.
// The log keeps the latest outcome per action, in first-attempt order, so
// finishing something clears it from the summary the way it clears the menu.
type attemptLog []actionReport

func (l *attemptLog) record(rep actionReport) {
	for i, existing := range *l {
		if existing.action == rep.action {
			(*l)[i] = rep
			return
		}
	}
	*l = append(*l, rep)
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

// waitRedirectHelp is provider-neutral on purpose: this is Piccolo's generic
// wait screen, and naming one provider's console in it teaches users nothing who
// are setting up a different integration.
var waitRedirectHelp = []string{
	"this port changes every attempt — do not register it with your provider;",
	"a native/desktop client needs no registered redirect URI at all.",
	"press ctrl-c to give up (esc does nothing here: no form is on screen).",
}

// courierFailureNote turns the two ways a browser wait ends badly into something a
// user can act on. They are different problems: one is a decision, one is a clock.
func courierFailureNote(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "cancelled — the browser flow was abandoned and nothing was changed"
	case errors.Is(err, context.DeadlineExceeded):
		return fmt.Sprintf("timed out after %s — the browser flow never completed; nothing was changed", actionWaitBudget)
	default:
		return fmt.Sprintf("browser authorization did not complete: %v", err)
	}
}

// errActionAborted signals that the user backed out of a rendered form. It is not
// a failure: nothing is saved for that action, no container starts, and the menu
// redraws — while a configuration proposal earned by an already-completed action
// stays pending, because it belongs to that action and not to the form abandoned.
var errActionAborted = errors.New("integration form abandoned")

// requiredValidator blocks advancing past, and submitting with, an empty required
// field. A sensitive field that already holds a value is the one exception: blank
// there is the gesture for "keep what is stored", not an absence of input.
func requiredValidator(in picintegration.ActionInput, blankMeansKeep bool) func(string) error {
	return func(v string) error {
		if strings.TrimSpace(v) != "" {
			return nil
		}
		if blankMeansKeep {
			return nil
		}
		return fmt.Errorf("%s is required — enter a value, or press esc to go back without saving", in.Title)
	}
}

// renderActionInputs builds a huh form from the action's declared inputs and
// returns the answers to deliver, keyed by input. Every declared input renders:
// state facts gate which actions are selectable, never which fields appear,
// because a field deleted before rendering can be neither validated nor replaced.
//
// Three states stay distinguishable on the wire, which is the whole point of
// pre-filling:
//
//   - non-empty value      -> set
//   - empty value          -> clear, which is what deleting a pre-filled optional
//     field means, and the reason "absent means keep" alone cannot work
//   - key absent           -> keep; only an untouched sensitive field produces this,
//     since no stored secret may travel outbound
//
// For a `path` input the returned value is the *container-side* path, staged
// under the integration state directory.
func renderActionInputs(action picintegration.Action, inputs []picintegration.ActionInput, state picintegration.State, hostStateDir, containerState string, tty bool) (map[string]string, error) {
	if len(inputs) == 0 {
		return map[string]string{}, nil
	}
	if !tty {
		return nil, fmt.Errorf("integration setup needs an interactive terminal — rerun from a shell with a TTY")
	}

	// One holder per input: values are only meaningful after the form runs.
	type holder struct {
		key        string
		kind       string
		text       string
		check      bool
		multi      []string
		input      picintegration.ActionInput
		sensitive  bool
		blankKeeps bool // blank means keep a stored value, not clear it
	}
	holders := make([]*holder, 0, len(inputs))
	var groups []*huh.Group

	for _, in := range inputs {
		// The library never renders its own abort binding, so the hint is written
		// into the screen -- the way `piccolo setup` does it. Once per form, on the
		// first field, because repeating it on every field buries the parts that
		// explain what pressing enter will do.
		first := len(groups) == 0
		current := state.Field(in.Key)
		h := &holder{key: in.Key, kind: in.Type, input: in, sensitive: in.SensitiveInput()}
		// Show what is stored so skipping and clearing remain distinguishable —
		// except a secret, which is reported as a state instead of a value.
		if !h.sensitive {
			h.text = current.Value
		} else {
			h.blankKeeps = current.Set
		}
		holders = append(holders, h)
		switch in.Type {
		case "text", "secret":
			field := huh.NewInput().Title(in.Title)
			desc := ""
			if h.sensitive {
				field = field.EchoMode(huh.EchoModePassword)
				if current.Set {
					desc = "Already set. Press enter to keep it, or type a value to replace it."
				}
			} else if current.Value != "" {
				// A pre-filled visible field is otherwise silent about the fact
				// that pressing enter means KEEP. That silence is how a user ends
				// up authorizing a stale client while believing they replaced it.
				desc = fmt.Sprintf("Currently: %s — press enter to keep it, or edit to replace it.", elide(current.Value))
			}
			if first {
				desc = withEscHint(desc, "esc back to the menu")
			}
			if desc != "" {
				field = field.Description(desc)
			}
			if in.Required {
				field = field.Validate(requiredValidator(in, h.blankKeeps))
			}
			groups = append(groups, huh.NewGroup(field.Value(&h.text)))
		case "confirm":
			confirm := huh.NewConfirm().Title(in.Title)
			if first {
				confirm = confirm.Description(escBackHint)
			}
			groups = append(groups, huh.NewGroup(confirm.Value(&h.check)))
		case "select":
			sel := huh.NewSelect[string]().Title(in.Title).Options(stringOptions(in.Options, &h.text)...)
			if first {
				sel = sel.Description(escBackHint)
			}
			groups = append(groups, huh.NewGroup(sel))
		case "multiselect":
			multi := huh.NewMultiSelect[string]().Title(in.Title).Options(multiOptions(in.Options, &h.multi)...)
			if first {
				multi = multi.Description(escBackHint)
			}
			groups = append(groups, huh.NewGroup(multi))
		case "path":
			// The picker starts inside the integration's own state directory: the
			// user is choosing a host file to stage there, not browsing the whole
			// filesystem by default. A pre-filled path shows the stored container-side
			// path; emptying it asks the integration to stop using it.
			picker := huh.NewFilePicker().Title(in.Title).Height(12).Picking(true).ShowHidden(true).ShowPermissions(true).FileAllowed(true).DirAllowed(false).CurrentDirectory(hostStateDir)
			if _, err := os.Stat(hostStateDir); err != nil {
				picker = picker.CurrentDirectory(homeOr(hostStateDir))
			}
			if in.Required {
				picker = picker.Validate(requiredValidator(in, false))
			}
			if first {
				picker = picker.Description(escBackHint)
			}
			groups = append(groups, huh.NewGroup(picker.Value(&h.text)))
		default:
			return nil, fmt.Errorf("unsupported input type %q for %q", in.Type, in.Key)
		}
	}

	if err := wizard.Form(groups...).Run(); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return nil, errActionAborted
		}
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
				// Blank is a decision, not an absence: send it, so the integration
				// can unset a path it was previously given.
				values[h.key] = ""
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
			values[h.key] = h.text
		}
	}
	return encodeAnswers(inputs, state, values), nil
}

// encodeAnswers turns raw form output into the answers document, applying the
// three-state rule that pre-fill makes possible:
//
//   - a non-empty value sets
//   - an empty value on a field that can be seen clears, which is the only way
//     deleting a stored optional value can mean anything
//   - an omitted key keeps, reserved for an untouched sensitive field, since no
//     stored secret may travel outbound to be shown
//
// Sensitive fields are the sole source of omission, so a blank can never be
// misread as "keep" on a field the user was looking at.
func encodeAnswers(inputs []picintegration.ActionInput, state picintegration.State, raw map[string]string) map[string]string {
	out := map[string]string{}
	for _, in := range inputs {
		value, ok := raw[in.Key]
		if !ok {
			continue
		}
		if in.SensitiveInput() && value == "" {
			continue // untouched: the integration keeps what it stores
		}
		out[in.Key] = value
	}
	return out
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
	form := wizard.Form(huh.NewGroup(
		huh.NewConfirm().
			Title(fmt.Sprintf("Replace the existing %s?", filepath.Base(target))).
			Description("the current credentials for this integration will be overwritten").
			Affirmative("Yes, replace").
			Negative("No, keep the current file").
			Value(&agree),
	))
	if err := form.Run(); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			// Backing out of a yes/no gate is a no. Reporting it as a failure would
			// make the escape key sound like a crash on the screen whose whole job is
			// to let the user say no.
			return false, nil
		}
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
		style:           newStyler(out),
		integrationName: found.Name,
		description:     strings.TrimSpace(desc.Description),
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
	var attempts attemptLog
	quitting := false
	crashes := map[string]int{}
	for {
		state, err := s.state(ctx)
		if err != nil {
			return err
		}
		menu := buildMenu(desc.Actions, state)
		if len(menu) == 0 {
			return fmt.Errorf("integration declares no usable setup actions")
		}
		chosen, done, err := s.chooseAction(displayIntegration(*found), menu, state)
		if err != nil {
			if errors.Is(err, huh.ErrUserAborted) {
				// Quitting from the menu abandons the run: a proposal from a completed
				// action is not applied behind the user's back. The summary still
				// prints, because failures the user walked away from are exactly the
				// thing not to lose silently.
				quitting = true
				break
			}
			return err
		}
		if done {
			break
		}
		rep, err := s.runAction(ctx, chosen, state)
		if err != nil {
			if errors.Is(err, errActionAborted) {
				// Backing out of a form saves nothing for that action and keeps any
				// proposal already earned, then re-reads state and redraws.
				continue
			}
			return err
		}
		if len(rep.patch) > 0 {
			pendingPatch = rep.patch
		}
		attempts.record(*rep)
		if rep.outcome.terminal() {
			if rep.outcome == outcomeCrashed {
				crashes[rep.action]++
				if crashes[rep.action] == 2 {
					fmt.Fprintln(s.out, s.style.Crashf("  %s has died twice without reporting; stop retrying it and check the state below", rep.action))
				}
			}
		}
		// A reported failure is data, not an exception: the loop continues and the
		// next iteration re-reads state, so a partial save becomes visible instead
		// of the session ending on top of it.
	}
	s.summarize(attempts)
	if quitting {
		// Only true when something actually was pending: a session that proposed
		// nothing should not report abandoning something.
		if len(pendingPatch) > 0 {
			fmt.Fprintln(s.out, s.style.Mutedf("  left without applying a pending config change; rerun setup to review it"))
		}
		return nil
	}
	if len(pendingPatch) == 0 {
		return nil
	}
	return applyIntegrationPatch(s.out, s.layout.Config(), pendingPatch, integrationServerKey(*found, desc), confirmApplyConfig)
}

// summarize prints what the session ended with. Severity lives here, in colour
// and words, so the command itself can exit 0: a diagnostic that correctly
// reports a disabled API is not a failed command, and making it one is how every
// message later gets ignored.
// summarize lists only unfinished actions. The filter lives here, not in the
// caller: a summary that prints whatever it is handed would list a success or a
// cancel the user chose as a failure the moment a second call site appears, and
// a summary with false entries is the thing that teaches people to skip it.
// escBackHint is shown once per form. huh never renders its own abort binding,
// so a key that works must also be advertised -- see the setup-ux spec.
const escBackHint = "esc back to the menu"

// withEscHint appends the abort hint to a field description that may already
// carry an explanation of its own.
func withEscHint(desc, hint string) string {
	if strings.TrimSpace(desc) == "" {
		return hint
	}
	return desc + " · " + hint
}

func (s *setupSession) summarize(reports []actionReport) {
	var unfinished []actionReport
	for _, r := range reports {
		if r.outcome.terminal() {
			unfinished = append(unfinished, r)
		}
	}
	if len(unfinished) == 0 {
		return
	}
	fmt.Fprintln(s.out, s.style.heading("\nSome actions did not complete"))
	for _, r := range unfinished {
		line := fmt.Sprintf("  %s: %s", r.action, r.outcome)
		if r.outcome == outcomeCrashed || r.outcome == outcomeTimedOut {
			fmt.Fprintln(s.out, s.style.Crashf("%s", line))
			continue
		}
		fmt.Fprintln(s.out, s.style.Failf("%s", line))
	}
	fmt.Fprintln(s.out, s.style.Mutedf("  run 'piccolo integration status' for what is in place now, or rerun an action from the menu"))
}

// setupSession carries the wiring an interactive flow needs, so the loop body
// stays readable and tests can drive it.
type setupSession struct {
	runner          integrationRunner
	layout          workspace.Layout
	out             io.Writer
	style           styler
	integrationName string
	// description is the integration's own line, shown under the menu title so
	// Piccolo never has to describe a provider it does not know.
	description  string
	stateArgv    []string
	setupCommand []string
	specBase     docker.Spec
	hostStateDir string
	contState    string
	tty          bool
	env          []string
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

// storedSummary names what is already on disk, so a user can see which client an
// action is about to use BEFORE the browser opens. Without it, a pre-filled field
// that is being kept silently is indistinguishable from one that was just typed —
// which is exactly how a login ended up authorizing a stale client while the user
// believed they had replaced it.
//
// Keys belonging to sensitive inputs are skipped outright. The protocol already
// refuses to send secret values, and the display layer should not be the thing
// that notices if an integration ever breaks that rule.
func storedSummary(actions []picintegration.Action, state picintegration.State) string {
	sensitive := map[string]bool{}
	for _, a := range actions {
		for _, in := range a.Inputs {
			if in.SensitiveInput() {
				sensitive[in.Key] = true
			}
		}
	}
	keys := make([]string, 0, len(state.Values))
	for k, v := range state.Values {
		if v.Set && v.Value != "" && !sensitive[k] {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return ""
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+elide(state.Values[k].Value))
	}
	return "stored: " + strings.Join(parts, "  ")
}

// elide shortens a stored value for display while keeping the part that
// distinguishes it. Truncating both ends was the first attempt (12 + "…" + 6), and
// it failed the only case that matters: two OAuth clients in the same project share
// their first twelve characters, so both rendered as "192902532576…nt.com" — a
// summary that looks informative and identifies nothing. Keep the head instead,
// which is where the distinguishing part of a client ID lives.
func elide(v string) string {
	r := []rune(strings.TrimSpace(v))
	if len(r) <= 26 {
		return string(r)
	}
	return string(r[:26]) + "…"
}

// menuActions flattens the menu rows back into their declared actions, so display
// helpers can reason about input sensitivity without re-deriving the menu.
func menuActions(menu []menuEntry) []picintegration.Action {
	out := make([]picintegration.Action, 0, len(menu))
	for _, e := range menu {
		out = append(out, e.Action)
	}
	return out
}

func (s *setupSession) chooseAction(title string, menu []menuEntry, state picintegration.State) (*menuEntry, bool, error) {
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
	subtitle := s.description
	if stored := storedSummary(menuActions(menu), state); stored != "" {
		if subtitle != "" {
			subtitle += "  ·  " + stored
		} else {
			subtitle = stored
		}
	}
	selectField := huh.NewSelect[string]().
		Title(title + " — what do you want to do?").
		Options(huh.NewOptions(labels...)...).
		Value(&pick)
	if subtitle != "" {
		subtitle += "  ·  esc quit"
	} else {
		subtitle = "esc quit"
	}
	if subtitle != "" {
		selectField = selectField.Description(subtitle)
	}
	form := wizard.Form(huh.NewGroup(selectField))
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

// inputsFor returns the inputs an action must present. It is a seam rather than
// logic: state facts gate which actions are selectable (buildMenu) and have never
// been allowed to remove a declared field. An earlier version did suppress, keyed
// on whenUnmet, which made the credential action unfillable once credentials
// existed — the form lost the two fields it was for and submitted an unrelated
// optional one. The accompanying test pins that this returns every declared input.
func inputsFor(action picintegration.Action, state picintegration.State) []picintegration.ActionInput {
	return action.Inputs
}

// runAction executes one chosen action. `state` is the same snapshot the menu was
// built from, so availability and the values shown for pre-fill come from one read
// per iteration — no window where the two disagree, and no extra container start.
func (s *setupSession) runAction(ctx context.Context, entry *menuEntry, state picintegration.State) (*actionReport, error) {
	action := entry.Action
	inputs := inputsFor(action, state)
	values, err := renderActionInputs(action, inputs, state, s.hostStateDir, s.contState, s.tty)
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
			// The colour follows ev.Status (protocol data), never the message text:
			// a reported failure is amber, and only Piccolo's own faults go red.
			fmt.Fprintf(s.out, "  %s\n", s.style.severity(ev.Status, fmt.Sprintf("[%s] %s: %s", ev.Status, ev.ID, ev.Message)))
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
			// Surface the callback target, and defuse the trap: the port is
			// re-randomised per attempt, so registering it is both pointless and
			// impossible to keep working. A user who feels the need to add it is
			// holding a Web-application client, which is the wrong type.
			fmt.Fprintf(s.out, "  callback: %s (Piccolo is listening; nothing to open here)\n", c.callbackAddr())
			for _, line := range waitRedirectHelp {
				fmt.Fprintf(s.out, "            %s\n", line)
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
			fmt.Fprintln(s.out, s.style.Failf("  x %s", ev.Message))
			if ev.Remediation != "" {
				fmt.Fprintf(s.out, "    -> %s\n", ev.Remediation)
			}
			if ev.RemediationURL != "" {
				fmt.Fprintf(s.out, "       %s: %s\n", ev.Label, ev.RemediationURL)
			}
		}
	}

	reader, outcome, err := runIntegrationAction(actCtx, s.runner, integrationCallRequest{
		Answers:   answers,
		Container: spec.Name,
		Spec:      spec,
	}, s.out, onEvent)
	if err != nil {
		// Piccolo could not run the action at all (container, daemon, transport).
		// That is the one class of failure that still ends the session, because
		// nothing sensible can be redrawn without a working daemon.
		return nil, err
	}
	if courierErr != nil && outcome == outcomeSucceeded {
		outcome = outcomeReported
	}
	rep := &actionReport{action: action.ID, outcome: outcome}
	switch outcome {
	case outcomeCancelled, outcomeTimedOut:
		// Piccolo ended this attempt; saying "failed" here would teach the user to
		// distrust the message.
		note := courierErr
		if note == nil {
			if outcome == outcomeTimedOut {
				note = context.DeadlineExceeded
			} else {
				note = context.Canceled
			}
		}
		fmt.Fprintln(s.out, s.style.Mutedf("  %s", courierFailureNote(note)))
	case outcomeCrashed:
		fmt.Fprintln(s.out, s.style.Crashf("  x %s exited without reporting a result", action.ID))
		fmt.Fprintln(s.out, "    it may have partially applied changes; the menu below reflects what is on disk now")
	case outcomeReported:
		// Already rendered from the event stream, with remediation.
	}
	if outcome == outcomeSucceeded {
		if res := reader.Result(); res != nil && res.Result != nil {
			rep.patch = res.Result.ConfigPatch
		}
	}
	return rep, nil
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
	form := wizard.Form(huh.NewGroup(
		huh.NewConfirm().
			Title("Apply this change to config.json?").
			Description("your previous config.json is kept as config.json.bak").
			Affirmative("Yes, write it").
			Negative("No, discard").
			Value(&confirm),
	))
	if err := form.Run(); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			// Backing out of a yes/no gate is a no. Reporting it as a failure would
			// make the escape key sound like a crash on the screen whose whole job is
			// to let the user say no.
			return false, nil
		}
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
