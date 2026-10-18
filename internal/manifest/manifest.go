// Package manifest loads and validates piccolo.yaml, Piccolo's own
// configuration. Runtime PicoClaw-domain config (models, tools, agents,
// channel tokens) lives in config.json, which is user-owned; piccolo.yaml may
// also hold non-secret Piccolo setup metadata used by setup flows, and — the one
// exception, by decision rather than accident — the enc:// passphrase under
// secrets, which is a secret held in a committed file so that an encrypted
// workspace needs no host setup to start.
package manifest

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/kimus/piccolo/internal/fileperm"
)

// CurrentVersion is the piccolo.yaml schema version this build understands.
const CurrentVersion = 1

// Manifest is the parsed piccolo.yaml.
type Manifest struct {
	Version   int       `yaml:"version"`
	Container Container `yaml:"container"`
	Runtime   Runtime   `yaml:"runtime,omitempty"`
	Setup     Setup     `yaml:"setup,omitempty"`
	Secrets   Secrets   `yaml:"secrets,omitempty"`
}

// Secrets holds a workspace's enc:// decryption passphrase. It is Piccolo-owned
// delivery configuration, and unlike every other part of piccolo.yaml it IS a
// secret: the passphrase is written here so an encrypted workspace needs no host
// setup to boot. That is a deliberate trade, not an oversight — see the field
// comment on Secrets.Passphrase for what it costs.

// Secrets describes how a workspace's encrypted credentials are unlocked. It is
// Piccolo-owned delivery configuration, and it holds NO secret: PassphraseEnv is
// the NAME of a host environment variable, never its value. The value is read at
// serve time through the same env: reference mechanism container.env already uses,
// so the passphrase never exists in a file Piccolo writes.
//
// The distinction is the whole point of the block, so it is enforced rather than
// trusted: a value that looks like a passphrase instead of a variable name is
// rejected at parse time, because a user who pasted their passphrase here would
// otherwise get a plaintext secret in the workspace they were trying to protect,
// with the encryption silently doing nothing.
type Secrets struct {
	// Passphrase is the enc:// decryption passphrase, stored here in piccolo.yaml.
	// Empty means the workspace is not encrypted.
	//
	// This is a deliberate trade: it buys a workspace that starts with no setup
	// ritual — no host variable to export in every shell, no prompt on each run —
	// and it costs the at-rest protection that `enc://` was otherwise buying, because
	// piccolo.yaml is the one file in this workspace that is meant to be committed
	// and shared. Anyone who can read the workspace directory can therefore decrypt
	// it. The wording in help text, README and doctor states that plainly rather
	// than letting `enc://` imply more than it now delivers.
	Passphrase string `yaml:"passphrase,omitempty"`
}

// Runtime holds Piccolo-owned container runtime configuration. This is distinct
// from PicoClaw's config.json runtime/agent settings.
type Runtime struct {
	Toolchain    Toolchain     `yaml:"toolchain,omitempty"`
	Integrations []Integration `yaml:"integrations,omitempty"`
}

// Toolchain describes packages to install with the base image's distribution
// package manager (Alpine apk names) and the command names that must end up on
// PATH inside the container. An entry may pin a version, e.g. "jq=1.7.1-r0".
// Changing either list changes the image content, so Piccolo asks for a
// rebuild rather than rebuilding implicitly.
type Toolchain struct {
	Packages []string `yaml:"packages,omitempty"`
	Commands []string `yaml:"commands,omitempty"`
}

