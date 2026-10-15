package picoclaw

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestAgentModelAcceptsBothForms pins the field PicoClaw actually reads on an
// agents.list entry. Piccolo used to write "model_name" there, which upstream
// ignores — the agent silently kept agents.defaults.model_name.
func TestAgentModelAcceptsBothForms(t *testing.T) {
	var bare Config
	err := json.Unmarshal([]byte(`{"version":3,"agents":{"defaults":{"model_name":"d"},"list":[{"id":"main","model":"fast"}]},"model_list":[]}`), &bare)
	if err != nil {
		t.Fatal(err)
	}
	if got := bare.Agents.List[0].ModelRef(); got != "fast" {
		t.Fatalf("bare-string form: ModelRef = %q", got)
	}

	var object Config
	err = json.Unmarshal([]byte(`{"version":3,"agents":{"list":[{"id":"coder","model":{"primary":"fast","fallbacks":["slow","other"]},"subagents":{"model":{"primary":"planner"}}}]},"model_list":[]}`), &object)
	if err != nil {
		t.Fatal(err)
	}
	a := object.Agents.List[0]
	if got := a.ModelRef(); got != "fast" {
		t.Fatalf("object form: ModelRef = %q", got)
	}
	if len(a.Model.Fallbacks) != 2 || a.Model.Fallbacks[0] != "slow" || a.Model.Fallbacks[1] != "other" {
		t.Fatalf("object form: Fallbacks = %v", a.Model.Fallbacks)
	}
	if a.Subagents == nil || a.Subagents.Model == nil || a.Subagents.Model.Primary != "planner" {
		t.Fatalf("subagents.model not parsed: %+v", a.Subagents)
	}
}

func TestAgentModelMarshalKeepsUpstreamShape(t *testing.T) {
	// No fallbacks → the bare alias, which is what upstream's own marshaller
	// emits and what a hand-written config most often looks like.
	out := marshalAgent(t, Agent{ID: "main", Model: &AgentModel{Primary: "fast"}})
	if !strings.Contains(out, `"model":"fast"`) {
		t.Fatalf("want bare alias, got %s", out)
	}
	if strings.Contains(out, "primary") {
		t.Fatalf("object form written without fallbacks: %s", out)
	}

	out = marshalAgent(t, Agent{ID: "coder", Model: &AgentModel{Primary: "fast", Fallbacks: []string{"slow"}}})
	if !strings.Contains(out, `"primary":"fast"`) || !strings.Contains(out, `"fallbacks":["slow"]`) {
		t.Fatalf("object form not written: %s", out)
	}

	// Absent model omits the key entirely, so generated config.json is unchanged.
	// "model_name"/"model_list" elsewhere in the document must not be mistaken
	// for it, hence the colon.
	out = marshalAgent(t, Agent{ID: "main"})
	if strings.Contains(out, `"model":`) {
		t.Fatalf("nil model should omit the key: %s", out)
	}
}

func marshalAgent(t *testing.T, a Agent) string {
	t.Helper()
	raw, err := json.Marshal(Config{Version: SchemaVersion, Agents: Agents{List: []Agent{a}}})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// TestDefaultsKeepFallbackFields: the failover lists are what keep a chat alive
// when a free endpoint empties out, so the model must be able to carry them.
func TestDefaultsKeepFallbackFields(t *testing.T) {
	raw := `{"version":3,"agents":{"defaults":{"workspace":"w","model_name":"fast","model_fallbacks":["slow"],"image_model":"vision","image_model_fallbacks":["vision2"]}},"model_list":[]}`
	var cfg Config
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatal(err)
	}
	d := cfg.Agents.Defaults
	if d.ModelName != "fast" || len(d.ModelFallbacks) != 1 || d.ModelFallbacks[0] != "slow" {
		t.Fatalf("defaults = %+v", d)
	}
	if d.ImageModel != "vision" || len(d.ImageModelFallbacks) != 1 {
		t.Fatalf("image defaults = %+v", d)
	}
	out, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"model_fallbacks":["slow"]`, `"image_model_fallbacks":["vision2"]`} {
		if !strings.Contains(string(out), want) {
			t.Errorf("missing %s in %s", want, out)
		}
	}
	// And nothing is written when unset: generated workspaces keep their shape.
	fresh, err := json.Marshal(Config{Version: SchemaVersion})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(fresh), "fallbacks") {
		t.Errorf("empty fallback lists should be omitted: %s", fresh)
	}
}
