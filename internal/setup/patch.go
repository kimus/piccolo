package setup

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/kimus/piccolo/internal/configdoc"
)

// ChannelInfo is the summarized state of one channel_list entry.
type ChannelInfo struct {
	Name     string
	Type     string
	Enabled  bool
	TokenSet bool // best-effort: any settings key holding a non-empty string secret
	Unknown  bool // type not recognized by piccolo's registry
	Mentions bool // group_trigger.mention_only
}

// Channels lists channel_list entries in document order.
func Channels(doc *configdoc.Doc) []ChannelInfo {
	cl, ok := doc.GetObject("channel_list")
	if !ok {
		return nil
	}
	var out []ChannelInfo
	for _, name := range cl.Keys() {
		entry, _ := cl.GetObject(name)
		if entry == nil {
			continue
		}
		info := ChannelInfo{Name: name}
		info.Type, _ = entry.GetString("type")
		if info.Type == "" {
			info.Type = name
		}
		info.Enabled, _ = entry.GetBool("enabled")
		if settings, ok := entry.GetObject("settings"); ok {
			for _, k := range settings.Keys() {
				if v, ok := settings.GetString(k); ok && v != "" && isSecretKey(k) {
					info.TokenSet = true
				}
			}
		}
		if gt, ok := entry.GetObject("group_trigger"); ok {
			info.Mentions, _ = gt.GetBool("mention_only")
		}
		if _, known := Type(info.Type); !known {
			info.Unknown = true
		}
		out = append(out, info)
	}
	return out
}

func isSecretKey(k string) bool {
	switch k {
	case "token", "bot_token", "app_token", "app_secret", "client_secret", "sign_secret", "verification_token":
		return true
	}
	return false
}

// ChannelValues are the wizard answers for building/patching one channel.
type ChannelValues struct {
	AllowFrom   []string
	MentionOnly bool
	Fields      map[string]string // FieldSpec.Key -> value
}

// PatchChannel merge-patches one channel_list entry. Existing entries keep
// unknown settings keys; new entries follow the config.example.json shape
// (verified picoclaw v0.3.1).
func PatchChannel(doc *configdoc.Doc, t ChannelType, name string, v ChannelValues) {
	cl, ok := doc.GetObject("channel_list")
	if !ok {
		cl = configdoc.NewDoc()
		doc.Set("channel_list", cl)
	}
	entry, ok := cl.GetObject(name)
	if !ok {
		// Order matches upstream config.example.json: enabled, type,
		// allow_from, group_trigger, reasoning_channel_id, settings.
		entry = configdoc.Obj(
			"enabled", true,
			"type", t.Name,
			"allow_from", configdoc.StrList(v.AllowFrom...),
		)
		if t.MentionOnly {
			entry.Set("group_trigger", configdoc.Obj("mention_only", v.MentionOnly))
		}
		entry.Set("reasoning_channel_id", "")
		entry.Set("settings", configdoc.NewDoc())
	}
	if allow := v.AllowFrom; allow != nil {
		entry.Set("allow_from", configdoc.StrList(allow...))
	}
	if t.MentionOnly {
		if gt, ok := entry.GetObject("group_trigger"); ok {
			gt.Set("mention_only", v.MentionOnly)
		} else {
			entry.Set("group_trigger", configdoc.Obj("mention_only", v.MentionOnly))
		}
	}
	settings, ok := entry.GetObject("settings")
	if !ok {
		settings = configdoc.NewDoc()
		entry.Set("settings", settings)
	}
	for _, f := range t.Fields {
		val, given := v.Fields[f.Key]
		if !given {
			continue
		}
		if val == "" {
			if f.Optional {
				settings.Delete(f.Key) // explicit clear
			} else {
				settings.Set(f.Key, "")
			}
			continue
		}
		settings.Set(f.Key, val)
	}
	cl.Set(name, entry)
}