// Integration records Piccolo-owned metadata for an integration package baked
// into the configured gateway image. Provider-specific runtime settings remain
// in config.json or integration-owned state files.
type Integration struct {
	Package           string   `yaml:"package,omitempty" json:"package,omitempty"`
	Requested         string   `yaml:"requested,omitempty" json:"requested,omitempty"`
	Source            string   `yaml:"source,omitempty" json:"source,omitempty"`
	Channel           string   `yaml:"channel,omitempty" json:"channel,omitempty"`
	Selector          string   `yaml:"selector,omitempty" json:"selector,omitempty"`
	ResolvedVersion   string   `yaml:"resolved_version,omitempty" json:"resolvedVersion,omitempty"`
	ArtifactDigest    string   `yaml:"artifact_digest,omitempty" json:"artifactDigest,omitempty"`
	StatePath         string   `yaml:"state_path,omitempty" json:"statePath,omitempty"`
	Name              string   `yaml:"name" json:"name"`
	DisplayName       string   `yaml:"display_name,omitempty" json:"displayName,omitempty"`
	Ref               string   `yaml:"ref,omitempty" json:"ref,omitempty"` // legacy source-build ref
	Commands          []string `yaml:"commands,omitempty" json:"commands,omitempty"`
	DescriptorCommand []string `yaml:"descriptor_command,omitempty" json:"descriptorCommand,omitempty"`
	SetupCommand      []string `yaml:"setup_command,omitempty" json:"setupCommand,omitempty"`
	StatusCommand     []string `yaml:"status_command,omitempty" json:"statusCommand,omitempty"`
	SetupRequiresTTY  bool     `yaml:"setup_requires_tty,omitempty" json:"setupRequiresTty,omitempty"`
	SetupPorts        []string `yaml:"setup_ports,omitempty" json:"setupPorts,omitempty"`
	// MCPServerKey is the one config.json path this integration may manage:
	// tools.mcp.servers.<MCPServerKey>. Piccolo enforces it; the integration
	// cannot widen it.
	MCPServerKey string `yaml:"mcp_server_key,omitempty" json:"mcpServerKey,omitempty"`
}

// StatePathRelative returns the integration state directory name beneath the
// workspace `integrations/` root, e.g. "kimus/piccolo-gws".
func (i Integration) StatePathRelative() string {
	trimmed := strings.TrimSpace(i.StatePath)
	if trimmed == "" {
		return i.Name
	}
	return strings.TrimPrefix(strings.Trim(trimmed, "/"), workspaceIntegrationsPrefix)
}

const workspaceIntegrationsPrefix = "integrations/"

// Setup holds non-secret Piccolo setup metadata that is useful across setup
// runs but is not consumed by PicoClaw at runtime.
type Setup struct {
	Channels SetupChannels `yaml:"channels,omitempty"`
}

// SetupChannels stores provider-specific setup metadata.
type SetupChannels struct {
	Discord DiscordSetup `yaml:"discord,omitempty"`
}

// DiscordSetup stores non-secret Discord setup metadata. The bot token remains
// in config.json because PicoClaw needs it; ApplicationID is used by Piccolo to
// print direct links and the server invite URL.
type DiscordSetup struct {
	ApplicationID string `yaml:"application_id,omitempty"`
}

// Container describes how serve launches the PicoClaw gateway container.
type Container struct {
	Image   string            `yaml:"image"`
	Name    string            `yaml:"name"`
	Network string            `yaml:"network"`
	Ports   []string          `yaml:"ports,omitempty"`
	Env     map[string]string `yaml:"env,omitempty"`
	// PreviousImage records the image the workspace ran before the last
	// successful promotion. It is the single rollback slot: one entry, because
	// "go back to what worked" needs exactly one answer, and a deeper history
	// would be a different feature.
	PreviousImage string `yaml:"previous_image,omitempty"`
}

// Valid network modes for the container.
var validNetworks = map[string]bool{"none": true, "bridge": true, "host": true}

// ErrPlaceholderEnv is returned by ResolveEnv when an env:HOST_VAR reference
// cannot be resolved from the host environment.
type ErrPlaceholderEnv struct {
	Key string
	Var string
}

func (e *ErrPlaceholderEnv) Error() string {
	return fmt.Sprintf("env reference %q for %q: host variable %s is not set", "env:"+e.Var, e.Key, e.Var)
}

// Load reads and strictly parses a piccolo.yaml file.
func Load(path string) (*Manifest, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return Parse(f)
}

// Parse strictly decodes a manifest from r. Unknown keys are an error so
// typos surface early.
func Parse(r io.Reader) (*Manifest, error) {
	dec := yaml.NewDecoder(r)
	dec.KnownFields(true)
	var m Manifest
	if err := dec.Decode(&m); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, errors.New("manifest is empty")
		}
		return nil, fmt.Errorf("parse manifest: %w", err)
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return &m, nil
}

