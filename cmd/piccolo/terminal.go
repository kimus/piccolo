package main

import (
	"context"
	"os"
	"sync"
	"time"

	"github.com/kimus/piccolo/internal/docker"
	"golang.org/x/term"
)

// interactiveTerminal prepares the host terminal for an attached container PTY
// that runs a full-screen or key-driven program (integration setup wizards).
//
// Without raw mode the host line discipline keeps ECHO and canonical buffering,
// so arrow keys are printed as `^[[B` and only reach the container after Enter,
// which makes inquire/crossterm menus impossible to navigate.
type interactiveTerminal struct {
	fd       int
	oldState *term.State
	active   bool
}

// enterInteractiveTerminal enables raw mode when `active` and stdin really is a
// terminal. It is a no-op otherwise (piped input, CI, tests).
func enterInteractiveTerminal(active bool) (*interactiveTerminal, error) {
	fd := int(os.Stdin.Fd())
	if !active || !term.IsTerminal(fd) {
		return &interactiveTerminal{fd: fd, active: false}, nil
	}
	state, err := term.MakeRaw(fd)
	if err != nil {
		return nil, err
	}
	return &interactiveTerminal{fd: fd, oldState: state, active: true}, nil
}

// Restore puts the host terminal back the way it was. Safe to call twice.
func (t *interactiveTerminal) Restore() {
	if t == nil || !t.active || t.oldState == nil {
		return
	}
	_ = term.Restore(t.fd, t.oldState)
	t.oldState = nil
	t.active = false
}

// Size reports the host terminal window, or ok=false when unavailable.
func (t *interactiveTerminal) Size() (height, width uint, ok bool) {
	if t == nil || !t.active {
		return 0, 0, false
	}
	w, h, err := term.GetSize(t.fd)
	if err != nil || w <= 0 || h <= 0 {
		return 0, 0, false
	}
	return uint(h), uint(w), true
}

// syncPTYSize propagates the host window size into the container PTY until the
// returned stop func is called. Non-resizable processes (test fakes, non-TTY
// runs) are left alone.
func syncPTYSize(ctx context.Context, proc docker.Process, tty *interactiveTerminal) (stop func()) {
	resizable, ok := proc.(docker.Resizable)
	if !ok || tty == nil || !tty.active {
		return func() {}
	}
	done := make(chan struct{})
	var lastH, lastW uint
	apply := func() {
		h, w, ok := tty.Size()
		if !ok || (h == lastH && w == lastW) {
			return
		}
		if err := resizable.Resize(ctx, h, w); err == nil {
			lastH, lastW = h, w
		}
	}
	apply()
	go func() {
		ticker := time.NewTicker(300 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-done:
				return
			case <-ticker.C:
				apply()
			}
		}
	}()
	var once sync.Once
	return func() { once.Do(func() { close(done) }) }
}
