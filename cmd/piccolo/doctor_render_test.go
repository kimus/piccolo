package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/kimus/piccolo/internal/doctor"
)

// The report is grouped by section, in the order the doctor package declares.
// A renderer that invented its own order would make the two halves of one
// feature disagree about what a report looks like depending on who printed it.
func TestRenderDoctorGroupsBySection(t *testing.T) {
	results := doctor.GroupBySection([]doctor.Result{
		{Status: doctor.OK, Section: doctor.SecCapabilities, Message: "channel: discord"},
		{Status: doctor.WARN, Section: doctor.SecEnvironment, Message: "image not present locally: img"},
		{Status: doctor.OK, Section: doctor.SecEnvironment, Message: "docker daemon reachable"},
	})
	var out bytes.Buffer
	if err := renderDoctor(&out, styler{}, results, renderDoctorOptions{}); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{"Environment", "Channels & integrations", "docker daemon reachable", "channel: discord"} {
		if !strings.Contains(got, want) {
			t.Errorf("report must contain %q:\n%s", want, got)
		}
	}
	// Environment is the layer everything rides on, so it comes first.
	if strings.Index(got, "Environment") > strings.Index(got, "Channels & integrations") {
		t.Errorf("sections out of order:\n%s", got)
	}
}

// A section whose checks all stayed silent prints NO heading: many doctor checks
// are quiet by design (env refs, a stale container, MCP with nothing installed),
// and an empty heading would read as a problem the code never claimed.
func TestRenderDoctorSkipsEmptySections(t *testing.T) {
	results := []doctor.Result{{Status: doctor.OK, Section: doctor.SecWorkspace, Message: "manifest: piccolo.yaml"}}
	var out bytes.Buffer
	if err := renderDoctor(&out, styler{}, doctor.GroupBySection(results), renderDoctorOptions{}); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if !strings.Contains(got, "Workspace") {
		t.Fatalf("the populated section must be headed:\n%s", got)
	}
	for _, absent := range []string{"Environment", "Credentials", "Assistant", "Channels & integrations"} {
		if strings.Contains(got, absent) {
			t.Errorf("empty section %q printed a heading:\n%s", absent, got)
		}
	}
}