// PrivatePerm is the mode piccolo.yaml is written with.
//
// The file holds the enc:// passphrase, so group- or world-readable is a secret
// readable by any local account. It is deliberately not conditional on whether a
// passphrase happens to be set: a mode that flips depending on content is one no
// user can reason about, and the next opt-in would silently widen exposure again.
const PrivatePerm = fileperm.Private

// Write renders m to path with deterministic 2-space YAML, at PrivatePerm, keeping the
// comments of the file it replaces.
//
// Comments are transplanted ONTO a render that comes from the struct; they are not
// merged from the old document into a node tree the caller then has to keep coherent.
// That direction is the safety property: a node merge would require projecting every
// field by hand — nested maps, sequences, omitempty blocks — and a field the walker
// misses is not cosmetically lost, it is deleted from the file holding the passphrase.
// Struct-first means content is complete by construction and the worst outcome is a
// missing comment.
//
// The mode is asserted rather than requested: os.WriteFile applies perm only when
// CREATING, so rewriting an existing 0644 piccolo.yaml left the passphrase
// world-readable — which is how an already-created workspace stayed exposed even after
// Piccolo "knew" better.
func (m *Manifest) Write(path string) error {
	var existing []byte
	if data, err := os.ReadFile(path); err == nil {
		existing = data
	}
	data, err := m.RenderPreserving(existing)
	if err != nil {
		return err
	}
	// A written-but-still-readable file is the exposure this exists to close, so a
	// failed chmod is reported rather than tolerated.
	if err := fileperm.WritePrivate(path, data); err != nil {
		return fmt.Errorf("write %s: %w", filepath.Base(path), err)
	}
	return nil
}

// RenderPreserving emits the manifest and moves old's comments onto it.
//
// Empty or unparseable old content is not an error: the caller gets the plain render.
// Comment preservation must never become a reason a workspace cannot be written.
func (m *Manifest) RenderPreserving(old []byte) ([]byte, error) {
	fresh, err := m.marshal()
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(old)) == 0 {
		return fresh, nil
	}
	merged, err := transplantComments(old, fresh)
	if err != nil {
		return fresh, nil
	}
	return merged, nil
}

func (m *Manifest) marshal() ([]byte, error) {
	var out strings.Builder
	enc := yaml.NewEncoder(&out)
	enc.SetIndent(2)
	if err := enc.Encode(m); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return []byte(out.String()), nil
}

// transplantComments copies every comment from old onto the node standing at the same
// location in the fresh render. Mapping keys are matched by name and sequence items by
// index, because that is the only identity a config file has: matching by value would
// attach a note about the previous image tag to whatever replaced it.
func transplantComments(old, fresh []byte) ([]byte, error) {
	var oldNode, newNode yaml.Node
	if err := yaml.Unmarshal(old, &oldNode); err != nil {
		return nil, err
	}
	if err := yaml.Unmarshal(fresh, &newNode); err != nil {
		return nil, err
	}
	if len(oldNode.Content) == 0 || len(newNode.Content) == 0 {
		return nil, errors.New("no document to merge")
	}
	// The comments above the first key attach to the DOCUMENT node, not the root
	// mapping — which is where piccolo.yaml's "Generated by piccolo init" header lives.
	// Merging only Content[0] preserved every nested comment and silently lost the two
	// lines that tell a reader what the file is for.
	mergeComments(&oldNode, &newNode)
	mergeComments(oldNode.Content[0], newNode.Content[0])
	var out strings.Builder
	enc := yaml.NewEncoder(&out)
	enc.SetIndent(2)
	if err := enc.Encode(&newNode); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return []byte(out.String()), nil
}

