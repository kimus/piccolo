// Package manifest loads and validates piccolo.yaml, Piccolo's own launch
// configuration. It intentionally knows nothing about PicoClaw-domain config
// (models, tools, agents): those live in config.json, which is user-owned.
package manifest

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// CurrentVersion is the piccolo.yaml schema version this build understands.
const CurrentVersion = 1

// Manifest is the parsed piccolo.yaml.
type Manifest struct {
	Version   int       `yaml:"version"`
	Container Container `yaml:"container"`
}

// Container describes how serve launches the PicoClaw gateway container.
type Container struct {
	Image   string            `yaml:"image"`
	Name    string            `yaml:"name"`
	Network string            `yaml:"network"`
	Ports   []string          `yaml:"ports,omitempty"`
	Env     map[string]string `yaml:"env,omitempty"`
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
