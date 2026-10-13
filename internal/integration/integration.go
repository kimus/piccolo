// Package integration models Piccolo containerized integration packages.
package integration

import (
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
)

const SchemaV1 = "piccolo.integration.v1"

// Descriptor is returned by an installed integration's `describe --json` command.
type Descriptor struct {
	Schema      string            `json:"schema"`
	Name        string            `json:"name"`
	DisplayName string            `json:"displayName,omitempty"`
	Commands    []string          `json:"commands,omitempty"`
	Setup       SetupDescriptor   `json:"setup"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

type SetupDescriptor struct {
	Command     []string `json:"command"`
	RequiresTTY bool     `json:"requiresTty,omitempty"`
	Ports       []string `json:"ports,omitempty"`
}

var nameRE = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

func ParseDescriptor(data []byte) (Descriptor, error) {
	var d Descriptor
	if err := json.Unmarshal(data, &d); err != nil {
		return Descriptor{}, fmt.Errorf("parse integration descriptor: %w", err)
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
	for _, p := range d.Setup.Ports {
		if strings.TrimSpace(p) == "" || strings.ContainsAny(p, " \t\n") {
			return fmt.Errorf("setup port %q invalid", p)
		}
	}
	for _, c := range d.Commands {
		if strings.TrimSpace(c) == "" || strings.ContainsAny(c, " \t\n") {
			return fmt.Errorf("command %q invalid", c)
		}
	}
	return nil
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