// mergeComments fills in comments the source has and the destination lacks. It never
// overwrites: if the fresh render already carries a comment, the render's own text is
// the more current statement.
func mergeComments(src, dst *yaml.Node) {
	if src == nil || dst == nil {
		return
	}
	if src.HeadComment != "" && dst.HeadComment == "" {
		dst.HeadComment = src.HeadComment
	}
	if src.LineComment != "" && dst.LineComment == "" {
		dst.LineComment = src.LineComment
	}
	if src.FootComment != "" && dst.FootComment == "" {
		dst.FootComment = src.FootComment
	}
	switch {
	case src.Kind == yaml.MappingNode && dst.Kind == yaml.MappingNode:
		// Content is (key, value) pairs. The comment above a key attaches to the KEY node
		// in one shape and to the VALUE node in another depending on nesting, so both are
		// merged and the recursion descends through the value.
		for i := 0; i+1 < len(src.Content); i += 2 {
			sk, sv := src.Content[i], src.Content[i+1]
			dk, dv := findPair(dst, sk.Value)
			if dk == nil {
				// The render does not emit this key — an omitempty block that went empty.
				// Its comment has nowhere correct to go, and re-adding the key to host a
				// comment would put content in the file that is not true.
				continue
			}
			mergeComments(sk, dk)
			mergeComments(sv, dv)
		}
	case src.Kind == yaml.SequenceNode && dst.Kind == yaml.SequenceNode:
		for i := 0; i < len(src.Content) && i < len(dst.Content); i++ {
			mergeComments(src.Content[i], dst.Content[i])
		}
	}
}

// findPair returns the render's (key, value) nodes for a mapping key name.
func findPair(dst *yaml.Node, key string) (*yaml.Node, *yaml.Node) {
	for i := 0; i+1 < len(dst.Content); i += 2 {
		if dst.Content[i].Value == key {
			return dst.Content[i], dst.Content[i+1]
		}
	}
	return nil, nil
}

// EnsurePrivate tightens an existing piccolo.yaml to owner-only access. Thin
// delegation: the rule itself lives in internal/fileperm so every credential-bearing
// file shares one definition.
func EnsurePrivate(path string) (bool, error) { return fileperm.EnsurePrivate(path) }

// Validate checks schema version and container fields.
func (m *Manifest) Validate() error {
	if m.Version != CurrentVersion {
		return fmt.Errorf("unsupported manifest version %d (want %d)", m.Version, CurrentVersion)
	}
	if strings.TrimSpace(m.Container.Image) == "" {
		return errors.New("container.image is required")
	}
	if !IsPinnedImage(m.Container.Image) {
		return fmt.Errorf("container.image %q must be pinned to a specific tag (not empty, not \"latest\")", m.Container.Image)
	}
	if strings.TrimSpace(m.Container.Name) == "" {
		return errors.New("container.name is required")
	}
	if net := m.Container.Network; net != "" && !validNetworks[net] {
		return fmt.Errorf("container.network %q invalid (want none, bridge, or host)", net)
	}
	for _, p := range m.Container.Ports {
		if err := validatePort(p); err != nil {
			return err
		}
	}
	if err := validateStringList("runtime.toolchain.packages", m.Runtime.Toolchain.Packages); err != nil {
		return err
	}
	if err := validateStringList("runtime.toolchain.commands", m.Runtime.Toolchain.Commands); err != nil {
		return err
	}
	if err := m.Secrets.Validate(); err != nil {
		return err
	}
	seenIntegrations := map[string]bool{}
	for i, in := range m.Runtime.Integrations {
		prefix := fmt.Sprintf("runtime.integrations[%d]", i)
		if strings.TrimSpace(in.Name) == "" {
			return fmt.Errorf("%s.name is required", prefix)
		}
		if strings.ContainsAny(in.Name, " \t\n") {
			return fmt.Errorf("%s.name %q invalid", prefix, in.Name)
		}
		if seenIntegrations[in.Name] {
			return fmt.Errorf("%s.name %q is duplicated", prefix, in.Name)
		}
		seenIntegrations[in.Name] = true
		if strings.TrimSpace(in.Ref) == "" && strings.TrimSpace(in.Package) == "" {
			return fmt.Errorf("%s.package is required", prefix)
		}
		if err := validateStringList(prefix+".commands", in.Commands); err != nil {
			return err
		}
		if err := validateStringList(prefix+".descriptor_command", in.DescriptorCommand); err != nil {
			return err
		}
		if err := validateStringList(prefix+".setup_command", in.SetupCommand); err != nil {
			return err
		}
		if err := validateStringList(prefix+".setup_ports", in.SetupPorts); err != nil {
			return err
		}
	}
	return nil
}

