package setup

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/kimus/piccolo/internal/configdoc"
)

// Integration config patches are validated against a FIXED, code-defined
// allowlist. An integration proposes; only Piccolo writes config.json, and an
// integration can never widen what it may touch. Anything outside these paths
// is refused, so a malicious or buggy integration cannot rewrite agents,
// model keys, or tool toggles in a file Piccolo treats as user-owned.

// MCPServerPath returns the one configuration path an integration may propose
// changing: tools.mcp.servers.<serverKey>.
func MCPServerPath(serverKey string) []string {
	return []string{"tools", "mcp", "servers", serverKey}
}

// AllowedIntegrationPatchPaths reports the permitted prefix for an integration
// patch. Only the integration's own MCP server entry may be written.
func AllowedIntegrationPatchPath(serverKey string) string {
	return strings.Join(MCPServerPath(serverKey), ".")
}

// ValidateIntegrationPatch checks a proposed patch document. It returns the
// rejected paths (sorted) when the patch reaches outside the allowlist.
func ValidateIntegrationPatch(raw json.RawMessage, serverKey string) error {
	if len(raw) == 0 {
		return nil
	}
	allowed := MCPServerPath(serverKey)
	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil {
		return fmt.Errorf("integration config patch is not a JSON object: %w", err)
	}
	if len(root) == 0 {
		return fmt.Errorf("integration config patch is empty")
	}
	var rejected []string
	var walk func(prefix []string, obj map[string]json.RawMessage)
	walk = func(prefix []string, obj map[string]json.RawMessage) {
		for key, child := range obj {
			path := append(append([]string(nil), prefix...), key)
			if pathWithin(path, allowed) {
				continue // inside the allowlist, nothing below it is ours to police
			}
			var nested map[string]json.RawMessage
			if err := json.Unmarshal(child, &nested); err == nil {
				// An intermediate container on the allowed prefix carries no data
				// of its own (possibly empty), so descend rather than reject.
				if pathIsPrefixOf(path, allowed) {
					walk(path, nested)
					continue
				}
				if len(nested) > 0 {
					walk(path, nested)
					continue
				}
			}
			rejected = append(rejected, strings.Join(path, "."))
		}
	}
	walk(nil, root)
	if len(rejected) > 0 {
		sort.Strings(rejected)
		return fmt.Errorf("integration config patch touches %d path(s) outside the allowed %s: %s",
			len(rejected), AllowedIntegrationPatchPath(serverKey), strings.Join(rejected, ", "))
	}
	return nil
}

// ApplyIntegrationPatch merges an already-validated patch into doc under
// tools.mcp.servers.<serverKey>, preserving all other keys and their order.
// It reports the server entry that was written.
func ApplyIntegrationPatch(doc *configdoc.Doc, raw json.RawMessage, serverKey string) (string, error) {
	if doc == nil {
		return "", fmt.Errorf("config document is nil")
	}
	if len(raw) == 0 {
		return "", nil
	}
	if err := ValidateIntegrationPatch(raw, serverKey); err != nil {
		return "", err
	}
	entry := extractAtPath(raw, MCPServerPath(serverKey))
	if len(entry) == 0 {
		// Patch was allowlisted but carried no server entry: nothing to do.
		return "", nil
	}
	var entryObj map[string]json.RawMessage
	if err := json.Unmarshal(entry, &entryObj); err != nil {
		return "", fmt.Errorf("mcp server entry %q is not a JSON object: %w", serverKey, err)
	}
	sub, err := configdoc.Parse(entry)
	if err != nil {
		return "", fmt.Errorf("parse mcp server entry: %w", err)
	}

	tools := ensureChildDoc(doc, "tools")
	mcp := ensureChildDoc(tools, "mcp")
	servers := ensureChildDoc(mcp, "servers")
	// Set preserves position on update, so re-registering a server does not
	// reorder the user's other servers.
	servers.Set(serverKey, sub)
	return serverKey, nil
}

