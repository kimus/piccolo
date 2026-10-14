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

	cur := doc
	for _, key := range []string{"tools", "mcp", "servers"} {
		next, ok := cur.GetObject(key)
		if !ok {
			next = configdoc.NewDoc()
			cur.Set(key, next)
		}
		cur = next
	}
	// Set preserves position on update, so re-registering a server does not
	// reorder the user's other servers.
	cur.Set(serverKey, sub)
	return serverKey, nil
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
