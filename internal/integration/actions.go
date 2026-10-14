package integration

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
)

// Schema identifiers for the declarative setup surfaces. These are additive
// inside descriptor schema v1: unknown descriptor fields are ignored by older
// Piccolo builds, so the descriptor's own schema value is never bumped.
const (
	StateSchemaV1 = "piccolo.integration.state.v1"
	EventSchemaV1 = "piccolo.integration.event.v1"
)

// Roles Piccolo dispatches on. Any other role is ignored (never an error) so a
// future role cannot break an older Piccolo build.
const (
	RoleSetup      = "setup"
	RoleDiagnostic = "diagnostic"
)

// InputTypes is the closed vocabulary Piccolo can render with huh widgets.
var InputTypes = []string{"text", "secret", "path", "select", "multiselect", "confirm"}

// Action is one integration-declared menu entry. The id is opaque to Piccolo:
// it is only ever echoed back as `call <id>`.
type Action struct {
	ID       string        `json:"id"`
	Role     string        `json:"role"`
	Title    string        `json:"title"`
	Requires []string      `json:"requires,omitempty"`
	Inputs   []ActionInput `json:"inputs,omitempty"`
}

// ActionInput declares a value the integration needs from the user.
type ActionInput struct {
	Key   string `json:"key"`
	Type  string `json:"type"`
	Title string `json:"title"`
	// Destination is relative to the integration state directory and only
	// meaningful for type "path": Piccolo stages the host file there and hands
	// the integration the container-side path.
	Destination string   `json:"destination,omitempty"`
	Sensitive   bool     `json:"sensitive,omitempty"`
	Options     []string `json:"options,omitempty"`
}

// Supported reports whether every input type is in the closed vocabulary.
// Rendering refuses (rather than parses-out) an unsupported type, naming it.
func (a Action) Supported() error {
	if strings.TrimSpace(a.ID) == "" {
		return fmt.Errorf("action %q has an empty id", a.Title)
	}
	for _, in := range a.Inputs {
		if !knownInputType(in.Type) {
			return fmt.Errorf("action %q input %q has unsupported type %q (supported: %s)", a.ID, in.Key, in.Type, strings.Join(InputTypes, ", "))
		}
		if in.Type == "path" && strings.TrimSpace(in.Destination) == "" {
			return fmt.Errorf("action %q input %q is type path but declares no destination", a.ID, in.Key)
		}
	}
	return nil
}

func knownInputType(t string) bool {
	for _, known := range InputTypes {
		if t == known {
			return true
		}
	}
	return false
}

// ActionsByRole returns the declared actions for a role in descriptor order.
// Unknown roles are skipped silently.
func (d Descriptor) ActionsByRole(role string) []Action {
	var out []Action
	for _, a := range d.Actions {
		if a.Role == role {
			out = append(out, a)
		}
	}
	return out
}

// ValidateActions checks ids, roles, input types, and that every `requires`
// key is one the descriptor declares in StateKeys.
func (d Descriptor) ValidateActions() error {
	declared := map[string]bool{}
	for _, k := range d.StateKeys {
		declared[k] = true
	}
	seen := map[string]bool{}
	for _, a := range d.Actions {
		if strings.TrimSpace(a.ID) == "" {
			return fmt.Errorf("integration action requires an id")
		}
		if seen[a.ID] {
			return fmt.Errorf("integration action id %q declared twice", a.ID)
		}
		seen[a.ID] = true
		if strings.TrimSpace(a.Title) == "" {
			return fmt.Errorf("integration action %q requires a title", a.ID)
		}
		switch a.Role {
		case RoleSetup, RoleDiagnostic:
		default:
			continue // unknown role: ignored, not an error
		}
		if err := a.Supported(); err != nil {
			return err
		}
		for _, req := range a.Requires {
			if !declared[req] {
				return fmt.Errorf("integration action %q requires undeclared state key %q", a.ID, req)
			}
		}
		for _, in := range a.Inputs {
			if strings.TrimSpace(in.Key) == "" {
				return fmt.Errorf("integration action %q declares an input without a key", a.ID)
			}
		}
	}
	return nil
}

// State is what `call`-less fact lookup returns: it gates the menu so users
// cannot select an action whose prerequisites are unmet.
type State struct {
	Schema string          `json:"schema"`
	Name   string          `json:"name"`
	Facts  map[string]bool `json:"facts"`
}

