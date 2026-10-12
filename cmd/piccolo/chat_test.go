package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/kimus/piccolo/internal/generate"
)

// realBanner mimics the upstream picoclaw agent interactive output: colored
// ASCII art, the sentinel line, then the readline prompt.
const realBanner = "\x1b[1;38;2;62;93;185m██████╗ ██╗ ██████╗\x1b[0m\n" +
	"██╔══██╗██║\n" +
	"🦞 Interactive mode (Ctrl+C to exit)\n\n🦞 You: "

func TestBrandFilterReplacesBanner(t *testing.T) {
	var out bytes.Buffer
	f := newBrandFilter(&out, "Piccolo", chatHeader("Piccolo"))
	if _, err := f.Write([]byte(realBanner)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	_ = f.Close()

	got := out.String()
	if strings.Contains(got, "██████╗") {
		t.Error("upstream ASCII art must be swallowed")
	}
	if strings.Contains(got, "Interactive mode (Ctrl+C") {
		t.Error("upstream sentinel line must be swallowed")
	}
	if strings.Contains(got, lobsterLogo) {
		t.Error("lobster emoji must be replaced")
	}
	if !strings.Contains(got, "Piccolo — your assistant") {
		t.Errorf("our header missing:\n%s", got)
	}
	if !strings.HasSuffix(got, "Piccolo You: ") {
		t.Errorf("prompt must survive, rebranded:\n%q", got)
	}
}

func TestBrandFilterEmojiAcrossWrites(t *testing.T) {
	var out bytes.Buffer
	f := newBrandFilter(&out, "Ada", "")
	f.armed = false
	emoji := []byte(lobsterLogo)
	// Split the 4-byte emoji across two writes.
	f.Write(emoji[:2])
	f.Write(append(emoji[2:], []byte(" You: ")...))
	_ = f.Close()
	if got := out.String(); got != "Ada You: " {
		t.Errorf("got %q", got)
	}
}

func TestBrandFilterFallbackWithoutSentinel(t *testing.T) {
	var out bytes.Buffer
	f := newBrandFilter(&out, "Piccolo", "HEADER")
	f.maxBytes = 32
	// No sentinel: after maxBytes the filter degrades to pass-through.
	f.Write([]byte("some unexpected upstream output that is longer than maxBytes"))
	_ = f.Close()
	got := out.String()
	if !strings.Contains(got, "unexpected upstream output") {
		t.Errorf("content must pass through on fallback, got %q", got)
	}
	if strings.Contains(got, "HEADER") {
		t.Error("header must not be injected on fallback")
	}
}

func TestBrandFilterCloseFlushesPending(t *testing.T) {
	var out bytes.Buffer
	f := newBrandFilter(&out, "Piccolo", "")
	f.armed = false
	partial := []byte(lobsterLogo)[:2]
	f.Write(partial) // dangling partial emoji
	_ = f.Close()
	if got := out.Bytes(); !bytes.Equal(got, partial) {
		t.Errorf("pending bytes must be flushed verbatim, got %v", got)
	}
}

func TestBrandFilterSuppressesLogLines(t *testing.T) {
	var out bytes.Buffer
	f := newBrandFilter(&out, "Piccolo", "")
	f.armed = false
	// TTY raw-mode line endings (\r\n). A real session fragment: prompt,
	// two ERR log lines, the client error (kept), then a re-drawn prompt.
	stream := "Piccolo You: \r\n" +
		"17:39:57 ERR agent home/runner/work/picoclaw/picoclaw/pkg/agent/pipeline_llm.go:463 > LLM call failed error=\"API request failed\" agent_id=main\r\n" +
		"17:39:57 ERR events home/runner/work/picoclaw/picoclaw/pkg/agent/runtime_event_logger.go:184 > Runtime event: agent.error severity=error\r\n" +
		"Error: LLM call failed after retries\r\n" +
		"Piccolo You: "
	if _, err := f.Write([]byte(stream)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	_ = f.Close()

	got := out.String()
	if strings.Contains(got, "ERR agent") || strings.Contains(got, "ERR events") {
		t.Errorf("log lines must be suppressed, got:\n%s", got)
	}
	if !strings.Contains(got, "Error: LLM call failed after retries") {
		t.Errorf("client error output must survive, got:\n%s", got)
	}
	if strings.Count(got, "Piccolo You: ") != 2 {
		t.Errorf("both prompts must survive, got:\n%q", got)
	}
}

func TestBrandFilterHoldsPartialLogStart(t *testing.T) {
	var out bytes.Buffer
	f := newBrandFilter(&out, "Piccolo", "")
	f.armed = false
	// A log line arriving in two chunks must still be suppressed.
	f.Write([]byte("17:39:57 ERR agent something"))
	if out.Len() != 0 {
		t.Errorf("partial log line flushed early: %q", out.String())
	}
	f.Write([]byte(" failed\r\n"))
	_ = f.Close()
	if out.Len() != 0 {
		t.Errorf("completed log line must be suppressed, got %q", out.String())
	}

	// But a partial timestamp-looking tail flushes on Close.
	var out2 bytes.Buffer
	f2 := newBrandFilter(&out2, "Piccolo", "")
	f2.armed = false
	f2.Write([]byte("17:39"))
	_ = f2.Close()
	if out2.String() != "17:39" {
		t.Errorf("Close must flush held tail, got %q", out2.String())
	}
}

func TestOneShotFilterSwallowsBanner(t *testing.T) {
	var out bytes.Buffer
	f := newOneShotFilter(&out, "Piccolo")
	// Simulated picoclaw main() banner: color art ending with the
	// space-only tail line, then the one-shot response line.
	banner := "\r\n\x1b[1;38;2;62;93;185m██████╗ ██╗\n" +
		"\x1b[1;38;2;62;93;185m╚═╝     ╚═╝ \x1b[0m\r\n \x1b[0m\r\n"
	f.Write([]byte(banner))
	if out.Len() != 0 {
		t.Errorf("banner must be swallowed, got %q", out.String())
	}
	f.Write([]byte("\n🦞 Hello there!\n"))
	_ = f.Close()
	got := out.String()
	if strings.Contains(got, "██") || strings.Contains(got, "\x1b[") {
		t.Errorf("banner art leaked: %q", got)
	}
	if got != "\nPiccolo Hello there!\n" {
		t.Errorf("response = %q", got)
	}
}

func TestOneShotFilterBannerSplitAcrossWrites(t *testing.T) {
	var out bytes.Buffer
	f := newOneShotFilter(&out, "Ada")
	f.Write([]byte("\r\n██████╗ art\n╚═╝ \x1b[0m"))
	f.Write([]byte("\r\n \x1b[0m\r\n"))
	f.Write([]byte("🦞 hi\n"))
	_ = f.Close()
	if got := out.String(); got != "Ada hi\n" {
		t.Errorf("got %q", got)
	}
}

func TestOneShotFilterFallbackOnUnknownBanner(t *testing.T) {
	var out bytes.Buffer
	f := newOneShotFilter(&out, "Ada")
	f.maxBytes = 24
	f.Write([]byte("a banner shape we do not recognize at all"))
	_ = f.Close()
	if !strings.Contains(out.String(), "banner shape") {
		t.Errorf("fallback must pass content through, got %q", out.String())
	}
}

func TestBrandFilterSuppressesColorizedLogLines(t *testing.T) {
	var out bytes.Buffer
	f := newBrandFilter(&out, "Piccolo", "")
	f.armed = false
	// TTY mode: the upstream logger colorizes lines, so the timestamp is
	// preceded by ANSI codes.
	stream := "Piccolo You: \r\n" +
		"\x1b[31m17:51:23 ERR agent pipeline_llm.go:463 > LLM call failed\x1b[0m\r\n" +
		"\x1b[31m17:51:23 ERR events runtime_event_logger.go:184 > Runtime event: agent.error\x1b[0m\r\n" +
		"Error: LLM call failed after retries\r\n" +
		"Piccolo You: "
	if _, err := f.Write([]byte(stream)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	_ = f.Close()

	got := out.String()
	if strings.Contains(got, "ERR agent") || strings.Contains(got, "ERR events") {
		t.Errorf("colorized log lines must be suppressed, got:\n%s", got)
	}
	if !strings.Contains(got, "Error: LLM call failed after retries") {
		t.Errorf("client error output must survive, got:\n%s", got)
	}
	if strings.Count(got, "Piccolo You: ") != 2 {
		t.Errorf("both prompts must survive, got:\n%q", got)
	}
}

func TestBrandFilterHoldsColorizedPartialLogStart(t *testing.T) {
	var out bytes.Buffer
	f := newBrandFilter(&out, "Piccolo", "")
	f.armed = false
	// A colorized log line arriving in two chunks must still be suppressed.
	f.Write([]byte("\x1b[31m17:51:23 ERR agent something"))
	if out.Len() != 0 {
		t.Errorf("partial colorized log line flushed early: %q", out.String())
	}
	f.Write([]byte(" failed\x1b[0m\r\n"))
	_ = f.Close()
	if out.Len() != 0 {
		t.Errorf("completed colorized log line must be suppressed, got %q", out.String())
	}
}

func TestBrandFilterSuppressesLogLineWithReadlineArtifacts(t *testing.T) {
	var out bytes.Buffer
	f := newBrandFilter(&out, "Piccolo", "")
	f.armed = false
	// Exact shape captured from a live TTY session: readline redraw
	// artifacts and a cursor-position query precede the colorized log line
	// on the same line. The CPR must survive (remote readline blocks on it),
	// the log text must not.
	stream := "\x1b[31mIncorrect API key provided\x1b[0m\r\n" +
		" \x08\x1b[6n\x1b[90m17:54:37\x1b[0m \x1b[31mERR\x1b[0m \x1b[33mevents\x1b[0m \x1b[1mhome/runner/x.go:184\x1b[0m > Runtime event: agent.error severity=error\r\n" +
		"Piccolo You: "
	if _, err := f.Write([]byte(stream)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	_ = f.Close()

	got := out.String()
	if strings.Contains(got, "ERR") || strings.Contains(got, "Runtime event") {
		t.Errorf("log line must be suppressed, got:\n%q", got)
	}
	if !strings.Contains(got, "\x1b[6n") {
		t.Error("cursor-position query must be forwarded")
	}
	if !strings.Contains(got, "Incorrect API key provided") {
		t.Error("real error text must survive")
	}
	if !strings.HasSuffix(got, "Piccolo You: ") {
		t.Errorf("prompt must survive, got %q", got)
	}
}

func TestAssistantNameFromWorkspace(t *testing.T) {
	dir := t.TempDir()
	in := generate.Inputs{
		AssistantName: "Jarvis",
		OwnerName:     "Kimus",
		Purpose:       "personal",
		Mode:          generate.ModeTerminal,
		Model:         generate.ModelInput{Alias: "m", Provider: "openai", Model: "m", APIKey: "k"},
		WebSearch:     true,
	}
	plan, err := generate.BuildPlan(in)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if _, _, err := plan.Write(dir, nil); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if got := assistantName(dir); got != "Jarvis" {
		t.Errorf("assistantName = %q, want Jarvis", got)
	}
}

func TestAssistantNameFallback(t *testing.T) {
	if got := assistantName(t.TempDir()); got != "Piccolo" {
		t.Errorf("assistantName = %q, want fallback Piccolo", got)
	}
}
