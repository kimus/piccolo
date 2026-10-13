// Package integration models Piccolo containerized integration packages.
package integration

import (
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const SchemaV1 = "piccolo.integration.v1"

// Descriptor is returned by an installed integration's `describe --json` command.
type Descriptor struct {
	Schema       string                 `json:"schema"`
	Name         string                 `json:"name"`
	DisplayName  string                 `json:"displayName,omitempty"`
	Commands     CommandList            `json:"commands,omitempty"`
	CommandSpecs map[string]CommandSpec `json:"-"`
	Setup        SetupDescriptor        `json:"setup"`
	Metadata     map[string]string      `json:"metadata,omitempty"`
}

type CommandSpec struct {
	Command string   `json:"command"`
	Args    []string `json:"args,omitempty"`
}

type CommandList []string

type SetupDescriptor struct {
	Command     []string `json:"command"`
	RequiresTTY bool     `json:"requiresTty,omitempty"`
	Ports       PortList `json:"ports,omitempty"`
}

type PortList []string

var nameRE = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

func ParseDescriptor(data []byte) (Descriptor, error) {
	var d Descriptor
	if err := json.Unmarshal(data, &d); err != nil {
		return Descriptor{}, fmt.Errorf("parse integration descriptor: %w", err)
	}
	if d.Setup.Command == nil {
		if setup, ok := d.CommandSpecs["setup"]; ok {
			d.Setup.Command = setup.argv()
		}
	}
	if len(d.Commands) == 0 {
		for _, spec := range d.CommandSpecs {
			if strings.TrimSpace(spec.Command) != "" {
				d.Commands = append(d.Commands, spec.Command)
			}
		}
		d.Commands = uniqueStrings(d.Commands)
	}
	if err := d.Validate(); err != nil {
		return Descriptor{}, err
	}
	return d, nil
}

func (d Descriptor) Validate() error {
	if d.Schema != SchemaV1 {
		return fmt.Errorf("unsupported integration descriptor schema %q (want %q)", d.Schema, SchemaV1)
	}
	if !nameRE.MatchString(strings.TrimSpace(d.Name)) {
		return fmt.Errorf("integration descriptor name %q invalid (want kebab-case)", d.Name)
	}
	if strings.TrimSpace(d.DisplayName) == "" {
		return errors.New("integration descriptor displayName is required")
	}
	if err := validateCommand("setup.command", d.Setup.Command); err != nil {
		return err
	}
	for _, p := range []string(d.Setup.Ports) {
		if strings.TrimSpace(p) == "" || strings.ContainsAny(p, " \t\n") {
			return fmt.Errorf("setup port %q invalid", p)
		}
	}
	for _, c := range []string(d.Commands) {
		if strings.TrimSpace(c) == "" || strings.ContainsAny(c, " \t\n") {
			return fmt.Errorf("command %q invalid", c)
		}
	}
	return nil
}

func (d *Descriptor) UnmarshalJSON(data []byte) error {
	type alias Descriptor
	var raw struct {
		*alias
		Commands json.RawMessage `json:"commands"`
	}
	raw.alias = (*alias)(d)
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if len(raw.Commands) == 0 || string(raw.Commands) == "null" {
		return nil
	}
	var list []string
	if err := json.Unmarshal(raw.Commands, &list); err == nil {
		d.Commands = CommandList(list)
		return nil
	}
	var specs map[string]CommandSpec
	if err := json.Unmarshal(raw.Commands, &specs); err != nil {
		return err
	}
	d.CommandSpecs = specs
	for _, spec := range specs {
		if strings.TrimSpace(spec.Command) != "" {
			d.Commands = append(d.Commands, spec.Command)
		}
	}
	d.Commands = uniqueStrings(d.Commands)
	return nil
}

func (p *PortList) UnmarshalJSON(data []byte) error {
	var list []string
	if err := json.Unmarshal(data, &list); err == nil {
		*p = PortList(list)
		return nil
	}
	var specs []struct {
		Port     int    `json:"port"`
		Protocol string `json:"protocol"`
	}
	if err := json.Unmarshal(data, &specs); err != nil {
		return err
	}
	out := make([]string, 0, len(specs))
	for _, spec := range specs {
		if spec.Port <= 0 {
			continue
		}
		proto := strings.TrimSpace(spec.Protocol)
		if proto == "" {
			proto = "tcp"
		}
		out = append(out, "127.0.0.1:"+strconv.Itoa(spec.Port)+":"+strconv.Itoa(spec.Port)+"/"+proto)
	}
	*p = PortList(out)
	return nil
}

func (c CommandSpec) argv() []string {
	if strings.TrimSpace(c.Command) == "" {
		return nil
	}
	return append([]string{c.Command}, c.Args...)
}

func validateCommand(name string, cmd []string) error {
	if len(cmd) == 0 || strings.TrimSpace(cmd[0]) == "" {
		return fmt.Errorf("%s is required", name)
	}
	for _, part := range cmd {
		if strings.TrimSpace(part) == "" {
			return fmt.Errorf("%s contains an empty argument", name)
		}
	}
	return nil
}

type Resolved struct {
	Input             string
	Ref               string
	Name              string
	DescriptorCommand []string
	Unpinned          bool
}

var aliases = map[string]Resolved{
	"gws": {
		Input:             "gws",
		Ref:               "github:kimus/piccolo-gws",
		Name:              "gws",
		DescriptorCommand: []string{"piccolo-gws", "describe", "--json"},
		Unpinned:          true,
	},
	"google-workspace": {
		Input:             "google-workspace",
		Ref:               "github:kimus/piccolo-gws",
		Name:              "gws",
		DescriptorCommand: []string{"piccolo-gws", "describe", "--json"},
		Unpinned:          true,
	},
}

func uniqueStrings(in []string) CommandList {
	seen := map[string]bool{}
	var out []string
	for _, v := range in {
		v = strings.TrimSpace(v)
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

func Resolve(input string) (Resolved, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return Resolved{}, errors.New("integration reference is required")
	}
	if a, ok := aliases[input]; ok {
		a.Input = input
		return a, nil
	}
	if strings.HasPrefix(input, "github:") {
		base := path.Base(strings.TrimPrefix(input, "github:"))
		if i := strings.IndexAny(base, "?#"); i >= 0 {
			base = base[:i]
		}
		base = strings.TrimSuffix(base, ".git")
		if base == "." || base == "/" || strings.TrimSpace(base) == "" {
			return Resolved{}, fmt.Errorf("cannot derive integration command from %q", input)
		}
		return Resolved{Input: input, Ref: input, Name: base, DescriptorCommand: []string{base, "describe", "--json"}, Unpinned: IsUnpinned(input)}, nil
	}
	return Resolved{}, fmt.Errorf("unknown integration %q (use gws, google-workspace, or github:owner/repo)", input)
}

func IsUnpinned(ref string) bool {
	if !strings.HasPrefix(ref, "github:") {
		return false
	}
	return !strings.Contains(ref, "?rev=") && !strings.Contains(ref, "?ref=") && !strings.Contains(ref, "#")
}

func AliasNames() []string {
	out := make([]string, 0, len(aliases))
	for k := range aliases {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