func ParseState(data []byte) (State, error) {
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return State{}, fmt.Errorf("parse integration state: %w", err)
	}
	if s.Schema != StateSchemaV1 {
		return State{}, fmt.Errorf("unsupported integration state schema %q (want %q)", s.Schema, StateSchemaV1)
	}
	if strings.TrimSpace(s.Name) == "" {
		return State{}, fmt.Errorf("integration state name is required")
	}
	if s.Facts == nil {
		return State{}, fmt.Errorf("integration state facts are required")
	}
	return s, nil
}

// Missing returns the required keys that are false or absent, in stable order.
func (s State) Missing(requires []string) []string {
	var out []string
	for _, key := range requires {
		if !s.Facts[key] {
			out = append(out, key)
		}
	}
	sort.Strings(out)
	return out
}

// Event is one NDJSON line from an action's stdout.
type Event struct {
	Schema  string `json:"schema"`
	Type    string `json:"type"`
	ID      string `json:"id,omitempty"`
	Status  string `json:"status,omitempty"`
	Message string `json:"message,omitempty"`
	URL     string `json:"url,omitempty"`
	Label   string `json:"label,omitempty"`
	Code    string `json:"code,omitempty"`
	// Remediation is optional guidance for an error event; URL/Label link it.
	Remediation    string `json:"remediation,omitempty"`
	RemediationURL string `json:"remediationUrl,omitempty"`
	// Result carries the integration's proposed runtime configuration patch.
	// Piccolo — not the integration — validates and writes config.json.
	Result *ActionResult `json:"result,omitempty"`
}

// ActionResult is the terminal success payload of a `call`.
type ActionResult struct {
	Status string `json:"status,omitempty"`
	// ConfigPatch is proposed, never applied by the integration itself.
	ConfigPatch json.RawMessage `json:"configPatch,omitempty"`
}

const (
	EventStep         = "step"
	EventOpenURL      = "open_url"
	EventWaitRedirect = "wait_redirect"
	EventResult       = "result"
	EventError        = "error"
)

var eventTypes = []string{EventStep, EventOpenURL, EventWaitRedirect, EventResult, EventError}

// ValidEventType reports whether a type is in the closed event vocabulary.
func ValidEventType(t string) bool {
	for _, e := range eventTypes {
		if t == e {
			return true
		}
	}
	return false
}

// ParseEvent decodes a single NDJSON line.
func ParseEvent(line []byte) (Event, error) {
	var e Event
	if err := json.Unmarshal(line, &e); err != nil {
		return Event{}, fmt.Errorf("parse integration event: %w", err)
	}
	if e.Schema != EventSchemaV1 {
		return Event{}, fmt.Errorf("unsupported integration event schema %q (want %q)", e.Schema, EventSchemaV1)
	}
	if !ValidEventType(e.Type) {
		return Event{}, fmt.Errorf("unsupported integration event type %q", e.Type)
	}
	switch e.Type {
	case EventOpenURL, EventWaitRedirect:
		if strings.TrimSpace(e.URL) == "" {
			return Event{}, fmt.Errorf("integration event %q requires a url", e.Type)
		}
	}
	return e, nil
}

// EventReader consumes an action's stdout as NDJSON events. Lines that are not
// valid events are surfaced as log text without aborting the action, because a
// chatty dependency must not be able to wedge setup.
type EventReader struct {
	sc     *bufio.Scanner
	Logs   []string
	events []Event
}

func NewEventReader(r io.Reader) *EventReader {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	return &EventReader{sc: sc}
}

// Next returns the next event, or nil at end of stream. Unparsable lines are
// accumulated in Logs and skipped, so the caller keeps reading.
func (er *EventReader) Next() *Event {
	for er.sc.Scan() {
		line := strings.TrimSpace(er.sc.Text())
		if line == "" {
			continue
		}
		e, err := ParseEvent([]byte(line))
		if err != nil {
			er.Logs = append(er.Logs, line)
			continue
		}
		er.events = append(er.events, e)
		return &e
	}
	return nil
}

// Events returns every event consumed so far.
func (er *EventReader) Events() []Event { return append([]Event(nil), er.events...) }

// Result returns the last result event, if any.
func (er *EventReader) Result() *Event {
	for i := len(er.events) - 1; i >= 0; i-- {
		if er.events[i].Type == EventResult {
			e := er.events[i]
			return &e
		}
	}
	return nil
}

// ErrEvent returns the first error event, if any.
func (er *EventReader) ErrEvent() *Event {
	for _, e := range er.events {
		if e.Type == EventError {
			c := e
			return &c
		}
	}
	return nil
}
