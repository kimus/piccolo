package setup

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/kimus/piccolo/internal/configdoc"
	"github.com/kimus/piccolo/internal/picoclaw"
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
	// Shield is what picoclaw/.security.yml has to say about this entry. A matched
	// shield makes KeySet a statement about a field the gateway will not read:
	// PicoClaw replaces config.json's api_keys with the sidecar's whenever the
	// sidecar carries a key for this model — in both directions, so an entry can
	// look keyed and run keyless, or look empty and run fine.
	Shield picoclaw.ModelSecret
}

// EffectiveKeySet reports whether the gateway will actually have a usable key for
// this model — the only version of "key set" worth showing a user.
func (m ModelInfo) EffectiveKeySet() bool {
	if m.Shield.Matched {
		return m.Shield.HasKeys
	}
	return m.KeySet
}

// Models lists model_list entries in document order. secrets is the workspace's
// picoclaw/.security.yml; pass the zero value when there is nothing to consult
// and KeySet keeps its old meaning.
func Models(doc *configdoc.Doc, secrets picoclaw.Secrets) []ModelInfo {
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
				if s, ok := kl[0].(string); ok && picoclaw.HasUsableKey(s) {
					info.KeySet = true
				}
			}
		}
		out = append(out, info)
	}
	// Resolve the sidecar over the alias list in document order, then pair it back
	// by index: PicoClaw's key format counts duplicate aliases, so the mapping has
	// to be positional rather than by name.
	shields := secrets.ResolveModels(aliasesOf(out))
	for i := range out {
		if i < len(shields) {
			out[i].Shield = shields[i]
		}
	}
	return out
}

func aliasesOf(models []ModelInfo) []string {
	out := make([]string, len(models))
	for i, m := range models {
		out[i] = m.Alias
	}
	return out
}

// ModelShield resolves the sidecar's say over one model_list entry by alias.
// Duplicated aliases share a name, and the sidecar counts them, so this returns
// the FIRST entry carrying that name — the one a menu offers to edit.
func ModelShield(doc *configdoc.Doc, secrets picoclaw.Secrets, alias string) picoclaw.ModelSecret {
	for _, m := range Models(doc, secrets) {
		if m.Alias == alias {
			return m.Shield
		}
	}
	return picoclaw.ModelSecret{}
}

// ShieldAfterWrite resolves the sidecar's say over the entry a write is about to
// produce, without touching the document. It matters for a rename: the sidecar
// counts occurrences of the name the entry will HAVE, so asking about the old
// alias asks the wrong question — a model renamed onto a name the sidecar already
// covers is shadowed the moment it lands, and a pre-write check on the old name
// would wave it through.
//
// lookup is the alias being edited ("" when adding). PatchModel keeps an edited
// entry in place and appends a new one, so the occurrence index is the number of
// entries already carrying finalAlias ahead of that position.
func ShieldAfterWrite(doc *configdoc.Doc, secrets picoclaw.Secrets, lookup, finalAlias string) picoclaw.ModelSecret {
	aliases := modelAliases(doc)
	pos := len(aliases) // an added entry lands at the end
	if lookup != "" {
		for i, a := range aliases {
			if a == lookup {
				pos = i
				break
			}
		}
	}
	occurrence := 0
	for i, a := range aliases {
		if i >= pos {
			break
		}
		if a == finalAlias {
			occurrence++
		}
	}
	return secrets.ResolveModel(finalAlias, occurrence)
}

