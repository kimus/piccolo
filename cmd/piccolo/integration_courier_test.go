package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"runtime"
	"strings"
	"testing"
	"time"
)

// newTestCourier binds an OS-assigned loopback port so the test never races
// with something else on the developer's machine.
func newTestCourier(t *testing.T) (*courier, int) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	c, err := newCourierOn(ln, ln.Addr().(*net.TCPAddr).Port)
	if err != nil {
		t.Fatal(err)
	}
	return c, ln.Addr().(*net.TCPAddr).Port
}

func TestCourierRelaysRawQueryVerbatim(t *testing.T) {
	c, port := newTestCourier(t)
	query := "code=4%2F0Asecret-token&state=xyz&scope=a%20b"
	done := make(chan error, 1)
	go func() {
		done <- c.await(context.Background(), 5*time.Second, func(_ context.Context, got string) error {
			if got != query {
				return fmt.Errorf("query mangled: %q != %q", got, query)
			}
			return nil
		})
	}()
	time.Sleep(60 * time.Millisecond)
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/?%s", port, query))
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if !strings.Contains(string(body), "Piccolo received") {
		t.Fatalf("completion page = %q", body)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("await: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("relay never fired")
	}
}

func TestCourierTimesOutWithoutCallback(t *testing.T) {
	c, _ := newTestCourier(t)
	err := c.await(context.Background(), 120*time.Millisecond, func(context.Context, string) error {
		t.Error("relay must not run on timeout")
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expected timeout, got %v", err)
	}
}

func TestCourierCancelledByContext(t *testing.T) {
	c, _ := newTestCourier(t)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(80 * time.Millisecond)
		cancel()
	}()
	err := c.await(ctx, 5*time.Second, func(context.Context, string) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "context canceled") {
		t.Fatalf("expected cancellation, got %v", err)
	}
}

func TestCourierReleasesPortAfterUse(t *testing.T) {
	c, port := newTestCourier(t)
	_ = c.await(context.Background(), 80*time.Millisecond, func(context.Context, string) error { return nil })
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatalf("port %d still bound after courier closed: %v", port, err)
	}
	_ = ln.Close()
}

func TestCourierRefusesNonLoopbackListener(t *testing.T) {
	ln, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := newCourierOn(ln, 0); err == nil {
		t.Fatal("a non-loopback listener must be refused outright")
	}
}

func TestCallbackPortIsCheckedBeforeAnyBind(t *testing.T) {
	// A non-loopback redirect must be rejected before Piccolo binds anything.
	if _, err := callbackPortFromURL("https://accounts.google.com/o/oauth2/auth?redirect_uri=https%3A%2F%2Fattacker.example%2Fcb"); err == nil {
		t.Fatal("expected rejection before any bind")
	}
}

func TestDetectHostSessionPolicy(t *testing.T) {
	localGUI := runtime.GOOS == "darwin" || runtime.GOOS == "linux"
	cases := []struct {
		name string
		env  map[string]string
		tty  bool
		want bool
	}{
		{"ssh session never opens", map[string]string{"SSH_CONNECTION": "1.2.3.4 1 x y", "DISPLAY": ":0"}, true, false},
		{"no terminal never opens", map[string]string{"DISPLAY": ":0"}, false, false},
		{"local tty with display opens", map[string]string{"DISPLAY": ":0"}, true, localGUI},
		{"headless linux has no browser", map[string]string{}, true, runtime.GOOS == "darwin"},
	}
	for _, c := range cases {
		h := detectHostSession(c.env, c.tty)
		if got := h.shouldLaunchBrowser(); got != c.want {
			t.Fatalf("%s: shouldLaunchBrowser = %v, want %v (%+v)", c.name, got, c.want, h)
		}
	}
}

func TestBrowserHintForSSHExplainsForwarding(t *testing.T) {
	h := detectHostSession(map[string]string{"SSH_CLIENT": "1.2.3.4 5 6"}, true)
	if !h.ssh {
		t.Fatal("expected ssh detection")
	}
	got := h.browserHint("https://accounts.google.com/o/oauth2/auth?x=1")
	if !strings.Contains(got, "ssh -L") || !strings.Contains(got, "local machine") {
		t.Fatalf("hint must explain both the URL and the tunnel: %q", got)
	}
}

func TestBrowserHintWithoutTerminal(t *testing.T) {
	h := detectHostSession(map[string]string{}, false)
	if got := h.browserHint("https://x/y"); !strings.Contains(got, "manually") {
		t.Fatalf("hint = %q", got)
	}
}