// SetChannelEnabled toggles the enabled flag on an existing entry.
func SetChannelEnabled(doc *configdoc.Doc, name string, enabled bool) {
	if cl, ok := doc.GetObject("channel_list"); ok {
		if entry, ok := cl.GetObject(name); ok {
			entry.Set("enabled", enabled)
		}
	}
}

// ModelInfo summarizes one model_list entry for menus.
type ModelInfo struct {
	Alias    string
	Provider string
	Model    string
	KeySet   bool
	APIBase  string
	Index    int
}

// Models lists model_list entries in document order.
func Models(doc *configdoc.Doc) []ModelInfo {
	v, ok := doc.Get("model_list")
	if !ok {
		return nil
	}
	arr, ok := v.([]configdoc.Value)
	if !ok {
		return nil
	}
	var out []ModelInfo
	for i, el := range arr {
		m, ok := el.(*configdoc.Doc)
		if !ok {
			continue
		}
		info := ModelInfo{Index: i}
		info.Alias, _ = m.GetString("model_name")
		info.Provider, _ = m.GetString("provider")
		info.Model, _ = m.GetString("model")
		info.APIBase, _ = m.GetString("api_base")
		if keys, ok := m.Get("api_keys"); ok {
			if kl, ok := keys.([]configdoc.Value); ok && len(kl) > 0 {
				if s, ok := kl[0].(string); ok && s != "" {
					info.KeySet = true
				}
			}
		}
		out = append(out, info)
	}
	return out
}

// PatchModel adds or updates a model_list entry (matched by alias). Provider
// and model are stored as separate fields (PicoClaw's preferred explicit
// form); a redundant "<provider>/" prefix on model is stripped so it is never
// double-prefixed. Empty optional values clear the corresponding field on
// update.
func PatchModel(doc *configdoc.Doc, alias, provider, model, apiKey, apiBase string) {
	entry := configdoc.NewDoc()
	idx := -1
	if v, ok := doc.Get("model_list"); ok {
		arr, _ := v.([]configdoc.Value)
		for i, el := range arr {
			if m, ok := el.(*configdoc.Doc); ok {
				if a, _ := m.GetString("model_name"); a == alias {
					entry = m
					idx = i
					break
				}
			}
		}
	}
	entry.Set("model_name", alias)
	provider = strings.TrimSpace(provider)
	model = strings.TrimSpace(model)
	if provider != "" {
		model = strings.TrimSpace(strings.TrimPrefix(model, provider+"/"))
		entry.Set("provider", provider)
	} else {
		entry.Delete("provider")
	}
	if model != "" {
		entry.Set("model", model)
	}
	if _, ok := entry.Get("api_keys"); !ok {
		entry.Set("api_keys", configdoc.StrList())
	}
	entry.Set("api_keys", configdoc.StrList(apiKey))
	if apiBase == "" {
		entry.Delete("api_base")
	} else {
		entry.Set("api_base", apiBase)
	}
	if idx < 0 {
		arr, _ := doc.Get("model_list")
		list, ok := arr.([]configdoc.Value)
		if !ok {
			list = []configdoc.Value{}
		}
		doc.Set("model_list", append(list, entry))
	}
}

// ToolsState is the enabled/unknown state of the piccolo tool toggles.
type ToolsState struct {
	Web, Exec, Spawn, Subagent, LoadImage *bool
	ExecAllowRemote                       *bool
}

func toolToggle(doc *configdoc.Doc, name string) *bool {
	tools, ok := doc.GetObject("tools")
	if !ok {
		return nil
	}
	tg, ok := tools.GetObject(name)
	if !ok {
		return nil
	}
	b, ok := tg.GetBool("enabled")
	if !ok {
		return nil
	}
	return &b
}

// GetTools reads the tool toggles (nil = absent/unknown).
func GetTools(doc *configdoc.Doc) ToolsState {
	return ToolsState{
		Web:             toolToggle(doc, "web"),
		Exec:            toolToggle(doc, "exec"),
		Spawn:           toolToggle(doc, "spawn"),
		Subagent:        toolToggle(doc, "subagent"),
		LoadImage:       toolToggle(doc, "load_image"),
		ExecAllowRemote: execAllowRemote(doc),
	}
}

