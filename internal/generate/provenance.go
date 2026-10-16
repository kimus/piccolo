package generate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// LockFileName is the workspace-root record of the exact bytes generation wrote,
// and which template produced each one. The contents are provenance (see
// Provenance); the name says only what the file is for the workspace: a lock
// beside piccolo.yaml recording what the tool last produced.
//
// It exists because the copies are user-owned and create-only: a template fix
// never reaches an existing workspace, and without this record there is no way to
// tell a hand-edited file apart from an untouched one that predates the fix. Those
// two facts demand opposite responses — leave one alone, refresh the other — so they
// cannot share a single "differs from template" warning.
const LockFileName = "piccolo.lock"

// version is stamped by the CLI at startup so a record can name the Piccolo that
// wrote it. Staleness itself is decided by digests, never by this string, so an
// unstamped dev build only makes the record less informative — never wrong.
var version string

// SetVersion records the running Piccolo version for generated provenance.
func SetVersion(v string) { version = strings.TrimSpace(v) }

// VersionStamp is the Piccolo version stamped into new records ("" when unset).
func VersionStamp() string { return version }

// ProvenanceSchemaVersion is the schema of the record.
const ProvenanceSchemaVersion = 1

// ProvenanceFile is one recorded generation output.
type ProvenanceFile struct {
	// SHA256 is the digest of the bytes Piccolo wrote, "sha256:<hex>".
	SHA256 string `json:"sha256"`
	// Source names the template that produced it, so a report can point at the
	// file to fix rather than at the copy that was edited.
	Source string `json:"source"`
}

// Provenance maps workspace-relative paths to their generation record. Paths use
// forward slashes so a record committed on one platform reads on another.
type Provenance struct {
	Version int                       `json:"version"`
	Piccolo string                    `json:"piccolo,omitempty"`
	Files   map[string]ProvenanceFile `json:"files"`
}

// Digest is the recorded identity of generated content.
func Digest(content []byte) string {
	sum := sha256.Sum256(content)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// LockPath is the absolute record location in a workspace.
func LockPath(dir string) string {
	return filepath.Join(dir, LockFileName)
}

// NewProvenance starts an empty record.
func NewProvenance(piccoloVersion string) *Provenance {
	return &Provenance{Version: ProvenanceSchemaVersion, Piccolo: piccoloVersion, Files: map[string]ProvenanceFile{}}
}

// Record notes that Piccolo wrote content to rel from the named template.
func (p *Provenance) Record(rel, source string, content []byte) {
	if p.Files == nil {
		p.Files = map[string]ProvenanceFile{}
	}
	p.Files[toSlash(rel)] = ProvenanceFile{SHA256: Digest(content), Source: source}
}

// Drop removes a path from the record (a file the user deleted is not ours to
// keep asserting exists).
func (p *Provenance) Drop(rel string) {
	delete(p.Files, toSlash(rel))
}

// Paths returns the recorded paths, sorted, for deterministic rendering.
func (p *Provenance) Paths() []string {
	out := make([]string, 0, len(p.Files))
	for rel := range p.Files {
		out = append(out, rel)
	}
	sort.Strings(out)
	return out
}

// LegacyPath is the record's previous file name (piccolo.provenance.json).
func LegacyPath(dir string) string { return filepath.Join(dir, LegacyFileName) }

// LegacyFileName is the name the record shipped under before the rename. It is
// read once, on the first write after the rename, so entries are not silently
// lost; nothing reads it afterwards, because carrying two formats forever is the
// compatibility work this project's rules exclude. Exported so doctor names the
// same file this package reads rather than keeping its own copy of the string.
const LegacyFileName = "piccolo.provenance.json"

// SeedFromLegacy adopts the record under its old name when no new one exists, and
// removes the old file so a workspace is not left holding two records that can
// disagree. Returns the record to keep writing to.
func SeedFromLegacy(dir string) (*Provenance, error) {
	if _, err := os.Stat(LockPath(dir)); err == nil {
		return nil, nil // new record already exists; nothing to adopt
	}
	data, err := os.ReadFile(LegacyPath(dir))
	if err != nil {
		return nil, nil // no old record either: not a problem, just nothing to do
	}
	var old Provenance
	if err := json.Unmarshal(data, &old); err != nil {
		// A corrupt legacy record is dropped rather than propagated: it cannot be
		// trusted, and refusing to write would leave the workspace stuck.
		return nil, os.Remove(LegacyPath(dir))
	}
	if err := os.Remove(LegacyPath(dir)); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("remove %s: %w", LegacyFileName, err)
	}
	p := &old
	if p.Files == nil {
		p.Files = map[string]ProvenanceFile{}
	}
	p.Version = ProvenanceSchemaVersion
	return p, nil
}

// LoadProvenance reads the record. A missing or unreadable file yields (nil,
// nil): the absence of a record is a normal state for a workspace created before
// provenance existed, and callers must report it without failing.
func LoadProvenance(dir string) (*Provenance, error) {
	data, err := os.ReadFile(LockPath(dir))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var p Provenance
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, err
	}
	if p.Files == nil {
		p.Files = map[string]ProvenanceFile{}
	}
	return &p, nil
}

