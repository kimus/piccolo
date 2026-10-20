package doctor

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// The report's grouping is metadata, not a substring guess. A check that forgets
// to declare a section is still PRINTED (GroupBySection sorts unknowns last), but
// it must not land there silently: this test is the guard that makes adding a
// check without a Section a test failure rather than a mystery heading.
func TestEveryResultDeclaresASection(t *testing.T) {
	dir := initWorkspace(t)
	results := Run(context.Background(), Options{
		Workspace: dir,
		Engine:    fakeEngine{imageExists: true},
		LookupEnv: func(string) (string, bool) { return "", false },
	})
	if len(results) == 0 {
		t.Fatal("no results")
	}
	known := map[Section]bool{}
	for _, s := range Sections() {
		known[s] = true
	}
	for _, r := range results {
		if !known[r.Section] {
			t.Errorf("result has no declared section: %+v", r)
		}
	}
}

// Grouping is only useful if the sections arrive in the order the renderer walks
// them, and only honest if a check's own block stays contiguous rather than being
// scattered by status.
func TestResultsArriveGroupedInSectionOrder(t *testing.T) {
	grouped := GroupBySection([]Result{
		{Status: OK, Section: SecCapabilities, Message: "c1"},
		{Status: FAIL, Section: SecWorkspace, Message: "w1"},
		{Status: OK, Section: SecEnvironment, Message: "e1"},
		{Status: WARN, Section: SecWorkspace, Message: "w2"},
		{Status: OK, Message: "no section declared"},
	})
	want := []string{"e1", "w1", "w2", "c1", "no section declared"}
	if len(grouped) != len(want) {
		t.Fatalf("grouped = %v", grouped)
	}
	for i, w := range want {
		if grouped[i].Message != w {
			t.Errorf("position %d = %q, want %q", i, grouped[i].Message, w)
		}
	}
}

// A dead daemon is ONE finding, not a reason to stop: the previous behaviour
// printed `docker daemon not reachable` and nothing else, hiding the half of the
// answer the user can act on while Docker is starting — whether the workspace
// itself is intact.
func TestDaemonDownStillReportsTheWorkspace(t *testing.T) {
	dir := initWorkspace(t)
	results := Run(context.Background(), Options{
		Workspace: dir,
		Engine:    fakeEngine{pingErr: errors.New("connection refused")},
	})
	r := find(results, "docker daemon not reachable")
	if r == nil || r.Status != FAIL {
		t.Fatalf("expected daemon FAIL, got %v", results)
	}
	if r.Section != SecEnvironment {
		t.Errorf("daemon finding should sit in %q, got %q", SecEnvironment, r.Section)
	}
	for _, want := range []string{"manifest: piccolo.yaml", "config.json (schema v3)", "agent: main"} {
		if find(results, want) == nil {
			t.Errorf("file checks must still run without a daemon; missing %q in %v", want, results)
		}
	}
	// And the engine-dependent findings must stay SILENT, not become a pile of
	// "not found" errors: the daemon was never asked.
	for _, absent := range []string{"image present", "image not present", "stale container", "running container image", "runtime image retention"} {
		if find(results, absent) != nil {
			t.Errorf("no daemon was consulted, so %q must not be reported: %v", absent, results)
		}
	}
}

// The remedy is data, not decoration: Text() is what a caller searching results
// should read, so moving an instruction out of Message into Hint cannot make a
// finding unfindable.
func TestResultTextJoinsConditionAndRemedy(t *testing.T) {
	r := Result{Status: WARN, Message: "image not present locally: x", Hint: "'piccolo serve' pulls it once"}
	if got := r.Text(); got != "image not present locally: x — 'piccolo serve' pulls it once" {
		t.Errorf("Text() = %q", got)
	}
	bare := Result{Status: OK, Message: "docker daemon reachable"}
	if bare.Text() != bare.Message {
		t.Errorf("a result with no hint must read as its message, got %q", bare.Text())
	}
	if strings.Contains(bare.Text(), "—") {
		t.Error("no hint must not fabricate a separator")
	}
}

// Sections carry their own title, and the zero value needs one: a result that
// declared nothing must be reported under a heading that says so rather than
// under a blank line.
func TestSectionTitlesAreStated(t *testing.T) {
	for _, sec := range Sections() {
		if sec.Title() == string(sec) || sec.Title() == "" {
			t.Errorf("section %q needs a human title, got %q", sec, sec.Title())
		}
	}
	if Section("").Title() != "Other" {
		t.Errorf("undeclared section title = %q, want Other", Section("").Title())
	}
}
