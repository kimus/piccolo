package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

// Severity styling for Piccolo's own terminal output.
//
// Colour carries the distinction the setup loop now depends on: an integration
// that reports a failure did its job, and the world is not what the user wanted;
// a crash means Piccolo cannot trust anything. Those two must not look the same,
// and "report a problem, stay in the menu" is only honest if the screen does not
// read like a tool failure.
//
// Rules that keep this from becoming decoration:
//
//   - Never colour-only. Every styled line keeps its text marker (`[ok]`,
//     `[fail]`, `x`, `[WARN]`), so meaning survives NO_COLOR, TERM=dumb, a pipe,
//     tmux copy-paste, and a screenshot pasted into a bug report.
//   - Colour only when writing to a real terminal. Tests and redirected output get
//     plain text, which also keeps golden-output diffs readable.
type styler struct {
	enabled bool
}

// SGR codes rather than a theming library: three severity colours, bold and faint
// do not need a renderer that consults its own idea of the terminal. A lipgloss
// style applied while its default renderer sees a non-terminal produces plain
// text no matter what this styler decided, which would make `enabled` a lie and
// the behaviour untestable. Codes here are emitted if and only if enabled is set.
const (
	sgrReset  = "\x1b[0m"
	sgrBold   = "\x1b[1m"
	sgrFaint  = "\x1b[2m"
	sgrRed    = "\x1b[31m"
	sgrYellow = "\x1b[33m"
	sgrGreen  = "\x1b[32m"
)

// newStyler decides once whether ANSI is appropriate for this writer.
func newStyler(out io.Writer) styler {
	f, ok := out.(*os.File)
	if !ok || !term.IsTerminal(int(f.Fd())) {
		return styler{}
	}
	if strings.TrimSpace(os.Getenv("NO_COLOR")) != "" {
		return styler{}
	}
	if os.Getenv("TERM") == "dumb" {
		return styler{}
	}
	return styler{enabled: true}
}

func paint(enabled bool, code, text string) string {
	if !enabled || text == "" {
		return text
	}
	return code + text + sgrReset
}

// severity colours a status word that already appears in the line, so the text
// stays the source of truth and the colour is redundant emphasis.
func (s styler) severity(status, text string) string {
	var code string
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "ok", "pass":
		code = sgrGreen
	case "warn", "skip", "unknown":
		code = sgrYellow
	case "fail":
		code = sgrRed
	default:
		code = sgrFaint
	}
	return paint(s.enabled, code, text)
}

// report is Piccolo's own verdict on an action outcome: amber when the
// integration reported a failure, red when it died without saying anything.
func (s styler) report(kind, text string) string {
	switch kind {
	case "crash", "fault":
		return paint(s.enabled, sgrRed, text)
	case "failure", "warn":
		return paint(s.enabled, sgrYellow, text)
	case "ok":
		return paint(s.enabled, sgrGreen, text)
	default:
		return paint(s.enabled, sgrFaint, text)
	}
}

func (s styler) heading(text string) string { return paint(s.enabled, sgrBold, text) }
func (s styler) muted(text string) string   { return paint(s.enabled, sgrFaint, text) }

// printf helpers keep call sites readable while centralising the enable check.
func (s styler) Failf(format string, a ...any) string {
	return s.report("failure", fmt.Sprintf(format, a...))
}
func (s styler) Crashf(format string, a ...any) string {
	return s.report("crash", fmt.Sprintf(format, a...))
}
func (s styler) Okf(format string, a ...any) string    { return s.report("ok", fmt.Sprintf(format, a...)) }
func (s styler) Mutedf(format string, a ...any) string { return s.muted(fmt.Sprintf(format, a...)) }

// doctorStatus renders a doctor result prefix with the same vocabulary as the
// setup flow, so one problem looks like one thing wherever it is reported.
func (s styler) doctorStatus(marker string) string {
	switch marker {
	case "[OK]":
		return s.severity("ok", marker)
	case "[WARN]":
		return s.severity("warn", marker)
	case "[FAIL]":
		return s.severity("fail", marker)
	default:
		return marker
	}
}