// IsPinnedImage reports whether ref carries an explicit non-latest tag or a
// digest.
func IsPinnedImage(ref string) bool {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return false
	}
	if strings.Contains(ref, "@") {
		return true // digest-pinned is always acceptable
	}
	// The tag lives after the last colon that follows the last slash (a colon
	// before the last slash belongs to a registry port, not a tag).
	lastSlash := strings.LastIndex(ref, "/")
	lastColon := strings.LastIndex(ref, ":")
	if lastColon < 0 || lastColon < lastSlash || lastColon == len(ref)-1 {
		return false
	}
	tag := ref[lastColon+1:]
	return tag != "latest"
}

func validateStringList(name string, values []string) error {
	seen := map[string]bool{}
	for _, v := range values {
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			return fmt.Errorf("%s contains an empty entry", name)
		}
		if strings.ContainsAny(trimmed, " \t\n\r") {
			return fmt.Errorf("%s entry %q must not contain whitespace", name, v)
		}
		if seen[trimmed] {
			return fmt.Errorf("%s contains duplicate entry %q", name, trimmed)
		}
		seen[trimmed] = true
	}
	return nil
}

func validatePort(spec string) error {
	portion := strings.TrimSpace(spec)
	if i := strings.LastIndex(portion, "/"); i >= 0 {
		portion = portion[:i]
	}
	parts := strings.Split(portion, ":")
	if len(parts) < 1 || len(parts) > 3 {
		return fmt.Errorf("container.ports entry %q invalid (want [host_ip:]host_port:container_port)", spec)
	}
	containerPort := parts[len(parts)-1]
	if n, err := parsePortNum(containerPort); err != nil || n <= 0 {
		return fmt.Errorf("container.ports entry %q invalid (bad container port)", spec)
	}
	if len(parts) >= 2 && strings.TrimSpace(parts[len(parts)-2]) != "" {
		if n, err := parsePortNum(parts[len(parts)-2]); err != nil || n <= 0 {
			return fmt.Errorf("container.ports entry %q invalid (bad host port)", spec)
		}
	}
	return nil
}

func parsePortNum(s string) (int, error) {
	n := 0
	for _, c := range strings.TrimSpace(s) {
		if c < '0' || c > '9' {
			return 0, errors.New("not a number")
		}
		n = n*10 + int(c-'0')
	}
	if n > 65535 {
		return 0, errors.New("out of range")
	}
	return n, nil
}

// EnvPrefix marks a manifest env value as a reference to a host variable.
const EnvPrefix = "env:"

// ResolveEnv turns the manifest env map into KEY=value pairs for the
// container, resolving env:HOST_VAR references through lookup. The result is
// sorted by key for deterministic container specs. Missing references return
// an *ErrPlaceholderEnv.
func (m *Manifest) ResolveEnv(lookup func(string) (string, bool)) ([]string, error) {
	keys := make([]string, 0, len(m.Container.Env))
	for k := range m.Container.Env {
		keys = append(keys, k)
	}
	sortStrings(keys)

	out := make([]string, 0, len(keys))
	for _, k := range keys {
		v := m.Container.Env[k]
		if ref, ok := strings.CutPrefix(v, EnvPrefix); ok {
			val, found := lookup(ref)
			if !found {
				return nil, &ErrPlaceholderEnv{Key: k, Var: ref}
			}
			v = val
		}
		out = append(out, k+"="+v)
	}
	return out, nil
}

// EnvRefs lists the host variable names referenced via env: prefixes, sorted.
func (m *Manifest) EnvRefs() []string {
	var refs []string
	for _, v := range m.Container.Env {
		if ref, ok := strings.CutPrefix(v, EnvPrefix); ok {
			refs = append(refs, ref)
		}
	}
	sortStrings(refs)
	return refs
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// Validate checks the secrets block. A whitespace-only passphrase is refused
// rather than rounded to empty, because a workspace that looks encrypted and
// cannot be decrypted is the state this whole feature must not produce. An absent
// value is valid: it simply means the workspace stores plaintext.
func (s Secrets) Validate() error {
	if s.Passphrase == "" {
		return nil
	}
	if strings.TrimSpace(s.Passphrase) == "" {
		return errors.New("secrets.passphrase is whitespace only — set a real passphrase or remove the field so the workspace stores plaintext")
	}
	return nil
}

// Passphrase returns the workspace's enc:// decryption passphrase, or "" when the
// workspace is not encrypted. Callers treat "" as the encrypted-posture test.
func (m *Manifest) Passphrase() string {
	return m.Secrets.Passphrase
}
