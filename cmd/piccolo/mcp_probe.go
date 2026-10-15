package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/kimus/piccolo/internal/docker"
)

// mcpProbeBudget bounds one handshake attempt. A server that never answers must
// not turn `piccolo integration setup` into a hang.
const mcpProbeBudget = 30 * time.Second

// mcpProbeNamePrefix names probe containers under the integration one-shot prefix,
// so the reclaim sweep that already cleans abandoned action containers cleans
// these too. A probe that Piccolo died behind is therefore not a permanent leak.
const mcpProbeNamePrefix = docker.SetupContainerPrefix + "mcp-probe-"

// mcpProbeLineCap is the read limit for the server's first reply line. Framing
// headers and small JSON responses fit; a giant first line is itself the answer.
const mcpProbeLineCap = 1 << 14

// mcpHandshake asks an integration's MCP server to complete the initialize
// handshake and reports what came back. Piccolo consults it before claiming an
// integration is usable, and it is an interface so the decision is unit-testable
// without a daemon.
type mcpHandshake interface {
	// Probe runs argv in a throwaway container and returns a short description of
	// the server that answered (name/version), or an error naming why it did not.
	Probe(ctx context.Context, serverKey string, argv []string) (string, error)
}

// dockerMCPProbe runs the configured stdio server from the workspace's runtime
// image with the same mounts the gateway gives it, so the probe sees the same
// binary, the same integration state directory, and nothing the gateway happens to
// have in memory. The gateway itself is not required: setup runs actions in one-shot
// containers for the same reason.
// mcpProbeRunner is the daemon surface the probe needs: start a one-shot container
// and sweep abandoned probe containers. It is deliberately narrower than
// docker.Runner so the verification decision can be unit-tested without a daemon,
// the isolation rule the rest of Piccolo's container access already follows.
type mcpProbeRunner interface {
	Run(context.Context, docker.Spec) (docker.Process, error)
	RemoveByPrefix(context.Context, string) ([]string, error)
}

type dockerMCPProbe struct {
	runner mcpProbeRunner
	base   docker.Spec // image + mounts + ownership labels from the workspace
	out    io.Writer
}

func (p dockerMCPProbe) Probe(ctx context.Context, serverKey string, argv []string) (string, error) {
	if p.runner == nil {
		return "", fmt.Errorf("no container engine available")
	}
	if len(argv) == 0 {
		return "", fmt.Errorf("the server entry declares no command to run")
	}

	pctx, cancel := context.WithTimeout(ctx, mcpProbeBudget)
	defer cancel()

	bin, cmd := splitEntrypointCommand(argv)
	spec := p.base
	spec.Name = mcpProbeName(serverKey)
	spec.Entrypoint, spec.Command = bin, cmd
	// No published ports and no TTY: this is a pipe conversation, and a PTY would
	// merge stderr into the stdout stream the verdict is read from.
	spec.Ports = nil
	spec.TTY = false

	// An earlier crashed probe is replaced rather than blocked on the name, and the
	// prefix sweep reports what it dropped.
	if removed, err := p.runner.RemoveByPrefix(ctx, mcpProbeNamePrefix); err == nil && len(removed) > 0 && p.out != nil {
		fmt.Fprintf(p.out, "  (removed %d abandoned MCP probe container(s))\n", len(removed))
	}

	proc, err := p.runner.Run(pctx, spec)
	if err != nil {
		return "", fmt.Errorf("could not start the server for a handshake: %w", err)
	}
	// Shutdown runs before Probe returns, so a probe never leaves a container behind
	// on success, on refusal, or on timeout. The name also sits under the integration
	// one-shot prefix, which makes an abandoned probe (Piccolo killed mid-probe) a
	// number doctor already reports and the next run reclaims.
	defer func() { _ = proc.Shutdown(2 * time.Second) }()

	req, err := mcpInitializeRequest()
	if err != nil {
		return "", err
	}
	go func() {
		w := proc.Stdin()
		if w == nil {
			return
		}
		_, _ = w.Write([]byte(req))
		// Half-close: a well-behaved server answers and exits on EOF. Servers that
		// ignore stdin closing are caught by the budget, not waited on forever.
		_ = w.Close()
	}()

	type first struct {
		line string
		err  error
	}
	ch := make(chan first, 1)
	go func() {
		sc := bufio.NewScanner(proc.Stdout())
		sc.Buffer(make([]byte, 0, 1024), mcpProbeLineCap)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" {
				continue
			}
			ch <- first{line: line}
			return
		}
		if err := sc.Err(); err != nil {
			ch <- first{err: err}
			return
		}
		ch <- first{err: fmt.Errorf("the server closed its output without replying")}
	}()

	select {
	case got := <-ch:
		if got.err != nil {
			return "", got.err
		}
		return classifyMCPPrelude(got.line)
	case <-pctx.Done():
		return "", fmt.Errorf("no reply to the initialize request within %s", mcpProbeBudget)
	}
}

