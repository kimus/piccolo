package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/kimus/piccolo/internal/docker"
	"github.com/kimus/piccolo/internal/picoclaw"
)

// lobsterLogo is the PicoClaw client emoji we rebrand to the assistant name.
const lobsterLogo = "🦞"

// bannerSentinel marks the end of the upstream client banner in interactive
// mode; everything up to and including it is replaced by our own header.
const bannerSentinel = "Interactive mode (Ctrl+C to exit)"

func newChatCmd() *cobra.Command {
	var message string
	cmd := &cobra.Command{
		Use:   "chat",
		Short: "Talk to the assistant in the terminal",
		Long: "Runs PicoClaw's interactive agent client inside the gateway " +
			"container (docker exec picoclaw agent), rebranded with your " +
			"assistant's name. With -m, sends a single message and prints the answer.",
		RunE: func(cmd *cobra.Command, args []string) error {
			name, dir, runner, ctx, err := containerContext(cmd)
			if err != nil {
				return err
			}
			state, err := runner.StateByName(ctx, name)
			if err != nil {
				return err
			}
			if !state.Found || !state.Running {
				return fmt.Errorf("gateway container %q is not running — start it with 'piccolo serve'", name)
			}
			assistant := assistantName(dir)
			if message != "" {
				return runChatOneShot(ctx, runner, name, message, assistant, cmd)
			}
			return runChatInteractive(ctx, runner, name, assistant)
		},
	}
	cmd.Flags().StringVarP(&message, "message", "m", "", "send a single message (non-interactive)")
	return cmd
}

// assistantName reads the default agent's name from its AGENT.md frontmatter.
// Falls back to "Piccolo" when it cannot be determined.
func assistantName(dir string) string {
	const fallback = "Piccolo"
	data, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		return fallback
	}
	var cfg picoclaw.Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return fallback
	}
	ws := cfg.Agents.Defaults.Workspace
	for _, a := range cfg.Agents.List {
		if a.Default {
			if a.Workspace != "" {
				ws = a.Workspace
			}
			break
		}
	}
	for _, prefix := range []string{"~/.picoclaw/", "/root/.picoclaw/"} {
		if rest, ok := strings.CutPrefix(ws, prefix); ok {
			ws = filepath.Join(dir, filepath.FromSlash(rest))
			break
		}
	}
	md, err := os.ReadFile(filepath.Join(ws, "AGENT.md"))
	if err != nil {
		return fallback
	}
	body := string(md)
	if !strings.HasPrefix(body, "---\n") {
		return fallback
	}
	rest := body[4:]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return fallback
	}
	for _, line := range strings.Split(rest[:end], "\n") {
		if name, ok := strings.CutPrefix(line, "name:"); ok {
			if name = strings.TrimSpace(name); name != "" {
				return name
			}
		}
	}
	return fallback
}

// bannerTailLine detection: both the color and plain picoclaw banners end
// with a line whose visible content is a single space (verified in
// cmd/picoclaw/main.go: " \033[0m\r\n" / " \r\n").
var ansiSeq = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func isBannerTailLine(line []byte) bool {
	clean := ansiSeq.ReplaceAll(bytes.TrimRight(line, "\r\n"), nil)
	return string(clean) == " "
}

// brandFilter rewrites the upstream PicoClaw client chrome:
//   - interactive mode: everything up to (and including) the banner sentinel
//     is swallowed and replaced by our own header;
//   - one-shot mode: the leading picoclaw banner (printed unconditionally by
//     its main()) is swallowed up to its terminating space-only line;
//   - all output afterwards: the 🦞 logo emoji is replaced by the assistant
//     name (handles the emoji being split across writes), and PicoClaw
//     client log lines are suppressed.
//
// If the sentinel never appears within maxBytes, the filter degrades
// to plain emoji replacement so an upstream banner change can never break
// the stream.
type brandFilter struct {
	w             io.Writer
	name          string
	header        string // our replacement banner, written once
	buf           []byte // pending banner-phase bytes
	armed         bool   // interactive: still looking for the sentinel
	swallowBanner bool   // one-shot: still looking for the banner tail line
	carry         []byte // trailing bytes that may be a partial emoji
	lineBuf       []byte // trailing incomplete line for log suppression
	maxBytes      int
}

func newBrandFilter(w io.Writer, name, header string) *brandFilter {
	return &brandFilter{w: w, name: name, header: header, armed: true, maxBytes: 8192}
}

// newOneShotFilter creates a filter for `picoclaw agent -m` output: swallow
// the leading banner, rebrand the logo, suppress log lines.
func newOneShotFilter(w io.Writer, name string) *brandFilter {
	return &brandFilter{w: w, name: name, swallowBanner: true, maxBytes: 8192}
}