// PatchModel adds or updates a model_list entry.
//
// lookup is the alias of the entry being EDITED; pass "" when adding. alias is
// the entry's alias AFTER the write, so editing the alias of an existing entry
// renames it in place (array position and unknown keys kept) instead of
// appending a duplicate and orphaning the entry every reference still points
// at. Renaming onto a different existing alias is refused: silently editing
// that other entry while leaving the original behind is never what an edit
// means.
//
// Provider and model are stored as separate fields (PicoClaw's preferred
// explicit form); a redundant "<provider>/" prefix on model is stripped so it
// is never double-prefixed. Empty optional values clear the corresponding field
// on update.
func PatchModel(doc *configdoc.Doc, lookup, alias, provider, model, apiKey, apiBase string) error {
	alias = strings.TrimSpace(alias)
	if alias == "" {
		return fmt.Errorf("model alias is required")
	}
	entry := configdoc.NewDoc()
	idx := -1
	if v, ok := doc.Get("model_list"); ok {
		arr, _ := v.([]configdoc.Value)
		want := lookup
		if want == "" {
			want = alias
		}
		for i, el := range arr {
			if m, ok := el.(*configdoc.Doc); ok {
				if a, _ := m.GetString("model_name"); a == want {
					entry = m
					idx = i
					break
				}
			}
		}
	}
	if lookup != "" && alias != lookup && modelExists(doc, alias) {
		return fmt.Errorf("a model named %q already exists — pick another alias", alias)
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
	return nil
}

func modelExists(doc *configdoc.Doc, alias string) bool {
	// Presence only — the sidecar has nothing to say about whether a name exists.
	for _, a := range modelAliases(doc) {
		if a == alias {
			return true
		}
	}
	return false
}

// modelAliases is config.json's model_name list in document order. Used where
// only the names matter, so no caller has to invent a Secrets value.
func modelAliases(doc *configdoc.Doc) []string {
	v, ok := doc.Get("model_list")
	if !ok {
		return nil
	}
	arr, ok := v.([]configdoc.Value)
	if !ok {
		return nil
	}
	var out []string
	for _, el := range arr {
		if m, ok := el.(*configdoc.Doc); ok {
			a, _ := m.GetString("model_name")
			out = append(out, a)
		}
	}
	return out
}

// modelRef is one place config.json names a model alias. owner/key/index locate
// it in the document so a rename writes it back in place, keeping every other
// key and the document order.
type modelRef struct {
	Path   string // human-readable location, e.g. agents.list[coder].model
	Alias  string
	InList bool // element of a fallback list: droppable, not load-bearing
	owner  *configdoc.Doc
	key    string
	index  int // -1 for a scalar field
}

// Set replaces the alias this reference points at.
func (r *modelRef) Set(alias string) {
	if r.index < 0 {
		r.owner.Set(r.key, alias)
		return
	}
	v, ok := r.owner.Get(r.key)
	list, ok2 := v.([]configdoc.Value)
	if !ok || !ok2 || r.index >= len(list) {
		return
	}
	list[r.index] = alias
	r.owner.Set(r.key, list)
}

// Drop removes this element from its fallback list, deleting the key once the
// list is empty: upstream omits an empty fallbacks field.
func (r *modelRef) Drop() {
	v, ok := r.owner.Get(r.key)
	list, ok2 := v.([]configdoc.Value)
	if !ok || !ok2 || r.index >= len(list) {
		return
	}
	out := append(append([]configdoc.Value{}, list[:r.index]...), list[r.index+1:]...)
	if len(out) == 0 {
		r.owner.Delete(r.key)
		return
	}
	r.owner.Set(r.key, out)
}

// modelRefs walks every reference site PicoClaw resolves against model_list
// (verified v0.3.1 pkg/config/config.go:428-431 and pkg/agent/instance.go:380-392),
// in document order. An AGENT.md frontmatter `model:` pin also names a model,
// but that file is user-owned workspace content: callers report those instead
// of rewriting them.
func modelRefs(doc *configdoc.Doc) []*modelRef {
	agents, ok := doc.GetObject("agents")
	if !ok {
		return nil
	}
	var refs []*modelRef
	if defaults := lookupAgentsDefaults(doc); defaults != nil {
		for _, key := range []string{"model_name", "image_model"} {
			if v, ok := defaults.GetString(key); ok {
				refs = append(refs, &modelRef{Path: "agents.defaults." + key, Alias: v, owner: defaults, key: key, index: -1})
			}
		}
		refs = append(refs, fallbackRefs(defaults, "model_fallbacks", "agents.defaults.model_fallbacks")...)
		refs = append(refs, fallbackRefs(defaults, "image_model_fallbacks", "agents.defaults.image_model_fallbacks")...)
	}
	listRaw, ok := agents.Get("list")
	if !ok {
		return refs
	}
	list, ok := listRaw.([]configdoc.Value)
	if !ok {
		return refs
	}
	for _, item := range list {
		ag, ok := item.(*configdoc.Doc)
		if !ok {
			continue
		}
		id, _ := ag.GetString("id")
		if id == "" {
			id = "?"
		}
		label := "agents.list[" + id + "]"
		refs = append(refs, modelFieldRefs(ag, label+".model")...)
		if sub, ok := ag.GetObject("subagents"); ok {
			refs = append(refs, modelFieldRefs(sub, label+".subagents.model")...)
		}
	}
	return refs
}

// fallbackRefs names each element of a *_fallbacks array on owner.
func fallbackRefs(owner *configdoc.Doc, key, path string) []*modelRef {
	v, ok := owner.Get(key)
	if !ok {
		return nil
	}
	list, ok := v.([]configdoc.Value)
	if !ok {
		return nil
	}
	var out []*modelRef
	for i, el := range list {
		s, ok := el.(string)
		if !ok {
			continue
		}
		out = append(out, &modelRef{Path: fmt.Sprintf("%s[%d]", path, i), Alias: s, InList: true, owner: owner, key: key, index: i})
	}
	return out
}

// modelFieldRefs reads one "model" field, which PicoClaw accepts as either a
// bare alias string or {"primary": ..., "fallbacks": [...]} (verified
// config.go:283-311).
func modelFieldRefs(owner *configdoc.Doc, path string) []*modelRef {
	v, ok := owner.Get("model")
	if !ok {
		return nil
	}
	switch t := v.(type) {
	case string:
		return []*modelRef{{Path: path, Alias: t, owner: owner, key: "model", index: -1}}
	case *configdoc.Doc:
		var out []*modelRef
		if s, ok := t.GetString("primary"); ok {
			// The primary is named by the field path, exactly as the bare-string
			// form is, so one rule reads the same wherever a model is named.
			out = append(out, &modelRef{Path: path, Alias: s, owner: t, key: "primary", index: -1})
		}
		out = append(out, fallbackRefs(t, "fallbacks", path+".fallbacks")...)
		return out
	}
	return nil
}

// ModelRefPaths lists where an alias is referenced, in document order. Empty
// means nothing in config.json still points at it.
func ModelRefPaths(doc *configdoc.Doc, alias string) []string {
	var out []string
	for _, r := range modelRefs(doc) {
		if r.Alias == alias {
			out = append(out, r.Path)
		}
	}
	return out
}

// RenameModelRefs rewrites every reference from oldAlias to newAlias and returns
// the paths it touched. It deliberately skips workspace AGENT.md files, which
// callers report instead of editing.
func RenameModelRefs(doc *configdoc.Doc, oldAlias, newAlias string) []string {
	oldAlias, newAlias = strings.TrimSpace(oldAlias), strings.TrimSpace(newAlias)
	if oldAlias == "" || newAlias == "" || oldAlias == newAlias {
		return nil
	}
	var touched []string
	for _, r := range modelRefs(doc) {
		if r.Alias != oldAlias {
			continue
		}
		r.Set(newAlias)
		touched = append(touched, r.Path)
	}
	return touched
}

// RemoveModel deletes one model_list entry. It refuses while the alias is still
// a load-bearing reference — a default, an image model, or an agent's own model
// — because PicoClaw would then resolve an alias that no longer exists, and
// doctor FAILs on exactly that; the error names the paths to change first.
// Fallback entries are dropped with the model: a failover list keeps working
// with one fewer candidate.
func RemoveModel(doc *configdoc.Doc, alias string) error {
	alias = strings.TrimSpace(alias)
	if alias == "" {
		return fmt.Errorf("model alias is required")
	}
	v, ok := doc.Get("model_list")
	if !ok {
		return fmt.Errorf("config.json has no model_list")
	}
	arr, ok := v.([]configdoc.Value)
	if !ok {
		return fmt.Errorf("model_list is not an array")
	}
	idx := -1
	for i, el := range arr {
		if m, ok := el.(*configdoc.Doc); ok {
			if a, _ := m.GetString("model_name"); a == alias {
				idx = i
				break
			}
		}
	}
	if idx < 0 {
		return fmt.Errorf("model %q is not in model_list", alias)
	}
	var blocking []string
	var drops []*modelRef
	for _, r := range modelRefs(doc) {
		if r.Alias != alias {
			continue
		}
		if r.InList {
			drops = append(drops, r)
			continue
		}
		blocking = append(blocking, r.Path)
	}
	if len(blocking) > 0 {
		return fmt.Errorf("%s is still referenced by %s", alias, strings.Join(blocking, ", "))
	}
	// Reverse order keeps the remaining element indices valid: one Drop only
	// shifts the elements after the index it removes.
	for i := len(drops) - 1; i >= 0; i-- {
		drops[i].Drop()
	}
	out := append(append([]configdoc.Value{}, arr[:idx]...), arr[idx+1:]...)
	doc.Set("model_list", out)
	return nil
}

// ToolsState is the enabled/unknown state of the piccolo tool toggles.
type ToolsState struct {
	Web, Exec, Spawn, Subagent, LoadImage, MCP *bool
	ExecAllowRemote                            *bool
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
		MCP:             toolToggle(doc, "mcp"),
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

// lookupAgentsDefaults returns agents.defaults without creating anything.
func lookupAgentsDefaults(doc *configdoc.Doc) *configdoc.Doc {
	agents, ok := doc.GetObject("agents")
	if !ok {
		return nil
	}
	d, _ := agents.GetObject("defaults")
	return d
}

// ensureAgentsDefaults returns agents.defaults, creating it and its parent.
func ensureAgentsDefaults(doc *configdoc.Doc) *configdoc.Doc {
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
	return defaults
}

// DefaultModel returns agents.defaults.model_name: the alias every agent
// inherits unless its AGENT.md frontmatter or its agents.list entry pins a model
// of its own (precedence verified v0.3.1 pkg/agent/instance.go:380-392).
func DefaultModel(doc *configdoc.Doc) string {
	defaults := lookupAgentsDefaults(doc)
	if defaults == nil {
		return ""
	}
	model, _ := defaults.GetString("model_name")
	return model
}

// SetDefaultModel writes agents.defaults.model_name, creating parents.
func SetDefaultModel(doc *configdoc.Doc, alias string) {
	ensureAgentsDefaults(doc).Set("model_name", alias)
}

// DefaultImageModel returns agents.defaults.image_model.
func DefaultImageModel(doc *configdoc.Doc) string {
	defaults := lookupAgentsDefaults(doc)
	if defaults == nil {
		return ""
	}
	model, _ := defaults.GetString("image_model")
	return model
}

// SetDefaultImageModel writes agents.defaults.image_model, creating parents.
func SetDefaultImageModel(doc *configdoc.Doc, alias string) {
	ensureAgentsDefaults(doc).Set("image_model", alias)
}

// WorkspaceRestricted returns agents.defaults.restrict_to_workspace. A nil
// result means the key is absent, which PicoClaw reads as TRUE
// (pkg/config/defaults.go:29) — callers must not treat absent as unrestricted.
//
// The flag is the whole exec path guard. Verified v0.3.1
// pkg/agent/instance.go:85-86,113:
//
//	restrict     := defaults.RestrictToWorkspace
//	readRestrict := restrict && !defaults.AllowReadOutsideWorkspace
//	tools.NewExecToolWithConfig(workspace, restrict, cfg, allowReadPaths)
//
// exec receives `restrict`, not `readRestrict`, so allowing reads outside the
// workspace does NOT stop a shell command that names an absolute path (a tool on
// PATH, /proc, another agent's workspace) from failing with "Command blocked by
// safety guard (path outside working dir)" — pkg/tools/shell.go:1172,1278 only
// exempts /dev pseudo-devices and tools.allow_read_paths.
func WorkspaceRestricted(doc *configdoc.Doc) *bool {
	defaults := lookupAgentsDefaults(doc)
	if defaults == nil {
		return nil
	}
	b, ok := defaults.GetBool("restrict_to_workspace")
	if !ok {
		return nil
	}
	return &b
}

// SetWorkspaceRestricted writes agents.defaults.restrict_to_workspace, creating
// parents. Piccolo generates false: the container is the security boundary, and a
// guard that trips on ordinary commands produces errors the agent cannot read.
func SetWorkspaceRestricted(doc *configdoc.Doc, restricted bool) {
	ensureAgentsDefaults(doc).Set("restrict_to_workspace", restricted)
}

// ToolNames is the editable toggle set exposed by piccolo setup.
var ToolNames = []struct{ Key, Label, Help string }{
	{"web", "Web search tools", "web_search / web_fetch"},
	{"exec", "Shell execution", "runs commands in the container — default OFF"},
	{"spawn", "Spawn sub-runners", "background task spawning"},
	{"subagent", "Subagent delegation", "main agent delegating to specialists"},
	{"load_image", "Image understanding", "load_image lets agents visually inspect local image attachments with a vision model — recommended ON"},
	{"mcp", "MCP tool servers", "the tool an integration's servers.<key> entry needs; OFF makes every installed integration invisible to agents"},
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
	for _, m := range Models(doc, picoclaw.Secrets{}) {
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

// --- MCP server entries -------------------------------------------------------

// mcpServers returns the tools.mcp.servers object, or nil when there is none.
func mcpServers(doc *configdoc.Doc) *configdoc.Doc {
	tools, ok := doc.GetObject("tools")
	if !ok {
		return nil
	}
	mcp, ok := tools.GetObject("mcp")
	if !ok {
		return nil
	}
	servers, _ := mcp.GetObject("servers")
	return servers
}

// MCPServerKeys lists the registered server keys in document order.
func MCPServerKeys(doc *configdoc.Doc) []string {
	servers := mcpServers(doc)
	if servers == nil {
		return nil
	}
	return servers.Keys()
}

// HasMCPServer reports whether config.json registers this server key.
func HasMCPServer(doc *configdoc.Doc, key string) bool {
	servers := mcpServers(doc)
	if servers == nil {
		return false
	}
	_, ok := servers.Get(key)
	return ok
}

// RemoveMCPServer deletes one registered server entry. It reports whether anything
// changed and never creates the objects it walks through.
//
// This is the inverse of what `piccolo integration setup` writes from a configPatch,
// and it is load-bearing for uninstall: a servers.<key> entry whose integration is
// gone is not merely inert. With tools.mcp.enabled true, PicoClaw tries to start
// every listed server, and a server whose command no longer exists in the image
// FAILS THE WHOLE TURN — every chat message errors, not just the removed
// integration's. Removing the manifest record without removing this entry turns an
// uninstall into a broken gateway, which is why the command cannot be a manifest edit
// alone.
func RemoveMCPServer(doc *configdoc.Doc, key string) (bool, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return false, fmt.Errorf("mcp server key is required")
	}
	servers := mcpServers(doc)
	if servers == nil {
		return false, nil
	}
	if _, ok := servers.Get(key); !ok {
		return false, nil
	}
	servers.Delete(key)
	return true, nil
}
