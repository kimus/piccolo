package doctor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kimus/piccolo/internal/generate"
)

// The generated file under test: input-free template, so Piccolo can re-render
// it today and prove currency or staleness.
func skillPath(dir string) string {
	return filepath.Join(dir, "picoclaw", "skills", generate.FileHandlingSkillName, "SKILL.md")
}

func writeSkill(t *testing.T, dir, content string) {
	t.Helper()
	path := skillPath(dir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestGeneratedProvenanceAbsentRecordIsNotAFinding(t *testing.T) {
	dir := t.TempDir()
	results := checkGeneratedProvenance(dir)
	if len(results) != 1 || results[0].Status != OK {
		t.Fatalf("an absent record is a normal state, want one OK, got %+v", results)
	}
	if !strings.Contains(results[0].Message, "no record") {
		t.Errorf("message should say there is no record: %q", results[0].Message)
	}
}

func TestGeneratedProvenanceCurrent(t *testing.T) {
	dir := t.TempDir()
	content := generate.FileHandlingSkillMD()
	writeSkill(t, dir, content)
	if err := generate.RecordGenerated(dir, "picoclaw/skills/file-handling/SKILL.md", generate.SourceFileHandlingSkill, []byte(content)); err != nil {
		t.Fatal(err)
	}
	results := checkGeneratedProvenance(dir)
	if len(results) != 1 || results[0].Status != OK {
		t.Fatalf("want one OK, got %+v", results)
	}
	if !strings.Contains(results[0].Message, "as generated") {
		t.Errorf("message = %q", results[0].Message)
	}
}

// A hand-edited copy must never be reported as needing a refresh: the remedy
// would overwrite the user's work, which is the one thing Piccolo's rules forbid.
func TestGeneratedProvenanceCustomisedIsSilentAndNotStale(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "as generated")
	if err := generate.RecordGenerated(dir, "picoclaw/skills/file-handling/SKILL.md", generate.SourceFileHandlingSkill, []byte("as generated")); err != nil {
		t.Fatal(err)
	}
	// The user edits it, and the template has also moved on since.
	writeSkill(t, dir, "as generated plus the user's own line")
	results := checkGeneratedProvenance(dir)
	for _, r := range results {
		if strings.Contains(r.Message, "predates the current template") || r.Status == WARN {
			t.Fatalf("a customised file must not be reported as stale or warned: %+v", results)
		}
	}
	if find(results, "customised") == nil {
		t.Errorf("expected a customised count in %+v", results)
	}
}

// The bug this exists for: the copy is untouched, but the template was fixed in
// code and the fix never reached the workspace. The report must name the source,
// so a reader (or an agent) knows where the change belongs.
func TestGeneratedProvenanceStaleNamesTheSource(t *testing.T) {
	dir := t.TempDir()
	// Simulate an older template's output: different from what the current
	// template renders, with the record agreeing with the file on disk.
	writeSkill(t, dir, "older template output")
	if err := generate.RecordGenerated(dir, "picoclaw/skills/file-handling/SKILL.md", generate.SourceFileHandlingSkill, []byte("older template output")); err != nil {
		t.Fatal(err)
	}
	results := checkGeneratedProvenance(dir)
	stale := find(results, "predates the current template")
	if stale == nil {
		t.Fatalf("stale copy must be reported, got %+v", results)
	}
	if stale.Status != WARN {
		t.Errorf("stale is a warning, not a failure: %v", stale.Status)
	}
	for _, want := range []string{"piccolo setup", "internal/generate/templates.go", "not in the copy"} {
		if !strings.Contains(stale.Message, want) {
			t.Errorf("stale message missing %q: %q", want, stale.Message)
		}
	}
	if Failed(results) {
		t.Error("provenance findings must never fail doctor")
	}
}

func TestGeneratedProvenanceMissingFileIsWarned(t *testing.T) {
	dir := t.TempDir()
	if err := generate.RecordGenerated(dir, "picoclaw/skills/file-handling/SKILL.md", generate.SourceFileHandlingSkill, []byte("anything")); err != nil {
		t.Fatal(err)
	}
	results := checkGeneratedProvenance(dir)
	missing := find(results, "generated file is missing")
	if missing == nil {
		t.Fatalf("missing recorded file must be reported, got %+v", results)
	}
	if missing.Status != WARN || Failed(results) {
		t.Errorf("missing is a warning only: %+v", *missing)
	}
}

func TestGeneratedProvenanceCorruptRecordWarns(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(generate.ProvenancePath(dir), []byte("{ nope"), 0o644); err != nil {
		t.Fatal(err)
	}
	results := checkGeneratedProvenance(dir)
	if find(results, "unreadable") == nil {
		t.Fatalf("corrupt record must be named, got %+v", results)
	}
	if Failed(results) {
		t.Error("a corrupt record must not fail doctor")
	}
}

// Wire the check into the full run so it cannot be silently dropped from the list.
func TestDoctorRunIncludesProvenanceCheck(t *testing.T) {
	dir := initWorkspace(t)
	// initWorkspace writes the plan but, like an older Piccolo, records nothing.
	results := Run(context.Background(), Options{
		Workspace: dir,
		Engine:    fakeEngine{imageExists: true},
		LookupEnv: func(string) (string, bool) { return "", false },
	})
	if find(results, "generated-file provenance: no record") == nil {
		t.Errorf("doctor output has no provenance line for an unrecorded workspace: %+v", results)
	}
	if Failed(results) {
		t.Errorf("an absent record must not fail doctor: %+v", results)
	}

	// Now generate the record the way init does, and re-run.
	in := generate.Inputs{
		AssistantName: "Piccolo", OwnerName: "Kimus", Purpose: "personal",
		Specialists: []string{"research"},
		Model:       generate.ModelInput{Alias: "gpt-5.4", Provider: "openai", Model: "gpt-5.4", APIKey: "sk-real-key"},
		WebSearch:   true,
	}
	plan, err := generate.BuildPlan(in)
	if err != nil {
		t.Fatal(err)
	}
	written := []string{
		filepath.Join("picoclaw", "skills", generate.FileHandlingSkillName, "SKILL.md"),
		filepath.Join("picoclaw", "agents", "main", "AGENT.md"),
	}
	if err := plan.RecordProvenance(dir, written); err != nil {
		t.Fatal(err)
	}
	results = Run(context.Background(), Options{
		Workspace: dir,
		Engine:    fakeEngine{imageExists: true},
		LookupEnv: func(string) (string, bool) { return "", false },
	})
	if find(results, "generated files:") == nil {
		t.Errorf("recorded workspace must report its generated files: %+v", results)
	}
	if Failed(results) {
		t.Errorf("recorded workspace must not fail doctor: %+v", results)
	}
}

// The hole this closes: EnsureGitignore runs only during init, so a workspace
// predating a required entry keeps it missing, and the first sign is usually the
// junk already committed. One line, because a legacy workspace can lack several.
func TestGitignoreHygieneReportsMissingEntries(t *testing.T) {
	dir := t.TempDir()
	if _, err := generate.EnsureGitignore(dir); err != nil {
		t.Fatal(err)
	}
	results := checkGitignoreHygiene(dir)
	if len(results) != 1 || results[0].Status != OK {
		t.Fatalf("complete ignore file should be one OK, got %+v", results)
	}

	data, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	var kept []string
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) != generate.GitignoreOSMetadata {
			kept = append(kept, line)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(strings.Join(kept, "\n")), 0o644); err != nil {
		t.Fatal(err)
	}
	results = checkGitignoreHygiene(dir)
	warn := find(results, ".gitignore is missing")
	if warn == nil {
		t.Fatalf("missing entry must be reported: %+v", results)
	}
	if warn.Status != WARN || Failed(results) {
		t.Errorf("a missing ignore rule is a warning, never a failure: %+v", *warn)
	}
	if !strings.Contains(warn.Message, generate.GitignoreOSMetadata) {
		t.Errorf("report must name the rule: %q", warn.Message)
	}
	if !strings.Contains(warn.Message, "piccolo init") {
		t.Errorf("report must name the remedy: %q", warn.Message)
	}
	if len(results) != 1 {
		t.Errorf("several missing rules must collapse to one line, got %d", len(results))
	}
}

// Calling the check directly proves nothing about the report a user sees. This
// goes through Run, so deleting the wiring fails the suite.
func TestDoctorRunIncludesGitignoreCheck(t *testing.T) {
	dir := initWorkspace(t)
	results := Run(context.Background(), Options{
		Workspace: dir,
		Engine:    fakeEngine{imageExists: true},
		LookupEnv: func(string) (string, bool) { return "", false },
	})
	if find(results, "workspace .gitignore") == nil {
		t.Errorf("doctor must report the ignore-file check: %+v", results)
	}
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("picoclaw/config.json\npicoclaw/logs/\nintegrations/*/auth/\nruntime/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	results = Run(context.Background(), Options{
		Workspace: dir,
		Engine:    fakeEngine{imageExists: true},
		LookupEnv: func(string) (string, bool) { return "", false },
	})
	r := find(results, "workspace .gitignore is missing")
	if r == nil {
		t.Errorf("Run must surface the missing OS-metadata rule: %+v", results)
	} else if !strings.Contains(r.Message, generate.GitignoreOSMetadata) {
		t.Errorf("Run report must name the rule: %q", r.Message)
	}
}
