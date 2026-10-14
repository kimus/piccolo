package setup

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/kimus/piccolo/internal/configdoc"
)

func TestValidateIntegrationPatchAllowsOnlyOwnMCPServer(t *testing.T) {
	patch := json.RawMessage(`{"tools":{"mcp":{"servers":{"google_workspace":{"enabled":true,"type":"stdio","command":"piccolo-gws","args":["mcp","--config-dir","/root/.piccolo/integrations/kimus/piccolo-gws","--read-only"]}}}}}`)
	if err := ValidateIntegrationPatch(patch, "google_workspace"); err != nil {
		t.Fatalf("allowed patch rejected: %v", err)
	}
}

func TestValidateIntegrationPatchRefusesOutOfScopePaths(t *testing.T) {
	cases := map[string]string{
		"agents":       `{"agents":{"list":[{"id":"main"}]}}`,
		"model keys":   `{"model_list":[{"model_name":"x","api_keys":["sk-live"]}]}`,
		"tool toggle":  `{"tools":{"exec":{"enabled":true}}}`,
		"other server": `{"tools":{"mcp":{"servers":{"someone_else":{"command":"evil"}}}}}`,
		"top-level":    `{"gateway":{"port":1}}`,
	}
	for name, raw := range cases {
		err := ValidateIntegrationPatch(json.RawMessage(raw), "google_workspace")
		if err == nil {
			t.Fatalf("%s: expected rejection", name)
		}
		if !strings.Contains(err.Error(), "outside the allowed") {
			t.Fatalf("%s: unexpected error %v", name, err)
		}
	}
}

func TestValidateIntegrationPatchNamesRejectedPaths(t *testing.T) {
	raw := json.RawMessage(`{"tools":{"mcp":{"servers":{"google_workspace":{"command":"piccolo-gws"},"evil":{"command":"x"}}},"exec":{"enabled":true}},"agents":{"x":1}}`)
	err := ValidateIntegrationPatch(raw, "google_workspace")
	if err == nil {
		t.Fatal("expected rejection")
	}
	for _, want := range []string{"agents.x", "tools.exec.enabled", "tools.mcp.servers.evil.command"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error must name %s, got %v", want, err)
		}
	}
}

func TestValidateIntegrationPatchEmpty(t *testing.T) {
	if err := ValidateIntegrationPatch(nil, "google_workspace"); err != nil {
		t.Fatalf("empty patch is a no-op, got %v", err)
	}
	if err := ValidateIntegrationPatch(json.RawMessage(`{}`), "google_workspace"); err == nil {
		t.Fatal("empty object should be rejected as empty patch")
	}
	if err := ValidateIntegrationPatch(json.RawMessage(`"nope"`), "google_workspace"); err == nil {
		t.Fatal("non-object patch must be rejected")
	}
}

