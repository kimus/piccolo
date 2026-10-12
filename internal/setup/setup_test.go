package setup

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/kimus/piccolo/internal/configdoc"
)

const userConfig = `{
  "version": 3,
  "agents": {
    "defaults": { "workspace": "~/.picoclaw/agents/main", "model_name": "gpt-5.4" },
    "list": [{ "id": "main", "default": true }]
  },
  "custom_key": { "kept": true },
  "model_list": [
    { "model_name": "gpt-5.4", "model": "openai/gpt-5.4", "api_keys": ["sk-early"] }
  ],
  "tools": { "web": { "enabled": true, "provider": "auto" }, "exec": { "enabled": false } }
}`

func parse(t *testing.T) *configdoc.Doc {
	t.Helper()
	d, err := configdoc.Parse([]byte(userConfig))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestRoundTripPreservesOrderAndUnknownKeys(t *testing.T) {
	d := parse(t)
	out := string(d.Marshal())
	var a, b any
	if err := json.Unmarshal([]byte(userConfig), &a); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(out), &b); err != nil {
		t.Fatal(err)
	}
	// Semantically identical.
	if !jsonEqual(a, b) {
		t.Fatalf("round trip changed content:\n%s", out)
	}
	// Key order preserved.
	for _, want := range []string{"version", "agents", "custom_key", "model_list", "tools"} {
		i := strings.Index(out, `"`+want+`"`)
		if i < 0 {
			t.Fatalf("key %s missing", want)
		}
	}
	if strings.Index(out, `"agents"`) > strings.Index(out, `"custom_key"`) {
		t.Error("custom_key must stay after agents")
	}
	// Unknown user key + untouched nested field survive.
	if !strings.Contains(out, `"kept": true`) {
		t.Error("custom_key.body lost")
	}
	if !strings.Contains(out, `"provider": "auto"`) {
		t.Error("tools.web.provider lost")
	}
}

func jsonEqual(a, b any) bool {
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	return string(ja) == string(jb)
}

func TestPatchChannelCreatesUpstreamShape(t *testing.T) {
	d := parse(t)
	ct, ok := Type("discord")
	if !ok {
		t.Fatal("discord type missing")
	}
	PatchChannel(d, ct, "discord", ChannelValues{
		AllowFrom:   []string{"1234567890"},
		MentionOnly: true,
		Fields:      map[string]string{"token": " bot-secret ", "proxy": ""},
	})
	out := string(d.Marshal())

	// Field order must match config.example.json.
	iEn := strings.Index(out, `"enabled"`)
	iType := strings.Index(out, `"type": "discord"`)
	iAllow := strings.Index(out, `"allow_from"`)
	iSet := strings.Index(out, `"settings"`)
	if !(iEn < iType && iType < iAllow && iAllow < iSet) {
		t.Errorf("field order wrong: %d %d %d %d", iEn, iType, iAllow, iSet)
	}
	for _, want := range []string{`"mention_only": true`, `"token": " bot-secret "`, `"1234567890"`} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s in %s", want, out)
		}
	}
	if strings.Contains(out, `"proxy": ""`) {
		t.Error("optional empty proxy should be skipped when new")
	}
	// Untouched parts preserved.
	if !strings.Contains(out, `"custom_key"`) {
		t.Error("user key lost")
	}
}

func TestPatchChannelKeepsUnknownSettings(t *testing.T) {
	d := parse(t)
	ct, _ := Type("discord")
	PatchChannel(d, ct, "discord", ChannelValues{Fields: map[string]string{"token": "one"}})
	// Simulate a user-added settings key + a future upstream field.
	cl, _ := d.GetObject("channel_list")
	entry, _ := cl.GetObject("discord")
	settings, _ := entry.GetObject("settings")
	settings.Set("user_extra", "keepme")
	entry.Set("brand_new_upstream_field", 42)

	PatchChannel(d, ct, "discord", ChannelValues{Fields: map[string]string{"token": "two"}})
	out := string(d.Marshal())
	if !strings.Contains(out, `"user_extra": "keepme"`) {
		t.Error("unknown settings key dropped")
	}
	if !strings.Contains(out, `"brand_new_upstream_field": 42`) {
		t.Error("unknown entry field dropped")
	}
	if !strings.Contains(out, `"token": "two"`) || strings.Contains(out, `"token": "one"`) {
		t.Error("token not updated")
	}
}

func TestChannelsListing(t *testing.T) {
	d := parse(t)
	ct, _ := Type("discord")
	PatchChannel(d, ct, "discord", ChannelValues{Fields: map[string]string{"token": "tok"}})
	// An unknown channel type the user added by hand.
	cl, _ := d.GetObject("channel_list")
	cl.Set("irc", Obj2(t, `{"enabled": true, "type": "irc", "settings": {}}`))

	list := Channels(d)
	if len(list) != 2 {
		t.Fatalf("len = %d", len(list))
	}
	if list[0].Name != "discord" || !list[0].Enabled || !list[0].TokenSet || list[0].Unknown {
		t.Errorf("discord = %+v", list[0])
	}
	if !list[1].Unknown || list[1].Type != "irc" {
		t.Errorf("irc = %+v", list[1])
	}
}

func TestSetChannelEnabled(t *testing.T) {
	d := parse(t)
	ct, _ := Type("telegram")
	PatchChannel(d, ct, "telegram", ChannelValues{Fields: map[string]string{"token": "t"}})
	SetChannelEnabled(d, "telegram", false)
	if chs := Channels(d); chs[0].Enabled {
		t.Error("toggle off failed")
	}
	SetChannelEnabled(d, "telegram", true)
	if chs := Channels(d); !chs[0].Enabled {
		t.Error("toggle on failed")
	}
}

func TestPatchModel(t *testing.T) {
	d := parse(t)
	PatchModel(d, "gpt-5.4", "openai/gpt-5.4", "sk-new", "")
	PatchModel(d, "llama", "ollama/llama3", "", "http://192.168.1.5:11434/v1")

	models := Models(d)
	if len(models) != 2 {
		t.Fatalf("models = %+v", models)
	}
	if models[0].Alias != "gpt-5.4" || models[0].APIBase != "" {
		t.Errorf("update: %+v", models[0])
	}
	if !models[0].KeySet {
		t.Error("key not set")
	}
	if models[1].Alias != "llama" || models[1].APIBase == "" {
		t.Errorf("add: %+v", models[1])
	}
	out := string(d.Marshal())
	if !strings.Contains(out, `"sk-new"`) {
		t.Error("api key not written")
	}
	// Existing entry position preserved (still before tools key etc).
	if strings.Index(out, `"gpt-5.4"`) > strings.Index(out, `"llama"`) {
		t.Error("entry order changed")
	}
}

func TestTools(t *testing.T) {
	d := parse(t)
	st := GetTools(d)
	if st.Web == nil || !*st.Web {
		t.Error("web should read true")
	}
	if st.Spawn != nil {
		t.Error("spawn absent should be nil")
	}
	SetTool(d, "exec", true)
	SetTool(d, "spawn", true)
	SetTool(d, "subagent", true)
	st = GetTools(d)
	if st.Exec == nil || !*st.Exec || st.Spawn == nil || !*st.Spawn {
		t.Errorf("after set: %+v", st)
	}
	out := string(d.Marshal())
	if !strings.Contains(out, `"provider": "auto"`) {
		t.Error("tools.web extra fields must survive toggle of exec")
	}
}

func Obj2(t *testing.T, src string) *configdoc.Doc {
	t.Helper()
	d, err := configdoc.Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	return d
}