func (f *brandFilter) Write(p []byte) (int, error) {
	n := len(p)
	if f.armed {
		f.buf = append(f.buf, p...)
		if i := bytes.Index(f.buf, []byte(bannerSentinel)); i >= 0 {
			f.armed = false
			// Drop the upstream banner (ASCII art + sentinel line).
			rest := f.buf[i+len(bannerSentinel):]
			rest = bytes.TrimLeft(rest, "\r\n")
			f.buf = nil
			if _, err := io.WriteString(f.w, f.header); err != nil {
				return n, err
			}
			return n, f.writeReplaced(rest)
		}
		if len(f.buf) > f.maxBytes {
			// Sentinel not found — pass everything through, rebranded.
			f.armed = false
			out := f.buf
			f.buf = nil
			return n, f.writeReplaced(out)
		}
		return n, nil
	}
	if f.swallowBanner {
		f.buf = append(f.buf, p...)
		if i := bannerEndIndex(f.buf); i >= 0 {
			f.swallowBanner = false
			rest := f.buf[i:]
			f.buf = nil
			return n, f.writeReplaced(rest)
		}
		if len(f.buf) > f.maxBytes {
			f.swallowBanner = false
			out := f.buf
			f.buf = nil
			return n, f.writeReplaced(out)
		}
		return n, nil
	}
	return n, f.writeReplaced(p)
}

// bannerEndIndex returns the offset just past the banner's terminating
// space-only line, or -1 while the banner is still incomplete.
func bannerEndIndex(buf []byte) int {
	pos := 0
	for {
		i := bytes.IndexByte(buf[pos:], '\n')
		if i < 0 {
			return -1
		}
		end := pos + i + 1
		if isBannerTailLine(buf[pos:end]) {
			return end
		}
		pos = end
	}
}

// Close flushes any buffered bytes (banner phase never resolved), a
// trailing partial emoji, and an incomplete final line.
func (f *brandFilter) Close() error {
	if (f.armed || f.swallowBanner) && len(f.buf) > 0 {
		out := f.buf
		f.armed, f.swallowBanner, f.buf = false, false, nil
		if err := f.writeReplaced(out); err != nil {
			return err
		}
	}
	if len(f.carry) > 0 {
		out := f.carry
		f.carry = nil
		if err := f.writeLines(out); err != nil {
			return err
		}
	}
	if len(f.lineBuf) > 0 {
		out := f.lineBuf
		f.lineBuf = nil
		if !logLinePrefix.Match(out) {
			if _, err := f.w.Write(out); err != nil {
				return err
			}
		}
	}
	return nil
}

// writeReplaced swaps the lobster emoji for the assistant name, holding back
// a possible partial emoji at the end of the chunk, then routes the result
// through log-line suppression.
func (f *brandFilter) writeReplaced(p []byte) error {
	data := append(f.carry, p...)
	f.carry = nil
	if k := partialEmojiTail(data); k > 0 {
		f.carry = append(f.carry, data[len(data)-k:]...)
		data = data[:len(data)-k]
	}
	data = bytes.ReplaceAll(data, []byte(lobsterLogo), []byte(f.name))
	if len(data) == 0 {
		return nil
	}
	return f.writeLines(data)
}

// logLinePrefix matches PicoClaw client log lines, e.g.
// "17:39:57 ERR agent home/runner/...". These are runtime diagnostics, not
// conversation: they are suppressed in chat (full detail stays available
// via 'piccolo logs').
var logLinePrefix = regexp.MustCompile(`^\d{2}:\d{2}:\d{2}\s+(ERR|WRN|INF|DBG|FTL|TRC)\b`)

// partialLogStart matches an incomplete line tail that may still grow into
// a log line (a bare timestamp prefix), so it can be held back instead of
// flushed. Anything else (notably the interactive prompt) flushes at once.
var partialLogStart = regexp.MustCompile(`^[\d:]{1,8}$|^\d{2}:\d{2}:\d{2}\b`)

// writeLines suppresses complete log lines while flushing everything else
// immediately. A trailing incomplete line is held only when it looks like
// the beginning of a log line.
func (f *brandFilter) writeLines(data []byte) error {
	f.lineBuf = append(f.lineBuf, data...)
	for {
		i := bytes.IndexByte(f.lineBuf, '\n')
		if i < 0 {
			break
		}
		line := f.lineBuf[:i+1]
		f.lineBuf = append([]byte(nil), f.lineBuf[i+1:]...)
		if !logLinePrefix.Match(bytes.TrimRight(line, "\r\n")) {
			if _, err := f.w.Write(line); err != nil {
				return err
			}
		}
	}
	if len(f.lineBuf) > 0 && !partialLogStart.Match(f.lineBuf) {
		out := f.lineBuf
		f.lineBuf = nil
		if _, err := f.w.Write(out); err != nil {
			return err
		}
	}
	return nil
}