// Never colour-only, and never glyph-only: the piped form keeps the bracket
// markers this command has always printed, and the terminal form keeps the WORD
// next to the glyph, because a missing codepoint turns `⚠` into a box.
func TestDoctorMarkersKeepTheirTextInBothForms(t *testing.T) {
	var plain bytes.Buffer
	if err := renderDoctor(&plain, styler{}, []doctor.Result{
		{Status: doctor.WARN, Section: doctor.SecWorkspace, Message: "env reference not set"},
	}, renderDoctorOptions{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plain.String(), "[WARN]") {
		t.Errorf("piped output must keep the bracket marker:\n%s", plain.String())
	}
	if strings.ContainsAny(plain.String(), "\x1b") {
		t.Errorf("piped output must carry no escapes:\n%q", plain.String())
	}

	var tty bytes.Buffer
	// enabled styler with no width: glyphs, words, colour, no wrapping.
	if err := renderDoctor(&tty, styler{enabled: true}, []doctor.Result{
		{Status: doctor.WARN, Section: doctor.SecWorkspace, Message: "env reference not set"},
	}, renderDoctorOptions{}); err != nil {
		t.Fatal(err)
	}
	line := tty.String()
	if !strings.Contains(line, "warn") {
		t.Errorf("terminal output must still name the severity:\n%q", line)
	}
	if !strings.Contains(line, "⚠") {
		t.Errorf("terminal output should carry the glyph:\n%q", line)
	}
}

// The remedy is a separate line, so a 250-character finding stays scannable and
// the thing to type is never buried at the end of a paragraph.
func TestRenderDoctorPutsTheRemedyOnItsOwnLine(t *testing.T) {
	var out bytes.Buffer
	err := renderDoctor(&out, styler{}, []doctor.Result{{
		Status:  doctor.WARN,
		Section: doctor.SecEnvironment,
		Message: "local Piccolo runtime image not present: img",
		Hint:    "build it with 'piccolo image build --set'",
	}}, renderDoctorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(out.String(), "\n")
	var found bool
	for _, l := range lines {
		if strings.Contains(l, "build it with") {
			found = true
			if strings.Contains(l, "not present") {
				t.Errorf("remedy must not share a line with the condition: %q", l)
			}
			if !strings.Contains(l, "→") {
				t.Errorf("remedy line should be marked: %q", l)
			}
		}
	}
	if !found {
		t.Fatalf("remedy missing from:\n%s", out.String())
	}
}

// --compact is a display choice, not a claim that nothing was checked: a passing
// section keeps its heading and says how many checks it held.
func TestRenderDoctorCompactFoldsPassingSections(t *testing.T) {
	results := doctor.GroupBySection([]doctor.Result{
		{Status: doctor.OK, Section: doctor.SecWorkspace, Message: "manifest: piccolo.yaml"},
		{Status: doctor.OK, Section: doctor.SecWorkspace, Message: "workspace layout: structured"},
		{Status: doctor.WARN, Section: doctor.SecCredentials, Message: "piccolo.yaml is readable by group/others"},
	})
	var out bytes.Buffer
	if err := renderDoctor(&out, styler{}, results, renderDoctorOptions{Compact: true}); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if strings.Contains(got, "manifest: piccolo.yaml") {
		t.Errorf("compact must not repeat passing findings:\n%s", got)
	}
	if !strings.Contains(got, "Workspace") || !strings.Contains(got, "all 2 checks passed") {
		t.Errorf("a passing section must still name itself and its count:\n%s", got)
	}
	if !strings.Contains(got, "piccolo.yaml is readable by group/others") {
		t.Errorf("compact must still show the warning:\n%s", got)
	}
}

// Wrapping is for the person at a terminal, and must never break a token they
// would copy: paths, image tags, and quoted commands survive intact.
func TestWrapIndentKeepsTokensWhole(t *testing.T) {
	text := "runtime image is stale: piccolo/picoclaw-runtime:aaaa, wants piccolo/picoclaw-runtime:bbbb (base image: alpine:3.22 -> alpine:3.23)"
	got := wrapIndent(text, 80, 8, 8)
	if !strings.Contains(got, "piccolo/picoclaw-runtime:aaaa") || !strings.Contains(got, "piccolo/picoclaw-runtime:bbbb") {
		t.Errorf("image references were split:\n%s", got)
	}
	for _, l := range strings.Split(got, "\n") {
		if len(l) > 80 {
			t.Errorf("line exceeds the width by %d:\n%s", len(l)-80, l)
		}
	}
	// Continuation lines hang at the same column as the first line's text.
	parts := strings.Split(got, "\n")
	if len(parts) < 2 {
		t.Fatalf("expected a wrap, got %q", got)
	}
	if !strings.HasPrefix(parts[1], strings.Repeat(" ", 8)) {
		t.Errorf("continuation not indented: %q", parts[1])
	}
	// A pipe gets no wrapping at all: one finding per line stays grep-able.
	if wrapIndent(text, 0, 8, 8) != text {
		t.Error("width 0 must not wrap")
	}
}

// The exit code is the script's signal, so the summary has to say the same thing
// in the same run: warnings count as "to look at", failures as "blocking".
func TestDoctorSummaryCountsMatchSeverity(t *testing.T) {
	cases := []struct {
		results []doctor.Result
		want    string
		absent  string
	}{
		{[]doctor.Result{{Status: doctor.OK, Section: doctor.SecWorkspace}}, "all 1 checks passed", "blocking"},
		{[]doctor.Result{{Status: doctor.WARN, Section: doctor.SecWorkspace}}, "1 to look at", "blocking"},
		{[]doctor.Result{{Status: doctor.FAIL, Section: doctor.SecWorkspace}, {Status: doctor.WARN, Section: doctor.SecWorkspace}}, "1 blocking, 1 to look at", "all"},
	}
	for _, tc := range cases {
		var out bytes.Buffer
		if err := renderDoctor(&out, styler{}, doctor.GroupBySection(tc.results), renderDoctorOptions{}); err != nil {
			t.Fatal(err)
		}
		got := out.String()
		if !strings.Contains(got, tc.want) {
			t.Errorf("summary must say %q:\n%s", tc.want, got)
		}
		if tc.absent != "" && strings.Contains(got, tc.absent) {
			t.Errorf("summary must not say %q:\n%s", tc.want, got)
		}
	}
}

// A structured report is the reason --json exists; a flat array of strings would
// make a script re-derive the grouping by guessing at message text.
func TestRenderDoctorJSONCarriesSectionsAndHints(t *testing.T) {
	results := doctor.GroupBySection([]doctor.Result{{
		Status: doctor.WARN, Section: doctor.SecCapabilities,
		Message: "mcp: tools.mcp.enabled is not true while server(s) {x} are registered",
		Hint:    "PicoClaw skips MCP initialization entirely",
	}})
	var out bytes.Buffer
	if err := renderDoctor(&out, styler{}, results, renderDoctorOptions{JSON: true}); err != nil {
		t.Fatal(err)
	}
	var report doctorReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("invalid json: %v\n%s", err, out.String())
	}
	if len(report.Sections) != 1 || report.Sections[0].Name != "capabilities" {
		t.Fatalf("sections = %+v", report.Sections)
	}
	e := report.Sections[0].Results[0]
	if e.Status != "warn" || !strings.Contains(e.Hint, "skips MCP initialization") {
		t.Errorf("entry = %+v", e)
	}
	if report.Summary.WARN != 1 {
		t.Errorf("summary = %+v", report.Summary)
	}
}

// A result that declared no section is a bug in a check, not a reason to drop a
// finding: it lands under a heading that admits it.
func TestRenderDoctorReportsUndeclaredSection(t *testing.T) {
	var out bytes.Buffer
	err := renderDoctor(&out, styler{}, []doctor.Result{{Status: doctor.FAIL, Message: "something broke"}}, renderDoctorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Other") || !strings.Contains(out.String(), "something broke") {
		t.Errorf("undeclared section must still be reported:\n%s", out.String())
	}
}
