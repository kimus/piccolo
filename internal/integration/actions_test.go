package integration

import (
	"strings"
	"testing"
)

const actionsDescriptor = `{
  "schema": "piccolo.integration.v1",
  "name": "gws",
  "displayName": "Google Workspace",
  "setup": { "command": ["piccolo-gws", "setup"] },
  "stateKeys": ["credentials", "login", "apis_enabled"],
  "actions": [
    { "id": "set_up_access", "role": "setup", "title": "Set up Google Workspace access",
      "requires": ["credentials"],
      "inputs": [
        { "key": "client_secret", "type": "path", "title": "client_secret.json",
          "destination": "auth/client_secret.json", "sensitive": true }
      ] },
    { "id": "login", "role": "setup", "title": "Login / authorize Google account" },
    { "id": "connectivity", "role": "diagnostic", "title": "Check connectivity" },
    { "id": "some_future_thing", "role": "maintenance", "title": "Ignored role" }
  ],
  "someFutureField": { "ignored": true }
}`

func TestParseDescriptorActions(t *testing.T) {
	d, err := ParseDescriptor([]byte(actionsDescriptor))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !d.Declarative() {
		t.Fatal("descriptor with actions must select the declarative path")
	}
	if len(d.Actions) != 4 {
		t.Fatalf("actions = %d, want 4 (unknown role parsed, not dropped at parse time)", len(d.Actions))
	}
	if got := d.ActionsByRole(RoleSetup); len(got) != 2 {
		t.Fatalf("setup actions = %d, want 2", len(got))
	}
	// Unknown role yields no actions for that role but is not an error.
	if got := d.ActionsByRole("maintenance"); len(got) != 1 {
		t.Fatalf("maintenance actions = %d, want 1 (dispatch ignores it later)", len(got))
	}
	if got := d.ActionsByRole("nonsense"); len(got) != 0 {
		t.Fatalf("unknown role must not match, got %d", len(got))
	}
	in := d.Actions[0].Inputs[0]
	if in.Type != "path" || in.Destination != "auth/client_secret.json" || !in.Sensitive {
		t.Fatalf("path input = %#v", in)
	}
	// The legacy setup.command survived untouched for older consumers.
	if strings.Join(d.Setup.Command, " ") != "piccolo-gws setup" {
		t.Fatalf("setup command = %v", d.Setup.Command)
	}
}

func TestParseDescriptorLegacyWithoutActions(t *testing.T) {
	d, err := ParseDescriptor([]byte(`{"schema":"piccolo.integration.v1","name":"gws","displayName":"Google Workspace","setup":{"command":["piccolo-gws","setup"]}}`))
	if err != nil {
		t.Fatalf("legacy descriptor must still parse: %v", err)
	}
	if d.Declarative() {
		t.Fatal("legacy descriptor must not select the declarative path")
	}
}

func TestValidateActionsRejectsUnknownInputType(t *testing.T) {
	d, err := ParseDescriptor([]byte(`{
		"schema":"piccolo.integration.v1","name":"gws","displayName":"Google Workspace",
		"setup":{"command":["piccolo-gws","setup"]},
		"stateKeys":["credentials"],
		"actions":[{"id":"a","role":"setup","title":"A","inputs":[{"key":"k","type":"datepicker","title":"K"}]}]
	}`))
	if err == nil {
		t.Fatal("expected unsupported input type to fail validation")
	}
	if !strings.Contains(err.Error(), "datepicker") || !strings.Contains(err.Error(), "text, secret, path, select, multiselect, confirm") {
		t.Fatalf("error must name the type and the supported set: %v", err)
	}
	_ = d
}

func TestValidateActionsRejectsUndeclaredRequires(t *testing.T) {
	_, err := ParseDescriptor([]byte(`{
		"schema":"piccolo.integration.v1","name":"gws","displayName":"Google Workspace",
		"setup":{"command":["piccolo-gws","setup"]},
		"stateKeys":["credentials"],
		"actions":[{"id":"a","role":"setup","title":"A","requires":["telepathy"]}]
	}`))
	if err == nil || !strings.Contains(err.Error(), "undeclared state key") || !strings.Contains(err.Error(), "telepathy") {
		t.Fatalf("expected undeclared requires key error, got %v", err)
	}
}

func TestValidateActionsRejectsPathWithoutDestination(t *testing.T) {
	_, err := ParseDescriptor([]byte(`{
		"schema":"piccolo.integration.v1","name":"gws","displayName":"Google Workspace",
		"setup":{"command":["piccolo-gws","setup"]},
		"actions":[{"id":"a","role":"setup","title":"A","inputs":[{"key":"k","type":"path","title":"K"}]}]
	}`))
	if err == nil || !strings.Contains(err.Error(), "no destination") {
		t.Fatalf("expected missing destination error, got %v", err)
	}
}

func TestValidateActionsRejectsDuplicateIDs(t *testing.T) {
	_, err := ParseDescriptor([]byte(`{
		"schema":"piccolo.integration.v1","name":"gws","displayName":"Google Workspace",
		"setup":{"command":["piccolo-gws","setup"]},
		"actions":[{"id":"dup","role":"setup","title":"A"},{"id":"dup","role":"setup","title":"B"}]
	}`))
	if err == nil || !strings.Contains(err.Error(), "declared twice") {
		t.Fatalf("expected duplicate id error, got %v", err)
	}
}