// partialEmojiTail returns how many trailing bytes of data form a prefix of
// the lobster emoji (0 when the tail is complete/irrelevant).
func partialEmojiTail(data []byte) int {
	emoji := []byte(lobsterLogo)
	max := len(emoji) - 1
	if max > len(data) {
		max = len(data)
	}
	for k := max; k > 0; k-- {
		if bytes.HasSuffix(data, emoji[:k]) {
			return k
		}
	}
	return 0
}

// chatHeader renders our replacement banner for interactive mode.
func chatHeader(name string) string {
	var b strings.Builder
	b.WriteString("\r\n")
	b.WriteString(fmt.Sprintf("  %s — your assistant (running on PicoClaw)\r\n", name))
	b.WriteString("  Type your message; 'exit' or Ctrl-C to quit.\r\n")
	b.WriteString("\r\n")
	return b.String()
}

// runChatOneShot executes `picoclaw agent -m <msg>` without a TTY and demuxes
// the output to stdout/stderr, rebranded with the assistant name.
func runChatOneShot(ctx context.Context, runner *docker.Runner, name, message, assistant string, cmd *cobra.Command) error {
	session, err := runner.Exec(ctx, name, []string{"picoclaw", "agent", "-m", message}, false)
	if err != nil {
		return err
	}
	defer session.Close()

	filter := newOneShotFilter(cmd.OutOrStdout(), assistant)
	demuxDone := make(chan error, 1)
	go func() { demuxDone <- docker.Demux(session.Reader, filter, cmd.ErrOrStderr()) }()

	code, waitErr := session.Wait(ctx)
	<-demuxDone
	_ = filter.Close()
	if waitErr != nil {
		return waitErr
	}
	if code != 0 {
		cmd.SilenceErrors = true
		os.Exit(code)
	}
	return nil
}

// runChatInteractive executes `picoclaw agent` with a TTY: the local terminal
// goes raw and bytes flow both ways until the remote client exits. The
// upstream banner is replaced by our own and the prompt shows the assistant
// name instead of the lobster logo.
func runChatInteractive(ctx context.Context, runner *docker.Runner, name, assistant string) error {
	stdinFd := int(os.Stdin.Fd())
	if !term.IsTerminal(stdinFd) {
		return fmt.Errorf("interactive chat needs a TTY (use -m \"message\" for one-shot)")
	}

	session, err := runner.Exec(ctx, name, []string{"picoclaw", "agent"}, true)
	if err != nil {
		return err
	}
	defer session.Close()

	oldState, err := term.MakeRaw(stdinFd)
	if err != nil {
		return fmt.Errorf("switch terminal to raw mode: %w", err)
	}
	defer term.Restore(stdinFd, oldState)

	sigCtx, stopSignals := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer stopSignals()

	inDone := make(chan struct{})
	go func() {
		defer close(inDone)
		_, _ = io.Copy(session.Stdin(), os.Stdin)
		_ = session.Stdin().Close()
	}()

	filter := newBrandFilter(os.Stdout, assistant, chatHeader(assistant))
	outDone := make(chan struct{})
	go func() {
		defer close(outDone)
		_, _ = io.Copy(filter, session.Reader)
		_ = filter.Close()
	}()

	waitCtx, cancelWait := context.WithCancel(ctx)
	defer cancelWait()
	code := 0
	waitDone := make(chan struct{})
	go func() {
		defer close(waitDone)
		if c, err := session.Wait(waitCtx); err == nil {
			code = c
		}
	}()

	select {
	case <-outDone:
	case <-sigCtx.Done():
		_ = session.Stdin().Close()
		select {
		case <-outDone:
		case <-time.After(5 * time.Second):
		}
	}
	cancelWait()
	<-waitDone
	term.Restore(stdinFd, oldState)
	fmt.Println()
	if code != 0 {
		os.Exit(code)
	}
	return nil
}

// signalContext returns a context cancelled on SIGINT/SIGTERM plus its stop func.
func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

// probeHealth GETs the health URL and returns a short status plus an optional
// one-line body summary.
func probeHealth(ctx context.Context, url string) (string, string) {
	client := &http.Client{Timeout: 3 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "unreachable", ""
	}
	resp, err := client.Do(req)
	if err != nil {
		return "unreachable", strings.TrimSpace(err.Error())
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	return resp.Status, strings.TrimSpace(string(body))
}