// MCPServerArgv returns the command and arguments PicoClaw would run for one
// registered server, read from tools.mcp.servers.<key>. Piccolo executes that argv to
// test the handshake before enabling the MCP tool.
//
// Empty means there is nothing to probe and the caller must NOT treat that as a
// pass: the entry is missing, individually disabled, declares no command (an
// sse/streamable-http server, whose transport Piccolo cannot spawn), or is shaped
// in a way Piccolo does not model. Unverifiable stays unverifiable.
func MCPServerArgv(doc *configdoc.Doc, serverKey string) []string {
	tools, ok := doc.GetObject("tools")
	if !ok {
		return nil
	}
	mcp, ok := tools.GetObject("mcp")
	if !ok {
		return nil
	}
	servers, ok := mcp.GetObject("servers")
	if !ok {
		return nil
	}
	entry, ok := servers.GetObject(serverKey)
	if !ok {
		return nil
	}
	if on, ok := entry.GetBool("enabled"); ok && !on {
		return nil
	}
	command, _ := entry.GetString("command")
	command = strings.TrimSpace(command)
	if command == "" {
		return nil
	}
	argv := []string{command}
	if v, ok := entry.Get("args"); ok {
		if list, ok := v.([]configdoc.Value); ok {
			for _, el := range list {
				if s, ok := el.(string); ok {
					argv = append(argv, s)
				}
			}
		}
	}
	return argv
}

// MCPToolEnabled reads tools.mcp.enabled. A nil result means the key is absent,
// which PicoClaw treats as FALSE (pkg/config/defaults.go:428-431) — callers must
// not read nil as "already on".
func MCPToolEnabled(doc *configdoc.Doc) *bool {
	tools, ok := doc.GetObject("tools")
	if !ok {
		return nil
	}
	mcp, ok := tools.GetObject("mcp")
	if !ok {
		return nil
	}
	b, ok := mcp.GetBool("enabled")
	if !ok {
		return nil
	}
	return &b
}

// EnableMCPTool writes tools.mcp.enabled: true and reports whether it changed
// anything. It never turns the tool off — that stays the user's decision in
// 'piccolo setup' \u2192 Tools.
//
// Enabling is deliberately a separate step from registering the server, so a caller
// can gate it on a handshake. Two verified facts make that pairing necessary rather
// than cosmetic:
//
//   - While the toggle is false, PicoClaw skips MCP initialization entirely
//     (pkg/agent/agent_mcp.go:79 reads IsToolEnabled("mcp") and returns before it
//     looks at servers), and it defaults to false — so a registered server is inert,
//     and the integration cannot fix that itself, because the allowlist above
//     confines it to its own server entry precisely so it cannot flip tool toggles.
//   - With the toggle true and a server that cannot complete the handshake, PicoClaw
//     fails the WHOLE turn: every chat message errors, not just the ones asking about
//     that integration. Observed live with a server that answers in LSP-style
//     Content-Length framing, which the MCP Go SDK (newline-delimited JSON) rejects.
//
// So registration and enablement are different promises, and only the verified one
// gets written.
func EnableMCPTool(doc *configdoc.Doc) bool {
	tools := ensureChildDoc(doc, "tools")
	mcp := ensureChildDoc(tools, "mcp")
	if on, ok := mcp.GetBool("enabled"); ok && on {
		return false
	}
	mcp.Set("enabled", true)
	return true
}

// ensureChildDoc returns the named child object, creating and attaching it when
// absent, without disturbing the parent's other keys.
func ensureChildDoc(doc *configdoc.Doc, key string) *configdoc.Doc {
	child, ok := doc.GetObject(key)
	if !ok {
		child = configdoc.NewDoc()
		doc.Set(key, child)
	}
	return child
}

// pathWithin reports whether candidate is at or below the allowed prefix.
func pathWithin(candidate, allowed []string) bool {
	if len(candidate) < len(allowed) {
		return false
	}
	for i, seg := range allowed {
		if candidate[i] != seg {
			return false
		}
	}
	return true
}

// pathIsPrefixOf reports whether candidate is a strict ancestor of allowed.
func pathIsPrefixOf(candidate, allowed []string) bool {
	if len(candidate) >= len(allowed) {
		return false
	}
	for i, seg := range candidate {
		if allowed[i] != seg {
			return false
		}
	}
	return true
}

// extractAtPath returns the raw JSON at a dotted path, or nil when absent.
func extractAtPath(raw json.RawMessage, path []string) json.RawMessage {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil
	}
	var current json.RawMessage = obj[path[0]]
	for _, seg := range path[1:] {
		if current == nil {
			return nil
		}
		var nested map[string]json.RawMessage
		if err := json.Unmarshal(current, &nested); err != nil {
			return nil
		}
		current = nested[seg]
	}
	return current
}