// Save writes the record deterministically: sorted paths, stable key order, no
// secrets — digests, paths, and template names only — so it is meant to be
// committed alongside piccolo.yaml.
func (p *Provenance) Save(dir string) error {
	data, err := p.Marshal()
	if err != nil {
		return err
	}
	return os.WriteFile(LockPath(dir), data, 0o644)
}

// provenanceWire is the on-disk shape. Marshalling through it (rather than the
// Provenance map directly) is what makes the file byte-stable: Go would otherwise
// order map keys by its own rules and every rewrite would look like a change.
type provenanceWire struct {
	Version int                       `json:"version"`
	Piccolo string                    `json:"piccolo,omitempty"`
	Files   map[string]ProvenanceFile `json:"files"`
}

// Marshal renders the record deterministically.
func (p *Provenance) Marshal() ([]byte, error) {
	wire := provenanceWire{Version: p.Version, Piccolo: p.Piccolo, Files: make(map[string]ProvenanceFile, len(p.Files))}
	for _, rel := range p.Paths() {
		wire.Files[rel] = p.Files[rel]
	}
	data, err := json.MarshalIndent(wire, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// RecordGenerated notes one freshly written generated file in the workspace
// record, creating the record when absent. Callers that write generated content
// outside init (piccolo setup sync) must use this, or Piccolo would report its
// own rewrite as a user edit.
func RecordGenerated(dir, rel, source string, content []byte) error {
	p, err := LoadProvenance(dir)
	if err != nil {
		return err
	}
	if p == nil {
		p = NewProvenance(VersionStamp())
	}
	p.Record(rel, source, content)
	return p.Save(dir)
}

// UnrecordGenerated drops a path from the record — a file the user deleted is
// not ours to keep asserting exists. Absent record: no-op.
func UnrecordGenerated(dir, rel string) error {
	p, err := LoadProvenance(dir)
	if err != nil || p == nil {
		return err
	}
	p.Drop(rel)
	return p.Save(dir)
}

// Status classifies one recorded file against what is on disk and what the
// current template renders.
type Status int

const (
	// StatusUnrecorded: Piccolo has no record of this path.
	StatusUnrecorded Status = iota
	// StatusMissing: recorded, but the file is gone.
	StatusMissing
	// StatusCurrent: untouched since generation, and matches the current
	// template render.
	StatusCurrent
	// StatusCustomised: the user changed it. Not a defect; Piccolo must not
	// overwrite it and a template fix will never land here.
	StatusCustomised
	// StatusStale: byte-identical to what Piccolo wrote, but the template has
	// changed since. The only case where refreshing is strictly better.
	StatusStale
)

func (s Status) String() string {
	switch s {
	case StatusCurrent:
		return "current"
	case StatusCustomised:
		return "customised"
	case StatusStale:
		return "stale"
	case StatusMissing:
		return "missing"
	default:
		return "unrecorded"
	}
}

// Classify compares the record against onDisk (nil when the file is absent) and
// currentTemplate (nil when Piccolo cannot re-render this file today).
//
// A customised file is never reported as stale, and a stale file is never
// reported as customised: the distinction is the whole point of keeping the
// record, because one means "the user owns this, leave it" and the other means
// "our own fix has not reached this workspace".
func (p *Provenance) Classify(rel string, onDisk []byte, currentTemplate []byte) Status {
	if p == nil {
		return StatusUnrecorded
	}
	entry, ok := p.Files[toSlash(rel)]
	if !ok {
		return StatusUnrecorded
	}
	if onDisk == nil {
		return StatusMissing
	}
	if Digest(onDisk) != entry.SHA256 {
		return StatusCustomised
	}
	if currentTemplate != nil && Digest(currentTemplate) != entry.SHA256 {
		return StatusStale
	}
	return StatusCurrent
}

// Source names the template that produced a recorded path, for messages that
// must tell the reader where to fix things.
func (p *Provenance) Source(rel string) string {
	if p == nil {
		return ""
	}
	return p.Files[toSlash(rel)].Source
}

func toSlash(rel string) string {
	return filepath.ToSlash(rel)
}

// currentRenderSources lists the templates Piccolo can re-render with no user
// input. Kept next to CurrentRender so adding a Source* constant without adding
// its renderer here is a visible, single-place decision.
var currentRenderSources = map[string]func() []byte{
	SourceFileHandlingSkill: func() []byte { return []byte(FileHandlingSkillMD()) },
}

// CurrentRender renders a named template's bytes today. ok is false for
// templates that consume wizard answers (agent names, purpose, tool toggles):
// Piccolo cannot re-derive those without re-running init, so it must not claim
// they are stale — the honest output for them is only "edited by you or not".
func CurrentRender(source string) ([]byte, bool) {
	render, ok := currentRenderSources[source]
	if !ok {
		return nil, false
	}
	return render(), true
}