func TestApplyIntegrationPatchPreservesUserContent(t *testing.T) {
	doc, err := configdoc.Parse([]byte(`{
	  "version": 3,
	  "agents": {"list": [{"id": "main"}, {"id": "research"}]},
	  "model_list": [{"model_name": "gpt", "api_keys": ["sk-keepme"]}],
	  "tools": {"web": {"enabled": true}, "exec": {"enabled": false}, "mcp": {"servers": {"other": {"command": "keep"}}}},
	  "unknownSection": {"nested": {"deep": [1, 2, 3]}}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	patch := json.RawMessage(`{"tools":{"mcp":{"servers":{"google_workspace":{"enabled":true,"command":"piccolo-gws"}}}}}`)
	written, err := ApplyIntegrationPatch(doc, patch, "google_workspace")
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if written != "google_workspace" {
		t.Fatalf("written = %q", written)
	}
	out := doc.Marshal()
	for _, want := range []string{"sk-keepme", "keep", "unknownSection", "piccolo-gws", "research"} {
		if !strings.Contains(string(out), want) {
			t.Fatalf("output lost %q:\n%s", want, out)
		}
	}
	// Re-parse to assert structure rather than formatting.
	reloaded, err := configdoc.Parse(out)
	if err != nil {
		t.Fatalf("output does not re-parse: %v", err)
	}
	if _, ok := reloaded.Get("unknownSection"); !ok {
		t.Fatal("unknown section dropped")
	}
	if _, ok := reloaded.Get("model_list"); !ok {
		t.Fatal("model_list dropped")
	}
	agents, ok := reloaded.GetObject("agents")
	if !ok {
		t.Fatal("agents dropped")
	}
	if list, _ := agents.Get("list"); list == nil {
		t.Fatal("agents.list dropped")
	}
	tools, _ := reloaded.GetObject("tools")
	if tools == nil {
		t.Fatal("tools dropped")
	}
	if _, ok := tools.GetObject("web"); !ok {
		t.Fatal("tools.web dropped")
	}
	mcp, ok := tools.GetObject("mcp")
	if !ok {
		t.Fatal("tools.mcp dropped")
	}
	servers, ok := mcp.GetObject("servers")
	if !ok {
		t.Fatal("tools.mcp.servers dropped")
	}
	keys := servers.Keys()
	// Key order is preserved: the pre-existing "other" server stays first.
	if len(keys) != 2 || keys[0] != "other" || keys[1] != "google_workspace" {
		t.Fatalf("server keys/order changed: %#v\n%s", keys, out)
	}
}

func TestApplyIntegrationPatchIdempotentUpdateKeepsPosition(t *testing.T) {
	doc, err := configdoc.Parse([]byte(`{"tools":{"mcp":{"servers":{"a":{"command":"one"},"google_workspace":{"command":"piccolo-gws"}}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	patch := json.RawMessage(`{"tools":{"mcp":{"servers":{"google_workspace":{"command":"piccolo-gws","args":["mcp","--read-only"]}}}}}`)
	if _, err := ApplyIntegrationPatch(doc, patch, "google_workspace"); err != nil {
		t.Fatal(err)
	}
	out := string(doc.Marshal())
	if strings.Index(out, `"a"`) > strings.Index(out, `"google_workspace"`) {
		t.Fatalf("update must preserve position:\n%s", out)
	}
	if !strings.Contains(out, "--read-only") {
		t.Fatalf("update not applied:\n%s", out)
	}
}

func TestApplyIntegrationPatchRefusesBeforeWriting(t *testing.T) {
	doc, err := configdoc.Parse([]byte(`{"tools":{"web":{"enabled":true}}}`))
	if err != nil {
		t.Fatal(err)
	}
	before := string(doc.Marshal())
	_, err = ApplyIntegrationPatch(doc, json.RawMessage(`{"tools":{"exec":{"enabled":true}}}`), "google_workspace")
	if err == nil {
		t.Fatal("expected refusal")
	}
	if string(doc.Marshal()) != before {
		t.Fatal("doc must be untouched after a refused patch")
	}
}

func TestApplyIntegrationPatchCreatesMissingParents(t *testing.T) {
	doc, err := configdoc.Parse([]byte(`{"version":3}`))
	if err != nil {
		t.Fatal(err)
	}
	patch := json.RawMessage(`{"tools":{"mcp":{"servers":{"google_workspace":{"command":"piccolo-gws"}}}}}`)
	if _, err := ApplyIntegrationPatch(doc, patch, "google_workspace"); err != nil {
		t.Fatalf("apply: %v", err)
	}
	tools, ok := doc.GetObject("tools")
	if !ok {
		t.Fatal("tools missing")
	}
	mcp, ok := tools.GetObject("mcp")
	if !ok {
		t.Fatal("mcp missing")
	}
	servers, ok := mcp.GetObject("servers")
	if !ok {
		t.Fatal("servers missing")
	}
	if _, ok := servers.Get("google_workspace"); !ok {
		t.Fatal("server entry missing")
	}
}

func TestApplyIntegrationPatchNoServerEntryIsNoOp(t *testing.T) {
	doc, err := configdoc.Parse([]byte(`{"version":3}`))
	if err != nil {
		t.Fatal(err)
	}
	patch := json.RawMessage(`{"tools":{"mcp":{}}}`)
	written, err := ApplyIntegrationPatch(doc, patch, "google_workspace")
	if err != nil {
		t.Fatalf("expected no-op, got %v", err)
	}
	if written != "" {
		t.Fatalf("written = %q", written)
	}
	if strings.Contains(string(doc.Marshal()), "google_workspace") {
		t.Fatal("no entry should have been created")
	}
}

func TestAllowedIntegrationPatchPathShape(t *testing.T) {
	if got := AllowedIntegrationPatchPath("google_workspace"); got != "tools.mcp.servers.google_workspace" {
		t.Fatalf("path = %q", got)
	}
}
