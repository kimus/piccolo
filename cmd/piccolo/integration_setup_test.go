package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	picintegration "github.com/kimus/piccolo/internal/integration"
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

	target, cont, err := stageHostFile(host, stateRoot, "auth/client_secret.json", containerState)
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
		if _, _, err := stageHostFile(c.src, stateRoot, c.dst, "/root/x"); err == nil {
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
	target, _, err := stageHostFile(file, stateRoot, "deep/nested/x.json", "/c")
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
