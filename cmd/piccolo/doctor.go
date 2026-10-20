package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/kimus/piccolo/internal/docker"
	"github.com/kimus/piccolo/internal/doctor"
	"github.com/kimus/piccolo/internal/workspace"
)

// markerWidth is the padded severity column, so every finding in the report starts
// at the same character and the eye compares findings instead of re-finding the
// sentence. Six cells fits both vocabularies: "[WARN]" is six characters, and the
// terminal form "⚠ warn" is six runes.
const (
	markerWidth = 6
	textIndent  = markerWidth + 1 // index the condition text starts at (marker + one space)
	hintIndent  = textIndent + 2  // remedy text starts one arrow past its condition
)

func newDoctorCmd() *cobra.Command {
	var (
		compact bool
		asJSON  bool
	)
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Validate Docker, workspace, and generated files",
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := workspace.Require(dirFlag)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()

			// Run already returns report order; the second GroupBySection here would
			// be a no-op, so the renderer takes what the checks produced.
			results := doctor.Run(ctx, doctor.Options{
				Workspace: dir,
				Engine:    docker.New(),
			})
			out := cmd.OutOrStdout()
			style := newStyler(out)
			if err := renderDoctor(out, style, results, renderDoctorOptions{
				Compact: compact,
				JSON:    asJSON,
				Width:   doctorWidth(out),
			}); err != nil {
				return err
			}
			if doctor.Failed(results) {
				cmd.SilenceErrors = true
				return fmt.Errorf("doctor found failures")
			}
			return nil
		},
	}
	// Grouping is a courtesy to the person reading a screen; a script wants the
	// same structure in a form it can index, which is why --json carries sections
	// instead of a flat array of strings.
	cmd.Flags().BoolVar(&compact, "compact", false, "show findings only; a passing section collapses to one line (ignored by --json, which carries every check as a field)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "machine-readable output, grouped by section")
	return cmd
}

// renderDoctorOptions controls the report's shape. Width 0 disables wrapping,
// which is what a pipe and a CI log want: a wrapped log line is worse than a long
// one, and it would make the same workspace report differently in a terminal and
// in a diff.
type renderDoctorOptions struct {
	Compact bool
	JSON    bool
	Width   int
}

// doctorWidth asks the terminal how wide it is. Any answer other than a usable
// width turns wrapping off — including a pipe, which is the case that matters:
// wrapping is a courtesy to a person reading a screen, and it must never change
// what a log file or a diff sees. NO_COLOR suppresses colour, not columns, so a
// terminal still wraps here even when the styler decided to paint nothing.
func doctorWidth(out io.Writer) int {
	f, ok := out.(*os.File)
	if !ok || !term.IsTerminal(int(f.Fd())) {
		return 0
	}
	w, _, err := term.GetSize(int(f.Fd()))
	if err != nil || w < 60 {
		return 0
	}
	return w
}

// doctorReport is the --json shape: one entry per section that produced a result,
// in report order.
type doctorReport struct {
	Sections []doctorSectionJSON `json:"sections"`
	Summary  doctorCounts        `json:"summary"`
}

type doctorSectionJSON struct {
	Name    string        `json:"name"`
	Title   string        `json:"title"`
	Results []doctorEntry `json:"results"`
}

type doctorEntry struct {
	Status  string `json:"status"`
	Message string `json:"message"`
	Hint    string `json:"hint,omitempty"`
}

type doctorCounts struct {
	OK   int `json:"ok"`
	WARN int `json:"warn"`
	FAIL int `json:"fail"`
}

func renderDoctor(out io.Writer, style styler, results []doctor.Result, opts renderDoctorOptions) error {
	counts := countDoctor(results)
	if opts.JSON {
		return renderDoctorJSON(out, results, counts)
	}

	sections := append(doctor.Sections(), doctor.Section(""))
	printed := false
	for _, sec := range sections {
		group := sectionResults(results, sec)
		if len(group) == 0 {
			// A section with no results is a set of checks that stayed deliberately
			// silent (env refs, a stale container, MCP with nothing installed). An
			// empty heading would advertise a problem where the code decided there
			// was none.
			continue
		}
		title := sec.Title() // "Other" for a result that declared no section
		problems := countProblems(group)
		// A blank line between sections, never above the first one: a report that
		// starts with an empty line looks like something went missing above it.
		if printed {
			fmt.Fprintln(out)
		}
		printed = true
		fmt.Fprintln(out, style.heading(title))
		if opts.Compact && problems == 0 {
			// A passing section keeps its heading: a compact report that dropped
			// headings would hide which checks ran at all, and "I never looked at your
			// credentials" reads worse than one quiet line admitting it passed.
			fmt.Fprintf(out, "%s%s\n", strings.Repeat(" ", textIndent), style.Mutedf("all %d checks passed", len(group)))
			continue
		}
		for _, r := range group {
			if opts.Compact && r.Status == doctor.OK {
				continue
			}
			writeDoctorLine(out, style, r, opts.Width)
		}
		if opts.Compact {
			if passed := len(group) - problems; passed > 0 {
				fmt.Fprintf(out, "%s%s\n", strings.Repeat(" ", textIndent), style.Mutedf("%d more checked, no issues", passed))
			}
		}
	}

	fmt.Fprintln(out)
	fmt.Fprintln(out, doctorSummaryLine(style, counts))
	return nil
}

