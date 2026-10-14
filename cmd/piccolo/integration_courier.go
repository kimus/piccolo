package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"runtime"
	"strings"
	"time"
)

// courier is Piccolo's loopback relay for a browser OAuth redirect.
//
// The integration listens on the *container's* loopback on an OS-assigned
// ephemeral port, while the browser lives on the host, so neither side can
// reach the other. Piccolo bridges them: it binds the port taken from the
// event URL's own redirect_uri, captures exactly one request, and hands the
// raw query string back so the integration can deliver it into its own
// container. Piccolo never interprets, logs, or stores the credential material.
type courier struct {
	port    int
	ln      net.Listener
	srv     *http.Server
	queryCh chan string
	errCh   chan error
}

// relayFunc pushes the captured request line into the still-running action
// container (normally a docker exec of the integration's own loopback).
type relayFunc func(ctx context.Context, rawQuery string) error

// newCourier binds a loopback listener on the port derived from the event URL.
// A bind failure is reported as a conflict so the caller can retry the action
// and obtain a fresh ephemeral port, rather than silently choosing another.
func newCourier(port int) (*courier, error) {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return nil, fmt.Errorf("callback port %d is already in use on this host: %w (retry the action to obtain a fresh port)", port, err)
	}
	return newCourierOn(ln, port)
}

// newCourierOn adopts an already-bound loopback listener (a seam for tests).
func newCourierOn(ln net.Listener, port int) (*courier, error) {
	if ln == nil {
		return nil, fmt.Errorf("courier requires a listener")
	}
	if tcp, ok := ln.Addr().(*net.TCPAddr); ok && !tcp.IP.IsLoopback() {
		_ = ln.Close()
		return nil, fmt.Errorf("courier listener must be loopback, got %s", tcp.IP)
	}
	c := &courier{
		port:    port,
		ln:      ln,
		queryCh: make(chan string, 1),
		errCh:   make(chan error, 1),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// The code must be delivered by the user's browser redirect; serving a
		// plain completion page and closing after one hit keeps the surface
		// single-use, exactly like the integration's own loopback server.
		select {
		case c.queryCh <- r.URL.RawQuery:
		default:
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "Piccolo received the authorization response. You may close this window.\n")
	})
	c.srv = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := c.srv.Serve(c.ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			c.errCh <- err
		}
	}()
	return c, nil
}

// await blocks for the single callback, a courier error, ctx cancellation, or
// the timeout. On a captured redirect it invokes relay with the raw query.
func (c *courier) await(ctx context.Context, timeout time.Duration, relay relayFunc) error {
	defer c.close()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case q := <-c.queryCh:
		return relay(ctx, q)
	case err := <-c.errCh:
		return fmt.Errorf("callback listener failed: %w", err)
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return fmt.Errorf("timed out after %s waiting for the browser authorization to complete", timeout)
	}
}

func (c *courier) close() {
	if c.srv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = c.srv.Shutdown(ctx)
	}
	if c.ln != nil {
		_ = c.ln.Close()
	}
}

// callbackURL renders the host-side listener address for user guidance.
func (c *courier) callbackAddr() string {
	return fmt.Sprintf("http://localhost:%d", c.port)
}

// hostSession describes where Piccolo is running relative to a usable browser,
// which decides whether open_url may launch anything at all.
type hostSession struct {
	tty    bool
	ssh    bool
	linux  bool
	darwin bool
}

// detectHostSession is deliberately conservative: over SSH or without a
// terminal Piccolo prints the URL instead of trying to open a browser that
// cannot exist on the remote user's machine.
func detectHostSession(env map[string]string, stdinTTY bool) hostSession {
	pick := func(k string) string { return strings.TrimSpace(env[k]) }
	h := hostSession{tty: stdinTTY}
	if pick("SSH_CONNECTION") != "" || pick("SSH_CLIENT") != "" || pick("SSH_TTY") != "" {
		h.ssh = true
	}
	switch runtime.GOOS {
	case "linux":
		h.linux = true
	case "darwin":
		h.darwin = true
	}
	// A headless Linux box (no display server) cannot open a browser either.
	if h.linux && pick("DISPLAY") == "" && pick("WAYLAND_DISPLAY") == "" {
		h.linux = false
	}
	return h
}

// shouldLaunchBrowser reports whether attempting to open a browser is sane.
func (h hostSession) shouldLaunchBrowser() bool {
	if !h.tty || h.ssh {
		return false
	}
	return h.darwin || h.linux
}

// browserHint explains, in one line, what Piccolo did or will not do with a
// URL, so a remote user is never left guessing.
func (h hostSession) browserHint(url string) string {
	switch {
	case h.ssh:
		return fmt.Sprintf("Remote session: open this URL in a browser on your local machine.\n  %s\n  If the callback must reach this host, forward it first: ssh -L <port>:localhost:<port> <this-host>", url)
	case !h.tty:
		return fmt.Sprintf("Not attached to a terminal; open this URL manually.\n  %s", url)
	default:
		return fmt.Sprintf("Opening in your browser. If nothing appears, open it manually.\n  %s", url)
	}
}
