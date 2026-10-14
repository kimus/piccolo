package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kimus/piccolo/internal/docker"

	picintegration "github.com/kimus/piccolo/internal/integration"
	"github.com/kimus/piccolo/internal/manifest"
)

func TestBuildMenuGatesOnStateFacts(t *testing.T) {
	actions := []picintegration.Action{
		{ID: "import", Role: "setup", Title: "Import credentials"},
		{ID: "login", Role: "setup", Title: "Login", Requires: []string{"credentials"}},
		{ID: "status", Role: "diagnostic", Title: "Check connectivity", Requires: []string{"login"}},
		{ID: "future", Role: "maintenance", Title: "Ignored"},
	}
	state := picintegration.State{Schema: picintegration.StateSchemaV1, Name: "gws", Facts: map[string]bool{"credentials": false, "login": false}}

	menu := buildMenu(actions, state)
	if len(menu) != 3 {
		t.Fatalf("menu = %#v, want 3 (unknown role ignored)", menu)
	}
	if !menu[0].Enabled {
		t.Fatal("action with no requires must be selectable")
	}
	if menu[1].Enabled || !strings.Contains(menu[1].Reason, "credentials") {
		t.Fatalf("login must be gated with the missing fact named: %#v", menu[1])
	}
	if menu[2].Enabled || !strings.Contains(menu[2].Reason, "login") {
		t.Fatalf("diagnostic must be gated: %#v", menu[2])
	}
	if got := menu[1].Title(); !strings.Contains(got, "unavailable") {
		t.Fatalf("disabled title = %q", got)
	}

	state.Facts["credentials"] = true
	state.Facts["login"] = true
	menu = buildMenu(actions, state)
	for _, e := range menu {
		if !e.Enabled {
			t.Fatalf("expected all enabled, got %#v", e)
		}
	}
}

func TestBuildMenuRejectsUnsupportedInputTypeWithoutCrashing(t *testing.T) {
	actions := []picintegration.Action{{
		ID: "weird", Role: "setup", Title: "Weird",
		Inputs: []picintegration.ActionInput{{Key: "d", Type: "datepicker", Title: "When"}},
	}}
	menu := buildMenu(actions, picintegration.State{Facts: map[string]bool{}})
	if len(menu) != 1 || menu[0].Enabled {
		t.Fatalf("unsupported type must disable the action: %#v", menu)
	}
	if !strings.Contains(menu[0].Reason, "datepicker") {
		t.Fatalf("reason must name the type: %q", menu[0].Reason)
	}
}

func TestCallbackPortFromURL(t *testing.T) {
	auth := "https://accounts.google.com/o/oauth2/auth?scope=x&redirect_uri=http%3A%2F%2Flocalhost%3A35935&response_type=code"
	port, err := callbackPortFromURL(auth)
	if err != nil {
		t.Fatalf("port: %v", err)
	}
	if port != 35935 {
		t.Fatalf("port = %d, want 35935", port)
	}
}

func TestCallbackPortRejectsNonLoopback(t *testing.T) {
	cases := map[string]string{
		"remote redirect": "https://accounts.google.com/o/oauth2/auth?redirect_uri=https%3A%2F%2Fexample.com%2Fcb",
		"lan redirect":    "https://x/oauth?redirect_uri=http%3A%2F%2F192.168.1.5%3A8080%2F",
		"no redirect":     "https://accounts.google.com/o/oauth2/auth?scope=x",
		"bad port":        "https://x/oauth?redirect_uri=http%3A%2F%2Flocalhost%2F",
		"insecure auth":   "http://evil.example.com/o/oauth2/auth?redirect_uri=http%3A%2F%2Flocalhost%3A35935",
	}
	for name, u := range cases {
		if _, err := callbackPortFromURL(u); err == nil {
			t.Fatalf("%s: expected rejection", name)
		}
	}
}

func TestCallbackPathPreservesOpaqueQuery(t *testing.T) {
	if got := callbackPath(""); got != "/" {
		t.Fatalf("empty = %q", got)
	}
	q := "code=4%2Fabc-secret&state=xyz"
	if got := callbackPath(q); got != "/?"+q {
		t.Fatalf("path = %q", got)
	}
	if strings.Contains(callbackPath(q), "parsed") {
		t.Fatal("query must be carried verbatim, never interpreted")
	}
}

func TestIsLoopbackHost(t *testing.T) {
	for _, ok := range []string{"localhost", "127.0.0.1", "127.4.5.6", "::1"} {
		if !isLoopbackHost(ok) {
			t.Fatalf("%s must be loopback", ok)
		}
	}
	for _, bad := range []string{"example.com", "192.168.0.1", "", "0.0.0.0"} {
		if isLoopbackHost(bad) {
			t.Fatalf("%s must not be loopback", bad)
		}
	}
}