// writeDoctorLine prints one finding: the severity marker, the condition, and the
// remedy on its own indented line. Conditions wrap at the terminal width with a
// hanging indent, because the messages that carry a verified upstream fact run to
// 250 characters and an unwrapped block is the reason people stop reading this
// command's output.
func writeDoctorLine(out io.Writer, style styler, r doctor.Result, width int) {
	fmt.Fprintf(out, "%s %s\n", style.doctorMark(r.Status.Word(), r.Status.Marker()),
		wrapIndent(r.Message, width, textIndent, textIndent))
	if r.Hint == "" {
		return
	}
	fmt.Fprintf(out, "%s→ %s\n", strings.Repeat(" ", textIndent),
		style.muted(wrapIndent(r.Hint, width, hintIndent, hintIndent)))
}

func countProblems(results []doctor.Result) int {
	n := 0
	for _, r := range results {
		if r.Status != doctor.OK {
			n++
		}
	}
	return n
}

// wrapIndent folds text to width, indenting the first line by first and every
// continuation line by rest. It breaks on spaces only: a path or an image tag
// split across lines stops being copyable, and copying the remedy is the whole
// point of putting it on screen.
func wrapIndent(text string, width, first, rest int) string {
	if width <= 0 {
		return text
	}
	limit := max(width-first, 20)
	words := strings.Fields(text)
	var lines []string
	var cur []string
	n := 0
	for _, w := range words {
		// Rune count, not bytes: the remedies say `→` and `⚠`, and measuring those
		// as three bytes each would wrap a line three columns early.
		add := utf8.RuneCountInString(w)
		if n > 0 {
			add++
		}
		if n > 0 && n+add > limit {
			lines = append(lines, strings.Join(cur, " "))
			cur, n = []string{w}, utf8.RuneCountInString(w)
			limit = max(width-rest, 20)
			continue
		}
		cur = append(cur, w)
		n += add
	}
	if len(cur) > 0 {
		lines = append(lines, strings.Join(cur, " "))
	}
	if len(lines) == 0 {
		return text
	}
	out := lines[0]
	for _, l := range lines[1:] {
		out += "\n" + strings.Repeat(" ", rest) + l
	}
	return out
}

func countDoctor(results []doctor.Result) doctorCounts {
	var c doctorCounts
	for _, r := range results {
		switch r.Status {
		case doctor.OK:
			c.OK++
		case doctor.WARN:
			c.WARN++
		default:
			c.FAIL++
		}
	}
	return c
}

// doctorSummaryLine answers the question the command was actually asked: is this
// workspace fine or not. Warnings are counted rather than characterized, because
// each one already carries its own remedy two lines above.
func doctorSummaryLine(style styler, c doctorCounts) string {
	switch {
	case c.FAIL > 0:
		return style.Failf("doctor: %d blocking, %d to look at, %d ok", c.FAIL, c.WARN, c.OK)
	case c.WARN > 0:
		return style.report("warn", fmt.Sprintf("doctor: %d to look at, %d ok", c.WARN, c.OK))
	default:
		return style.Okf("doctor: all %d checks passed", c.OK)
	}
}

func renderDoctorJSON(out io.Writer, results []doctor.Result, counts doctorCounts) error {
	report := doctorReport{Summary: counts}
	for _, sec := range append(doctor.Sections(), doctor.Section("")) {
		group := sectionResults(results, sec)
		if len(group) == 0 {
			continue
		}
		name := string(sec)
		if name == "" {
			name = "other"
		}
		entry := doctorSectionJSON{Name: name, Title: sec.Title()}
		for _, r := range group {
			entry.Results = append(entry.Results, doctorEntry{
				Status:  r.Status.Word(),
				Message: r.Message,
				Hint:    r.Hint,
			})
		}
		report.Sections = append(report.Sections, entry)
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(report)
}

func sectionResults(results []doctor.Result, sec doctor.Section) []doctor.Result {
	var out []doctor.Result
	for _, r := range results {
		if r.Section == sec {
			out = append(out, r)
		}
	}
	return out
}