func execAllowRemote(doc *configdoc.Doc) *bool {
	tools, ok := doc.GetObject("tools")
	if !ok {
		return nil
	}
	exec, ok := tools.GetObject("exec")
	if !ok {
		return nil
	}
	b, ok := exec.GetBool("allow_remote")
	if !ok {
		return nil
	}
	return &b
}

// SetTool writes one toggle, creating tools.<name>.enabled if missing.
// Other keys inside the toggle object are preserved.
func SetTool(doc *configdoc.Doc, name string, enabled bool) {
	tools, ok := doc.GetObject("tools")
	if !ok {
		tools = configdoc.NewDoc()
		doc.Set("tools", tools)
	}
	tg, ok := tools.GetObject(name)
	if !ok {
		tg = configdoc.NewDoc()
		tools.Set(name, tg)
	}
	tg.Set("enabled", enabled)
}

// SetExecAllowRemote writes tools.exec.allow_remote, creating parents. It does
// not imply tools.exec.enabled; callers that enable remote exec should also
// enable the exec tool explicitly.
func EnsureMediaReadConfig(doc *configdoc.Doc) bool {
	changed := ensureAllowReadOutsideWorkspace(doc)
	if EnsureToolFeedback(doc) {
		changed = true
	}
	return changed
}

func ensureAllowReadOutsideWorkspace(doc *configdoc.Doc) bool {
	agents, ok := doc.GetObject("agents")
	if !ok {
		agents = configdoc.NewDoc()
		doc.Set("agents", agents)
	}
	defaults, ok := agents.GetObject("defaults")
	if !ok {
		defaults = configdoc.NewDoc()
		agents.Set("defaults", defaults)
	}
	if v, ok := defaults.GetBool("allow_read_outside_workspace"); ok && v {
		return false
	}
	defaults.Set("allow_read_outside_workspace", true)
	return true
}

func jsonNumber(n int) json.Number {
	return json.Number(fmt.Sprint(n))
}

func ToolFeedbackEnabled(doc *configdoc.Doc) *bool {
	agents, ok := doc.GetObject("agents")
	if !ok {
		return nil
	}
	defaults, ok := agents.GetObject("defaults")
	if !ok {
		return nil
	}
	feedback, ok := defaults.GetObject("tool_feedback")
	if !ok {
		return nil
	}
	b, ok := feedback.GetBool("enabled")
	if !ok {
		return nil
	}
	return &b
}

func SetToolFeedback(doc *configdoc.Doc, enabled bool) {
	agents, ok := doc.GetObject("agents")
	if !ok {
		agents = configdoc.NewDoc()
		doc.Set("agents", agents)
	}
	defaults, ok := agents.GetObject("defaults")
	if !ok {
		defaults = configdoc.NewDoc()
		agents.Set("defaults", defaults)
	}
	feedback, ok := defaults.GetObject("tool_feedback")
	if !ok {
		feedback = configdoc.NewDoc()
		defaults.Set("tool_feedback", feedback)
	}
	feedback.Set("enabled", enabled)
	if _, ok := feedback.Get("max_args_length"); !ok {
		feedback.Set("max_args_length", jsonNumber(300))
	}
}

func EnsureToolFeedback(doc *configdoc.Doc) bool {
	agents, ok := doc.GetObject("agents")
	if !ok {
		agents = configdoc.NewDoc()
		doc.Set("agents", agents)
	}
	defaults, ok := agents.GetObject("defaults")
	if !ok {
		defaults = configdoc.NewDoc()
		agents.Set("defaults", defaults)
	}
	feedback, ok := defaults.GetObject("tool_feedback")
	if !ok {
		feedback = configdoc.NewDoc()
		defaults.Set("tool_feedback", feedback)
	}
	changed := false
	if v, _ := feedback.GetBool("enabled"); !v {
		feedback.Set("enabled", true)
		changed = true
	}
	if v, ok := feedback.Get("max_args_length"); !ok {
		feedback.Set("max_args_length", jsonNumber(300))
		changed = true
	} else if n, ok := v.(json.Number); ok && n.String() != "300" {
		feedback.Set("max_args_length", jsonNumber(300))
		changed = true
	}
	return changed
}