func TestActionSupportedOnUnknownRole(t *testing.T) {
	// Unknown roles are ignored for dispatch; they must not be a descriptor error.
	d, err := ParseDescriptor([]byte(actionsDescriptor))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := d.Actions[3].Supported(); err != nil {
		t.Fatalf("unknown-role action inputs are still well-formed: %v", err)
	}
}

func TestParseStateAndMissing(t *testing.T) {
	s, err := ParseState([]byte(`{"schema":"piccolo.integration.state.v1","name":"gws","facts":{"credentials":true,"login":false,"apis_enabled":false}}`))
	if err != nil {
		t.Fatalf("parse state: %v", err)
	}
	if missing := s.Missing([]string{"credentials", "login"}); len(missing) != 1 || missing[0] != "login" {
		t.Fatalf("missing = %#v", missing)
	}
	// Absent keys count as unmet.
	if missing := s.Missing([]string{"never_declared"}); len(missing) != 1 {
		t.Fatalf("absent fact must read as unmet: %#v", missing)
	}
}

func TestParseStateRejectsBadPayload(t *testing.T) {
	cases := map[string]string{
		"wrong schema": `{"schema":"piccolo.integration.state.v9","name":"gws","facts":{}}`,
		"no name":      `{"schema":"piccolo.integration.state.v1","facts":{}}`,
		"no facts":     `{"schema":"piccolo.integration.state.v1","name":"gws"}`,
		"not json":     `nonsense`,
	}
	for name, payload := range cases {
		if _, err := ParseState([]byte(payload)); err == nil {
			t.Fatalf("%s: expected error", name)
		}
	}
}

func TestParseEventTypes(t *testing.T) {
	lines := []string{
		`{"schema":"piccolo.integration.event.v1","type":"step","id":"credentials","status":"running","message":"Paste the client secret"}`,
		`{"schema":"piccolo.integration.event.v1","type":"open_url","url":"https://console.cloud.google.com/apis/credentials?project=p","label":"Create a Desktop OAuth client in project p"}`,
		`{"schema":"piccolo.integration.event.v1","type":"wait_redirect","url":"https://accounts.google.com/o/oauth2/auth?redirect_uri=http://localhost:35935"}`,
		`{"schema":"piccolo.integration.event.v1","type":"result","status":"ok","result":{"configPatch":{"tools":{"mcp":{"servers":{"google_workspace":{"command":"piccolo-gws"}}}}}}}`,
		`{"schema":"piccolo.integration.event.v1","type":"error","code":"login_required","message":"no credentials","remediation":"Import credentials","remediationUrl":"https://console.cloud.google.com/"}`,
	}
	for _, line := range lines {
		e, err := ParseEvent([]byte(line))
		if err != nil {
			t.Fatalf("parse %q: %v", line, err)
		}
		if !ValidEventType(e.Type) {
			t.Fatalf("type %q must be known", e.Type)
		}
	}
	if _, err := ParseEvent([]byte(`{"schema":"piccolo.integration.event.v1","type":"open_url"}`)); err == nil {
		t.Fatal("open_url without url must fail")
	}
	if _, err := ParseEvent([]byte(`{"schema":"piccolo.integration.event.v1","type":"confetti"}`)); err == nil {
		t.Fatal("unknown event type must fail")
	}
	if _, err := ParseEvent([]byte(`{"schema":"piccolo.integration.event.v2","type":"step"}`)); err == nil {
		t.Fatal("unknown event schema must fail")
	}
}

func TestEventReaderToleratesNonJSONLines(t *testing.T) {
	stream := strings.Join([]string{
		"some library chatter on stdout",
		`{"schema":"piccolo.integration.event.v1","type":"step","id":"a","status":"ok","message":"done"}`,
		"",
		"also not json",
		`{"schema":"piccolo.integration.event.v1","type":"result","status":"ok","result":{"configPatch":{"tools":{"mcp":{"servers":{"google_workspace":{"command":"piccolo-gws"}}}}}}}`,
	}, "\n")
	er := NewEventReader(strings.NewReader(stream))
	var got []Event
	for {
		e := er.Next()
		if e == nil {
			break
		}
		got = append(got, *e)
	}
	if len(got) != 2 {
		t.Fatalf("events = %d, want 2 (malformed lines must not abort the stream)", len(got))
	}
	if len(er.Logs) != 2 || er.Logs[0] != "some library chatter on stdout" {
		t.Fatalf("logs = %#v", er.Logs)
	}
	res := er.Result()
	if res == nil || res.Result == nil || len(res.Result.ConfigPatch) == 0 {
		t.Fatalf("result patch missing: %#v", res)
	}
	if er.ErrEvent() != nil {
		t.Fatal("no error event expected")
	}
}

func TestEventReaderErrorEvent(t *testing.T) {
	er := NewEventReader(strings.NewReader(`{"schema":"piccolo.integration.event.v1","type":"error","code":"login_required","message":"no credentials"}` + "\n"))
	if er.Next() == nil {
		t.Fatal("expected an event")
	}
	e := er.ErrEvent()
	if e == nil || e.Code != "login_required" {
		t.Fatalf("err event = %#v", e)
	}
	if er.Result() != nil {
		t.Fatal("no result expected")
	}
}