// mcpProbeName keeps one deterministic name per server key so an abandoned probe is
// always reclaimable by prefix.
func mcpProbeName(serverKey string) string {
	return mcpProbeNamePrefix + sanitizeForName(serverKey)
}

// mcpInitializeRequest is the MCP initialize call, as one newline-delimited JSON
// message: the stdio transport PicoClaw uses (github.com/modelcontextprotocol/
// go-sdk, mcp/transport.go) reads one message per line and understands no
// Content-Length framing.
func mcpInitializeRequest() (string, error) {
	payload := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "piccolo", "version": "probe"},
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encode initialize request: %w", err)
	}
	return string(body) + "\n", nil
}

// classifyMCPPrelude judges the first line the server wrote. Only a JSON-RPC
// response counts as a handshake; every other shape becomes the reason Piccolo
// withholds tools.mcp.enabled, phrased so the user can act on it.
func classifyMCPPrelude(line string) (string, error) {
	trimmed := strings.TrimSpace(line)
	lower := strings.ToLower(trimmed)

	if strings.HasPrefix(lower, "content-length:") {
		return "", fmt.Errorf("the server answered with LSP-style %q framing — MCP over stdio is newline-delimited JSON (one message per line), so PicoClaw cannot parse this reply; the integration's own reader accepts both framings, so this is its writer", "Content-Length:")
	}
	if !strings.HasPrefix(trimmed, "{") {
		return "", fmt.Errorf("the server's first reply was not JSON (%s)", quoteForLog(trimmed))
	}

	var resp struct {
		Result *struct {
			ServerInfo struct {
				Name    string `json:"name"`
				Version string `json:"version"`
			} `json:"serverInfo"`
		} `json:"result"`
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(trimmed), &resp); err != nil {
		return "", fmt.Errorf("the server replied with something that is not a JSON-RPC response: %v", err)
	}
	if resp.Error != nil {
		return "", fmt.Errorf("the server refused initialization: %s", resp.Error.Message)
	}
	if resp.Result == nil {
		return "", fmt.Errorf("the server replied without a result: %s", quoteForLog(trimmed))
	}
	name := strings.TrimSpace(resp.Result.ServerInfo.Name)
	if name == "" {
		return "answered initialize", nil
	}
	if v := strings.TrimSpace(resp.Result.ServerInfo.Version); v != "" {
		return fmt.Sprintf("%s %s", name, v), nil
	}
	return name, nil
}

// quoteForLog keeps a hostile or enormous first line to one short, quoted
// fragment: the probe's output is user-facing, and an unbounded echo of container
// stdout is how control characters reach a terminal.
func quoteForLog(s string) string {
	s = strings.ReplaceAll(s, "\r", "")
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	if len(s) > 120 {
		s = s[:120] + "…"
	}
	return fmt.Sprintf("%q", s)
}