func SetExecAllowRemote(doc *configdoc.Doc, enabled bool) {
	tools, ok := doc.GetObject("tools")
	if !ok {
		tools = configdoc.NewDoc()
		doc.Set("tools", tools)
	}
	exec, ok := tools.GetObject("exec")
	if !ok {
		exec = configdoc.NewDoc()
		tools.Set("exec", exec)
	}
	exec.Set("allow_remote", enabled)
}

// DefaultImageModel returns agents.defaults.image_model.
func DefaultImageModel(doc *configdoc.Doc) string {
	agents, ok := doc.GetObject("agents")
	if !ok {
		return ""
	}
	defaults, ok := agents.GetObject("defaults")
	if !ok {
		return ""
	}
	model, _ := defaults.GetString("image_model")
	return model
}

// SetDefaultImageModel writes agents.defaults.image_model, creating parents.
func SetDefaultImageModel(doc *configdoc.Doc, alias string) {
	agents, ok := doc.GetObject("agents")
	if !ok {
		agents = configdoc.NewDoc()
		doc.Set("agents", agents)
	}
	defaults, ok := agents.GetObject("defaults")
	if !ok {
		defaults = configdoc.NewDoc()
		agents.Set("defaults", defaults)
	}
	defaults.Set("image_model", alias)
}

// ToolNames is the editable toggle set exposed by piccolo setup.
var ToolNames = []struct{ Key, Label, Help string }{
	{"web", "Web search tools", "web_search / web_fetch"},
	{"exec", "Shell execution", "runs commands in the container — default OFF"},
	{"spawn", "Spawn sub-runners", "background task spawning"},
	{"subagent", "Subagent delegation", "main agent delegating to specialists"},
	{"load_image", "Image understanding", "load_image lets agents visually inspect local image attachments with a vision model — recommended ON"},
}

// ReadChannel extracts current values for pre-filling an edit form.
// Returns ok=false when the channel does not exist.
func ReadChannel(doc *configdoc.Doc, name string) (ChannelValues, bool) {
	cl, ok := doc.GetObject("channel_list")
	if !ok {
		return ChannelValues{}, false
	}
	entry, ok := cl.GetObject(name)
	if !ok {
		return ChannelValues{}, false
	}
	v := ChannelValues{Fields: map[string]string{}}
	if allow, ok := entry.GetStringList("allow_from"); ok {
		v.AllowFrom = allow
	}
	if gt, ok := entry.GetObject("group_trigger"); ok {
		v.MentionOnly, _ = gt.GetBool("mention_only")
	}
	if settings, ok := entry.GetObject("settings"); ok {
		for _, k := range settings.Keys() {
			if s, ok := settings.GetString(k); ok {
				v.Fields[k] = s
			}
		}
	}
	return v, true
}

// ReadModel extracts a model_list entry by alias for pre-fill.
func ReadModel(doc *configdoc.Doc, alias string) (provider, model, apiKey, apiBase string, ok bool) {
	for _, m := range Models(doc) {
		if m.Alias != alias {
			continue
		}
		v, _ := doc.Get("model_list")
		arr, _ := v.([]configdoc.Value)
		entry, _ := arr[m.Index].(*configdoc.Doc)
		provider, _ = entry.GetString("provider")
		model, _ = entry.GetString("model")
		apiBase, _ = entry.GetString("api_base")
		if keys, ok := entry.Get("api_keys"); ok {
			if kl, ok := keys.([]configdoc.Value); ok && len(kl) > 0 {
				apiKey, _ = kl[0].(string)
			}
		}
		return provider, model, apiKey, apiBase, true
	}
	return "", "", "", "", false
}