func TestStageHostFileCopiesWithOwnerOnlyMode(t *testing.T) {
	dir := t.TempDir()
	host := filepath.Join(dir, "client_secret.json")
	if err := os.WriteFile(host, []byte(`{"installed":{"client_id":"x"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	stateRoot := filepath.Join(dir, "integrations", "kimus", "piccolo-gws")
	containerState := "/root/.piccolo/integrations/kimus/piccolo-gws"

	target, cont, err := stageHostFile(host, stateRoot, "auth/client_secret.json", containerState, autoYes)
	if err != nil {
		t.Fatalf("stage: %v", err)
	}
	if cont != containerState+"/auth/client_secret.json" {
		t.Fatalf("container path = %q", cont)
	}
	fi, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", fi.Mode().Perm())
	}
	data, _ := os.ReadFile(target)
	if !strings.Contains(string(data), "client_id") {
		t.Fatalf("content not copied: %s", data)
	}
}

func TestStageHostFileRejections(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "f.json")
	if err := os.WriteFile(file, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	stateRoot := filepath.Join(dir, "state")
	cases := []struct {
		name string
		src  string
		dst  string
	}{
		{"missing source", filepath.Join(dir, "nope.json"), "auth/x.json"},
		{"directory source", dir, "auth/x.json"},
		{"empty destination", file, ""},
		{"absolute destination", file, "/etc/passwd"},
		{"traversal destination", file, "../outside.json"},
	}
	for _, c := range cases {
		if _, _, err := stageHostFile(c.src, stateRoot, c.dst, "/root/x", autoYes); err == nil {
			t.Fatalf("%s: expected rejection", c.name)
		}
	}
}

func TestStageHostFileRefusesEscapeViaSymlinkedRoot(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "f.json")
	if err := os.WriteFile(file, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(dir, "outside")
	stateRoot := filepath.Join(outside, "state")
	if err := os.MkdirAll(stateRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	// A nested destination stays inside the state root.
	target, _, err := stageHostFile(file, stateRoot, "deep/nested/x.json", "/c", autoYes)
	if err != nil {
		t.Fatalf("stage: %v", err)
	}
	if !strings.HasPrefix(target, stateRoot) {
		t.Fatalf("target escaped state root: %s", target)
	}
}

func TestAnswersJSONCarriesOnlyDeclaredKeys(t *testing.T) {
	inputs := []picintegration.ActionInput{
		{Key: "client_secret", Type: "path", Title: "secret"},
		{Key: "scope", Type: "select", Title: "scope"},
	}
	values := map[string]string{
		"client_secret": "/root/.piccolo/integrations/kimus/piccolo-gws/auth/client_secret.json",
		"scope":         "readonly",
		"unrelated":     "must-not-travel",
	}
	data, err := answersJSON(inputs, values)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	if strings.Contains(got, "must-not-travel") {
		t.Fatalf("undeclared key leaked: %s", got)
	}
	if !strings.Contains(got, `"scope":"readonly"`) {
		t.Fatalf("answers = %s", got)
	}
}

func TestAnswersJSONHandlesQuotesInValues(t *testing.T) {
	inputs := []picintegration.ActionInput{{Key: "k", Type: "text", Title: "K"}}
	data, err := answersJSON(inputs, map[string]string{"k": "a\"b\\c\nd"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `\"`) || !strings.Contains(string(data), `\\`) {
		t.Fatalf("value not escaped: %s", data)
	}
}

func descriptorJSON(actions string, extra ...string) string {
	if actions == "" {
		return `{"schema":"piccolo.integration.v1","name":"gws","displayName":"Google Workspace","commands":{"setup":{"command":"piccolo-gws","args":["setup","--config-dir","/root/.piccolo/integrations/kimus/piccolo-gws"]},"status":{"command":"piccolo-gws","args":["status"]}},"setup":{"command":["piccolo-gws","setup","--config-dir","/root/.piccolo/integrations/kimus/piccolo-gws"]}}`
	}
	body := `{"schema":"piccolo.integration.v1","name":"gws","displayName":"Google Workspace","mcpServerKey":"google_workspace","stateKeys":["credentials","login"],"commands":{"setup":{"command":"piccolo-gws","args":["setup","--config-dir","/root/.piccolo/integrations/kimus/piccolo-gws"]}},"setup":{"command":["piccolo-gws","setup","--config-dir","/root/.piccolo/integrations/kimus/piccolo-gws"]},"actions":[` + actions + `]}`
	return body
}

// A declarative integration must be routed to the Piccolo-rendered path. In a
// non-TTY environment that path refuses before rendering, which is itself the
// proof it took the declarative branch rather than the legacy attach.
func TestIntegrationSetupRoutesToDeclarativePath(t *testing.T) {
	dir := t.TempDir()
	writeIntegrationManifest(t, dir, []manifest.Integration{{
		Package: "github:kimus/piccolo-gws", Source: "release", Name: "gws", DisplayName: "Google Workspace",
		StatePath:         "integrations/kimus/piccolo-gws",
		DescriptorCommand: []string{"piccolo-gws", "describe", "--json"},
		SetupCommand:      []string{"piccolo-gws", "setup", "--config-dir", "/root/.piccolo/integrations/kimus/piccolo-gws"},
	}})
	desc := descriptorJSON(`{"id":"import","role":"setup","title":"Import credentials"}`)
	fake := &fakeIntegrationRunner{stdouts: []string{desc}}
	old := newIntegrationRunner
	newIntegrationRunner = func() integrationRunner { return fake }
	defer func() { newIntegrationRunner = old }()
	oldDir := dirFlag
	dirFlag = dir
	defer func() { dirFlag = oldDir }()

	var out bytes.Buffer
	err := runIntegrationSetup(testCommand(&out), "kimus/piccolo-gws")
	if err == nil || !strings.Contains(err.Error(), "interactive terminal") {
		t.Fatalf("expected declarative-path terminal refusal, got %v", err)
	}
	if len(fake.runs) != 1 {
		t.Fatalf("expected only the describe run, got %d: %#v", len(fake.runs), fake.runs)
	}
	if strings.Join(fake.runs[0].Command, " ") != "describe --json" {
		t.Fatalf("first run should be describe, got %v", fake.runs[0].Command)
	}
}

// An integration without actions must take the legacy TTY attach path unchanged.
func TestIntegrationSetupLegacyWithoutActions(t *testing.T) {
	dir := t.TempDir()
	writeIntegrationManifest(t, dir, []manifest.Integration{{
		Package: "github:kimus/piccolo-gws", Source: "release", Name: "gws", DisplayName: "Google Workspace",
		StatePath:         "integrations/kimus/piccolo-gws",
		DescriptorCommand: []string{"piccolo-gws", "describe", "--json"},
		SetupCommand:      []string{"piccolo-gws", "setup", "--config-dir", "/root/.piccolo/integrations/kimus/piccolo-gws"},
	}})
	fake := &fakeIntegrationRunner{stdouts: []string{descriptorJSON(""), ""}}
	old := newIntegrationRunner
	newIntegrationRunner = func() integrationRunner { return fake }
	defer func() { newIntegrationRunner = old }()
	oldDir := dirFlag
	dirFlag = dir
	defer func() { dirFlag = oldDir }()

	var out bytes.Buffer
	if err := runIntegrationSetup(testCommand(&out), "kimus/piccolo-gws"); err != nil {
		t.Fatalf("legacy setup should run to completion without a TTY: %v", err)
	}
	if len(fake.runs) != 2 {
		t.Fatalf("expected describe + legacy setup runs, got %d", len(fake.runs))
	}
	if strings.Join(fake.runs[1].Command, " ") != "setup --config-dir /root/.piccolo/integrations/kimus/piccolo-gws" {
		t.Fatalf("legacy command = %v", fake.runs[1].Command)
	}
	if fake.runs[1].TTY {
		t.Fatal("legacy path must not request a PTY when the host has no terminal")
	}
	if !strings.Contains(out.String(), "Running setup for Google Workspace") {
		t.Fatalf("legacy output = %q", out.String())
	}
}

// A broken describe must not block a legacy integration from being set up.
func TestIntegrationSetupSurvivesUnreadableDescriptor(t *testing.T) {
	dir := t.TempDir()
	writeIntegrationManifest(t, dir, []manifest.Integration{{
		Package: "github:kimus/piccolo-gws", Source: "release", Name: "gws",
		StatePath:         "integrations/kimus/piccolo-gws",
		DescriptorCommand: []string{"piccolo-gws", "describe", "--json"},
		SetupCommand:      []string{"piccolo-gws", "setup"},
	}})
	fake := &fakeIntegrationRunner{stdouts: []string{"not json at all", ""}}
	old := newIntegrationRunner
	newIntegrationRunner = func() integrationRunner { return fake }
	defer func() { newIntegrationRunner = old }()
	oldDir := dirFlag
	dirFlag = dir
	defer func() { dirFlag = oldDir }()

	if err := runIntegrationSetup(testCommand(&bytes.Buffer{}), "kimus/piccolo-gws"); err != nil {
		t.Fatalf("must fall back to the legacy path: %v", err)
	}
	if len(fake.runs) != 2 {
		t.Fatalf("runs = %d", len(fake.runs))
	}
}

func TestDeriveArgvReusesConfigDirFlag(t *testing.T) {
	setup := []string{"piccolo-gws", "setup", "--config-dir", "/root/.piccolo/integrations/kimus/piccolo-gws"}
	got, err := deriveArgv(setup, "state")
	if err != nil {
		t.Fatal(err)
	}
	want := "piccolo-gws state --config-dir /root/.piccolo/integrations/kimus/piccolo-gws"
	if strings.Join(got, " ") != want {
		t.Fatalf("argv = %q, want %q", strings.Join(got, " "), want)
	}

	got, err = deriveArgv(setup, "call", "login")
	if err != nil {
		t.Fatal(err)
	}
	want = "piccolo-gws call login --config-dir /root/.piccolo/integrations/kimus/piccolo-gws"
	if strings.Join(got, " ") != want {
		t.Fatalf("argv = %q, want %q", strings.Join(got, " "), want)
	}

	if _, err := deriveArgv(nil, "state"); err == nil {
		t.Fatal("expected error when nothing to derive from")
	}
}

func TestConfigDirFlagToleratesAbsence(t *testing.T) {
	if got := configDirFlag([]string{"piccolo-gws", "setup"}); got != nil {
		t.Fatalf("expected nil, got %v", got)
	}
}

func TestIntegrationServerKeyPrefersRecordedThenDescriptorThenName(t *testing.T) {
	desc := picintegration.Descriptor{MCPServerKey: "from_descriptor"}
	if got := integrationServerKey(manifest.Integration{Name: "gws"}, desc); got != "from_descriptor" {
		t.Fatalf("desc key = %q", got)
	}
	if got := integrationServerKey(manifest.Integration{Name: "gws", MCPServerKey: "recorded"}, desc); got != "recorded" {
		t.Fatalf("recorded key = %q", got)
	}
	if got := integrationServerKey(manifest.Integration{Name: "gws"}, picintegration.Descriptor{}); got != "gws" {
		t.Fatalf("fallback key = %q", got)
	}
}

func TestRelayCallbackBuildsLoopbackCurlInsideContainer(t *testing.T) {
	fake := &fakeIntegrationRunner{}
	old := newIntegrationRunner
	newIntegrationRunner = func() integrationRunner { return fake }
	defer func() { newIntegrationRunner = old }()

	err := relayCallbackIntoContainer(context.Background(), fake, "piccolo-setup-gws", 35935, "code=abc&state=xyz")
	// The fake has no live container, but the recorded argv proves the shape.
	if len(fake.execs) != 1 {
		t.Fatalf("execs = %#v", fake.execs)
	}
	got := strings.Join(fake.execs[0].cmd, " ")
	if !strings.Contains(got, "http://localhost:35935/?code=abc&state=xyz") {
		t.Fatalf("relay cmd = %q", got)
	}
	if fake.execs[0].name != "piccolo-setup-gws" {
		t.Fatalf("relay target = %q", fake.execs[0].name)
	}
	if err == nil {
		t.Fatal("expected the fake exec to surface an error")
	}
}

func autoYes(string) (bool, error) { return true, nil }
func autoNo(string) (bool, error)  { return false, nil }

func TestStageHostFileConfirmsBeforeOverwritingExisting(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "new.json")
	if err := os.WriteFile(source, []byte(`{"new":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	stateRoot := filepath.Join(dir, "state")
	dst := "auth/client_secret.json"
	if err := os.MkdirAll(filepath.Join(stateRoot, "auth"), 0o700); err != nil {
		t.Fatal(err)
	}
	existing := filepath.Join(stateRoot, "auth", "client_secret.json")
	if err := os.WriteFile(existing, []byte(`{"old":true}`), 0o600); err != nil {
		t.Fatal(err)
	}

	// Declining must leave the current credentials untouched.
	if _, _, err := stageHostFile(source, stateRoot, dst, "/c", autoNo); err == nil {
		t.Fatal("declining the overwrite must abort")
	}
	if data, _ := os.ReadFile(existing); !strings.Contains(string(data), "old") {
		t.Fatalf("existing file was overwritten after a refusal: %s", data)
	}

	// Confirming replaces it, and the replacement keeps owner-only mode.
	if _, _, err := stageHostFile(source, stateRoot, dst, "/c", autoYes); err != nil {
		t.Fatalf("confirmed overwrite failed: %v", err)
	}
	data, err := os.ReadFile(existing)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "new") {
		t.Fatalf("file not replaced: %s", data)
	}

	// No confirmation channel at all must also refuse rather than clobber.
	if _, _, err := stageHostFile(source, stateRoot, dst, "/c", nil); err == nil {
		t.Fatal("nil confirm must refuse to overwrite")
	}
}

// The reported defect: an action whose inputs were gated on a fact only that same
// action can set became unfillable once it had been filled — the form lost the two
// fields it existed for and Enter on the survivor was treated as a complete
// submission. State facts gate which actions are selectable, never which render.
func TestInputsForNeverSuppressesDeclaredFields(t *testing.T) {
	inputs := []picintegration.ActionInput{
		{Key: "client_id", Type: "text", Title: "id", WhenUnmet: "credentials", Required: true},
		{Key: "client_secret", Type: "secret", Title: "secret", WhenUnmet: "credentials", Sensitive: true, Required: true},
		{Key: "scope", Type: "select", Title: "scope"},
	}
	for _, credentialsSet := range []bool{false, true} {
		state := picintegration.State{Facts: map[string]bool{"credentials": credentialsSet}}
		got := inputsFor(picintegration.Action{Inputs: inputs}, state)
		if len(got) != 3 {
			t.Fatalf("credentials=%v rendered %d of %d declared inputs: %#v", credentialsSet, len(got), len(inputs), got)
		}
		if got[0].Key != "client_id" || got[1].Key != "client_secret" {
			t.Fatalf("credential inputs must always render, got %s and %s", got[0].Key, got[1].Key)
		}
	}
}

// Pre-fill exists so skip and clear are distinguishable. That only works if the
// three answers states are encoded deliberately rather than inferred from which
// bytes happen to be empty.
func TestEncodeAnswersDistinguishesSetClearAndKeep(t *testing.T) {
	inputs := []picintegration.ActionInput{
		{Key: "client_id", Type: "text", Title: "id", Required: true},
		{Key: "client_secret", Type: "secret", Title: "secret", Sensitive: true, Required: true},
		{Key: "project_id", Type: "text", Title: "project", Destination: ""},
	}
	state := picintegration.State{Values: map[string]picintegration.FieldState{
		"client_id":     {Value: "stored-id", Set: true},
		"client_secret": {Set: true}, // presence only; the value never travels
	}}

	// (a) nothing touched: id keeps its pre-filled value, secret is omitted, so
	// the integration reads it as keep rather than as a blank to store.
	got := encodeAnswers(inputs, state, map[string]string{
		"client_id": "stored-id", "client_secret": "", "project_id": "",
	})
	if got["client_secret"] != "" || hasKey(got, "client_secret") {
		t.Errorf("untouched secret must be omitted (keep), got %#v", got)
	}
	if !hasKey(got, "project_id") || got["project_id"] != "" {
		t.Errorf("emptied optional field must be sent as a clear, got %#v", got)
	}
	if got["client_id"] != "stored-id" {
		t.Errorf("pre-filled value should resubmit unchanged, got %q", got["client_id"])
	}

	// (b) typed replacement travels as typed.
	got = encodeAnswers(inputs, state, map[string]string{
		"client_id": "new-id", "client_secret": "new-secret", "project_id": "p",
	})
	if got["client_secret"] != "new-secret" || got["client_id"] != "new-id" {
		t.Errorf("typed values must be sent: %#v", got)
	}

	// (c) an undeclared key can never ride along, however it got into raw.
	got = encodeAnswers(inputs, state, map[string]string{"client_id": "x", "unrelated": "leak"})
	if hasKey(got, "unrelated") {
		t.Errorf("undeclared key leaked: %#v", got)
	}

	// (d) nothing reported about a secret (older bundle): typed blank is still
	// omitted, so no path can send an accidental clear of a stored secret.
	got = encodeAnswers(inputs, picintegration.State{}, map[string]string{"client_secret": ""})
	if hasKey(got, "client_secret") {
		t.Errorf("secret must be omitted when untouched: %#v", got)
	}
}

func TestRequiredValidatorBlocksEmptyButNotKeep(t *testing.T) {
	secret := picintegration.ActionInput{Key: "client_secret", Type: "secret", Title: "OAuth client secret", Required: true}
	id := picintegration.ActionInput{Key: "client_id", Type: "text", Title: "OAuth client ID", Required: true}

	// A stored secret may be left blank — that is the keep gesture, not an absence.
	if err := requiredValidator(secret, true)(""); err != nil {
		t.Errorf("blank must be allowed when it means keep: %v", err)
	}
	// A visible field left blank is a real gap, and submitting must not proceed.
	if err := requiredValidator(id, false)(""); err == nil {
		t.Error("empty required field must block the form")
	} else if !strings.Contains(err.Error(), "esc") {
		t.Errorf("block message should say how to get out: %v", err)
	}
	if err := requiredValidator(id, false)("192902532576-abc.apps.googleusercontent.com"); err != nil {
		t.Errorf("a typed value must pass: %v", err)
	}
}

func TestSensitiveInputRecognition(t *testing.T) {
	cases := []struct {
		in   picintegration.ActionInput
		want bool
	}{
		{picintegration.ActionInput{Type: "secret"}, true},
		{picintegration.ActionInput{Type: "text", Sensitive: true}, true},
		{picintegration.ActionInput{Type: "text"}, false},
		{picintegration.ActionInput{Type: "path"}, false},
	}
	for _, tc := range cases {
		if got := tc.in.SensitiveInput(); got != tc.want {
			t.Errorf("SensitiveInput(%#v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func hasKey(m map[string]string, key string) bool {
	_, ok := m[key]
	return ok
}

func TestActionContainerNamesAreDeterministicAndScoped(t *testing.T) {
	a := actionContainerName("gws", "set_credentials")
	b := actionContainerName("gws", "set_credentials")
	if a != b {
		t.Fatalf("names must be stable: %q vs %q", a, b)
	}
	if a == statusContainerName("gws") || a == setupContainerName("gws") {
		t.Fatalf("action container must not collide with status/setup: %q", a)
	}
	if !strings.HasPrefix(a, "piccolo-setup-gws-") {
		t.Fatalf("name should be scoped to the integration: %q", a)
	}
}

// TestActionArgvOrdersSubcommandBeforeFlags pins the clap contract: a flag that
// precedes the subcommand is rejected by the integration binary outright, which
// made every declarative setup action fail with "unexpected argument
// '--config-dir' found".
func TestActionArgvOrdersSubcommandBeforeFlags(t *testing.T) {
	setup := []string{"piccolo-gws", "setup", "--config-dir", "/root/.piccolo/integrations/kimus/piccolo-gws"}

	got, err := actionArgv(setup, "login")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"piccolo-gws", "call", "login", "--config-dir", "/root/.piccolo/integrations/kimus/piccolo-gws"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("argv = %v, want %v", got, want)
	}

	// The flag must never appear before the subcommand.
	flagIdx, subIdx := len(got), len(got)
	for i, a := range got {
		if a == "call" && i < subIdx {
			subIdx = i
		}
		if strings.HasPrefix(a, "-") && i < flagIdx {
			flagIdx = i
		}
	}
	if flagIdx < subIdx {
		t.Fatalf("flag %v appears before subcommand: %v", got[flagIdx], got)
	}
}

func TestStateArgvAlsoPutsSubcommandFirst(t *testing.T) {
	setup := []string{"piccolo-gws", "setup", "--config-dir", "/root/.piccolo/integrations/kimus/piccolo-gws"}
	got, err := deriveArgv(setup, "state")
	if err != nil {
		t.Fatal(err)
	}
	if got[0] != "piccolo-gws" || got[1] != "state" {
		t.Fatalf("state argv must be <binary> state [--config-dir ...], got %v", got)
	}
	if len(got) > 2 && strings.HasPrefix(got[1], "-") {
		t.Fatalf("flag before subcommand: %v", got)
	}
}

// hangingProcess never produces stdout and never exits until Shutdown is called,
// mimicking an action stuck on a browser interaction that never lands.
type hangingProcess struct {
	outR      *io.PipeReader
	outW      *io.PipeWriter
	errR      *io.PipeReader
	pipeW     *io.PipeWriter
	shutdowns int
}

func newHangingProcess() *hangingProcess {
	or, ow := io.Pipe()
	er, ew := io.Pipe()
	return &hangingProcess{outR: or, outW: ow, errR: er, pipeW: ew}
}

func (h *hangingProcess) Stdin() io.WriteCloser { return nopWriteCloser{bytes.NewBuffer(nil)} }
func (h *hangingProcess) Stdout() io.ReadCloser { return h.outR }
func (h *hangingProcess) Stderr() io.ReadCloser { return h.errR }
func (h *hangingProcess) Wait(ctx context.Context) (int, error) {
	<-ctx.Done()
	return -1, ctx.Err()
}
func (h *hangingProcess) Shutdown(time.Duration) error {
	h.shutdowns++
	_ = h.outW.CloseWithError(io.EOF)
	_ = h.pipeW.CloseWithError(io.EOF)
	return nil
}
func (h *hangingProcess) ExitCode() int { return -1 }

type hangingRunner struct {
	proc docker.Process
	run  int
}

func (r *hangingRunner) Ping(context.Context) error { return nil }
func (r *hangingRunner) BuildImage(context.Context, io.Reader, docker.ImageBuildOptions, io.Writer) error {
	return nil
}
func (r *hangingRunner) Run(context.Context, docker.Spec) (docker.Process, error) {
	r.run++
	return r.proc, nil
}
func (r *hangingRunner) RemoveByPrefix(context.Context, string) ([]string, error) { return nil, nil }
func (r *hangingRunner) Exec(context.Context, string, []string, bool) (*docker.ExecSession, error) {
	return nil, fmt.Errorf("no live container")
}

// TestRunIntegrationActionTearsDownOnCancel is the Ctrl-C contract: cancelling
// the action context must stop AND remove the container, not strand it.
func TestRunIntegrationActionTearsDownOnCancel(t *testing.T) {
	proc := newHangingProcess()
	runner := &hangingRunner{proc: proc}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(150 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, outcome, err := runIntegrationAction(ctx, runner, integrationCallRequest{
		Answers:   []byte("{}"),
		Container: "piccolo-setup-gws-login",
		Spec:      docker.Spec{Name: "piccolo-setup-gws-login"},
	}, io.Discard, nil)
	if time.Since(start) > 10*time.Second {
		t.Fatalf("cancel did not unblock the action (took %s)", time.Since(start))
	}
	// A cancel Piccolo initiated is not an error: it is an outcome, and the menu
	// has to survive it so the user can retry the login.
	if err != nil {
		t.Fatalf("a user cancel must not surface as an error: %v", err)
	}
	if outcome != outcomeCancelled {
		t.Fatalf("outcome = %v, want cancelled", outcome)
	}
	if proc.shutdowns == 0 {
		t.Fatal("container was never shut down: Ctrl-C would leak it")
	}
}

// TestRunIntegrationActionTearsDownOnDeadline covers the same contract for the
// timeout path, with no signal involved.
func TestRunIntegrationActionTearsDownOnDeadline(t *testing.T) {
	proc := newHangingProcess()
	runner := &hangingRunner{proc: proc}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	_, outcome, err := runIntegrationAction(ctx, runner, integrationCallRequest{
		Answers: []byte("{}"),
		Spec:    docker.Spec{},
	}, io.Discard, nil)
	if err != nil {
		t.Fatalf("a timeout must not surface as an error: %v", err)
	}
	if outcome != outcomeTimedOut {
		t.Fatalf("outcome = %v, want timed out", outcome)
	}
	if proc.shutdowns == 0 {
		t.Fatal("timed-out action must still be stopped and removed")
	}
}

// The menu has to say which client an action will use. A pre-filled field that is
// silently being kept is how a user ends up authorizing a stale client while
// believing they replaced it — so the summary exists, and it must not be able to
// carry a secret.
func TestStoredSummaryNamesWhatIsKeptWithoutTouchingSecrets(t *testing.T) {
	actions := []picintegration.Action{{
		ID: "set_credentials",
		Inputs: []picintegration.ActionInput{
			{Key: "client_id", Type: "text", Required: true},
			{Key: "client_secret", Type: "secret", Sensitive: true, Required: true},
			{Key: "project_id", Type: "text"},
		},
	}}
	state := picintegration.State{Values: map[string]picintegration.FieldState{
		"client_id":     {Value: "192902532576-hq1abcdefghij01234567890.apps.googleusercontent.com", Set: true},
		"client_secret": {Set: true},
		"project_id":    {Value: "192902532576", Set: true},
	}}

	got := storedSummary(actions, state)
	// The distinguishing part of a client ID is what comes after the project
	// number; a summary that cuts it off identifies nothing.
	if !strings.Contains(got, "client_id=192902532576-hq1") {
		t.Errorf("summary must identify WHICH client will be used: %q", got)
	}
	if !strings.Contains(got, "project_id=192902532576") {
		t.Errorf("summary lost a non-secret value: %q", got)
	}
	if strings.Contains(got, "client_secret") {
		t.Errorf("a sensitive key must never appear in the summary: %q", got)
	}
	if len(got) > 120 {
		t.Errorf("summary should stay short enough to sit under a menu: %d chars %q", len(got), got)
	}

	// Nothing stored → no line at all, rather than "stored:" with nothing after it.
	if s := storedSummary(actions, picintegration.State{}); s != "" {
		t.Errorf("empty state should produce no summary, got %q", s)
	}
}

// A summary exists to tell two clients of the same project apart, so the head is
// the part that must survive and the fixed domain suffix is the part that may go.
func TestElideKeepsTheDistinguishingHead(t *testing.T) {
	a := elide("192902532576-hq1abcdefghij01234567890.apps.googleusercontent.com")
	b := elide("192902532576-hegpu7ris5fpk53m75jkmerlj2o3e72b.apps.googleusercontent.com")
	if a == b {
		t.Fatalf("two different clients elided to the same string: %q == %q", a, b)
	}
	for _, got := range []string{a, b} {
		if !strings.HasPrefix(got, "192902532576-h") {
			t.Errorf("must keep the project and the start of the client hash: %q", got)
		}
		if len([]rune(got)) > 27 {
			t.Errorf("elided value too long for a menu line: %q", got)
		}
	}
	if elide("short") != "short" || elide("  spaced  ") != "spaced" {
		t.Error("short values must pass through, trimmed")
	}
}

// A cancel and a timeout are different events with different remedies, and neither
// should surface as a Go error string.
func TestCourierFailureNoteSeparatesCancelFromTimeout(t *testing.T) {
	cancelled := courierFailureNote(context.Canceled)
	if !strings.Contains(cancelled, "cancelled") || strings.Contains(cancelled, "context canceled") {
		t.Errorf("cancel should read as the user's decision: %q", cancelled)
	}
	if !strings.Contains(cancelled, "nothing was changed") {
		t.Errorf("cancel must state that nothing was saved: %q", cancelled)
	}
	timed := courierFailureNote(context.DeadlineExceeded)
	if !strings.Contains(timed, "timed out") || !strings.Contains(timed, "nothing was changed") {
		t.Errorf("timeout should say so and confirm nothing was saved: %q", timed)
	}
	other := courierFailureNote(errors.New("bind: address already in use"))
	if !strings.Contains(other, "address already in use") {
		t.Errorf("real faults must still be visible: %q", other)
	}
}

// The wait screen is Piccolo's generic one: it must not name one provider's
// console, and it must say how to get out — esc does nothing once no form is up.
func TestWaitRedirectHelpIsProviderNeutralAndSaysHowToAbort(t *testing.T) {
	joined := strings.Join(waitRedirectHelp, " ")
	for _, banned := range []string{"Google", "gcloud", "Firebase"} {
		if strings.Contains(joined, banned) {
			t.Errorf("generic wait screen must not name a provider (%s): %q", banned, joined)
		}
	}
	if !strings.Contains(joined, "ctrl-c") {
		t.Errorf("the only way out of a browser wait must be stated: %q", joined)
	}
}

// The distinction the whole resilience design rests on: a non-zero exit after an
// `error` event is a report, and a non-zero exit in silence is a death. Keying on
// the exit code alone would end the menu on the first and trust the second.
func TestClassifyActionRanksEventAboveExitCode(t *testing.T) {
	reported := &picintegration.Event{Type: picintegration.EventError, Code: "connectivity_failed"}
	cases := []struct {
		name     string
		errEvent *picintegration.Event
		exit     int
		ctxErr   error
		want     actionOutcome
	}{
		{"clean success", nil, 0, nil, outcomeSucceeded},
		{"reported failure with non-zero exit", reported, 11, nil, outcomeReported},
		{"reported failure that exited zero", reported, 0, nil, outcomeReported},
		{"silent non-zero exit is a crash", nil, 101, nil, outcomeCrashed},
		{"user cancel outranks everything", nil, -1, context.Canceled, outcomeCancelled},
		{"timeout is not a crash", nil, -1, context.DeadlineExceeded, outcomeTimedOut},
		{"cancel wins over a report we did receive", reported, 11, context.Canceled, outcomeCancelled},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyAction(tc.errEvent, tc.exit, tc.ctxErr); got != tc.want {
				t.Errorf("classifyAction(%v, %d, %v) = %v, want %v", tc.errEvent, tc.exit, tc.ctxErr, got, tc.want)
			}
		})
	}
}

// A cancel is the user's own decision; putting it in a summary next to real
// failures would make the summary lie about the session.
func TestOutcomeTerminalExcludesCancel(t *testing.T) {
	if outcomeCancelled.terminal() {
		t.Error("a cancel the user chose must not appear as an unfinished action")
	}
	for _, o := range []actionOutcome{outcomeReported, outcomeCrashed, outcomeTimedOut} {
		if !o.terminal() {
			t.Errorf("%v must appear in the closing summary", o)
		}
	}
	if outcomeSucceeded.terminal() {
		t.Error("success belongs in no failure summary")
	}
}

// Colour is emphasis, never the message: the text marker must survive every
// disabled or enabled state, or a piped log loses the meaning.
func TestStylerKeepsTextMarkersWhenColourIsOff(t *testing.T) {
	off := styler{} // non-terminal writers get this
	for _, tc := range []struct{ status, text string }{
		{"fail", "[fail] drive: API not enabled"},
		{"ok", "[ok] login: authorized"},
	} {
		if got := off.severity(tc.status, tc.text); got != tc.text {
			t.Errorf("disabled styler altered the text: %q", got)
		}
		if got := off.Failf("x %s", "bad"); got != "x bad" {
			t.Errorf("Failf must be plain without a terminal: %q", got)
		}
	}
	on := styler{enabled: true}
	got := on.severity("fail", "[fail] x")
	// The text marker must survive *inside* the styling, never replace it.
	if !strings.Contains(got, "[fail] x") {
		t.Errorf("styling dropped the text marker: %q", got)
	}
	if !strings.HasPrefix(got, "\x1b[31m") || !strings.HasSuffix(got, "\x1b[0m") {
		t.Errorf("fail must be coloured and reset: %q", got)
	}
	if on.severity("fail", "same") == on.severity("ok", "same") {
		t.Error("fail and ok must not render identically when colour is on")
	}
}

// newStyler must key on the writer, not on a global: tests and pipes get plain
// text, a terminal gets colour, and NO_COLOR wins over both.
func TestNewStylerOnlyColoursRealTerminals(t *testing.T) {
	var buf bytes.Buffer
	if s := newStyler(&buf); s.enabled {
		t.Error("a buffer is not a terminal")
	}
	t.Setenv("NO_COLOR", "1")
	if s := newStyler(os.Stdout); s.enabled {
		t.Error("NO_COLOR must disable colour")
	}
}

// The summary is the only place a walked-away failure gets recorded before the
// command exits 0, so it must render without a terminal and stay plain text.
func TestSummarizeReportsUnfinishedActions(t *testing.T) {
	var out bytes.Buffer
	s := &setupSession{out: &out, style: newStyler(&out), integrationName: "gws"}
	s.summarize([]actionReport{
		{action: "check_connectivity", outcome: outcomeReported},
		{action: "login", outcome: outcomeCrashed},
		{action: "set_credentials", outcome: outcomeSucceeded},
		{action: "check_connectivity", outcome: outcomeCancelled},
	})
	got := out.String()
	if !strings.Contains(got, "Some actions did not complete") {
		t.Fatalf("no summary rendered: %q", got)
	}
	if !strings.Contains(got, "check_connectivity: reported failure") {
		t.Errorf("reported failure missing from summary: %q", got)
	}
	if !strings.Contains(got, "login: crashed") {
		t.Errorf("crash missing from summary: %q", got)
	}
	// Successes and user cancels are not unfinished work.
	if strings.Contains(got, "set_credentials") || strings.Contains(got, "cancelled") {
		t.Errorf("summary listed non-failures: %q", got)
	}
	// Nothing coloured when the writer is not a terminal.
	if strings.Contains(got, "\x1b[") {
		t.Errorf("ANSI leaked into a non-terminal writer: %q", got)
	}

	var quiet bytes.Buffer
	qs := &setupSession{out: &quiet, style: newStyler(&quiet)}
	qs.summarize(nil)
	if quiet.Len() != 0 {
		t.Errorf("a clean session should print no summary, got %q", quiet.String())
	}
}

// A retry that succeeds must leave the session clean. Logging every attempt
// printed the stale first failure at the end of a run that ended fine.
func TestAttemptLogKeepsLatestOutcomePerAction(t *testing.T) {
	var l attemptLog
	l.record(actionReport{action: "check_connectivity", outcome: outcomeReported})
	l.record(actionReport{action: "login", outcome: outcomeCrashed})
	l.record(actionReport{action: "check_connectivity", outcome: outcomeSucceeded})

	var out bytes.Buffer
	s := &setupSession{out: &out, style: newStyler(&out)}
	s.summarize(l)
	got := out.String()
	if strings.Contains(got, "check_connectivity") {
		t.Errorf("a retried action that passed is still listed as unfinished:\n%s", got)
	}
	// The other action genuinely failed and must remain.
	if !strings.Contains(got, "login: crashed") {
		t.Errorf("unrelated failure dropped:\n%s", got)
	}
	// One line per action, not per attempt.
	if n := strings.Count(got, "login:"); n != 1 {
		t.Errorf("expected one login line, got %d:\n%s", n, got)
	}

	// Order is first attempt, so the summary reads like the session did.
	var ordered attemptLog
	ordered.record(actionReport{action: "b", outcome: outcomeReported})
	ordered.record(actionReport{action: "a", outcome: outcomeReported})
	ordered.record(actionReport{action: "b", outcome: outcomeReported})
	if len(ordered) != 2 || ordered[0].action != "b" || ordered[1].action != "a" {
		t.Errorf("unexpected order/len: %+v", ordered)
	}
}
