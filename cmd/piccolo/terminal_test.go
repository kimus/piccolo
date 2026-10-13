package main

import (
	"bytes"
	"context"
	"io"
	"testing"
	"time"

	"github.com/kimus/piccolo/internal/docker"
)

// resizeRecorder is a Process that also implements docker.Resizable.
type resizeRecorder struct {
	calls int
	tty   bool
}

func (r *resizeRecorder) Stdin() io.WriteCloser             { return nopWriteCloser{bytes.NewBuffer(nil)} }
func (r *resizeRecorder) Stdout() io.ReadCloser             { return io.NopCloser(bytes.NewReader(nil)) }
func (r *resizeRecorder) Stderr() io.ReadCloser             { return io.NopCloser(bytes.NewReader(nil)) }
func (r *resizeRecorder) Wait(context.Context) (int, error) { return 0, nil }
func (r *resizeRecorder) Shutdown(time.Duration) error      { return nil }
func (r *resizeRecorder) ExitCode() int                     { return 0 }
func (r *resizeRecorder) Resize(context.Context, uint, uint) error {
	r.calls++
	return nil
}

var _ docker.Resizable = (*resizeRecorder)(nil)

func TestInteractiveTerminalInactiveIsNoOp(t *testing.T) {
	tty, err := enterInteractiveTerminal(false)
	if err != nil {
		t.Fatalf("enter: %v", err)
	}
	if tty.active {
		t.Fatal("inactive request must not enable raw mode")
	}
	tty.Restore() // must not panic
	tty.Restore() // idempotent
	if _, _, ok := tty.Size(); ok {
		t.Fatal("inactive terminal must report no size")
	}
	// Inactive tty must never resize the container PTY.
	rec := &resizeRecorder{}
	stop := syncPTYSize(context.Background(), rec, tty)
	stop()
	if rec.calls != 0 {
		t.Fatalf("unexpected resize calls: %d", rec.calls)
	}
}

func TestSyncPTYSizeSkipsNonResizableProcess(t *testing.T) {
	tty, _ := enterInteractiveTerminal(false)
	stop := syncPTYSize(context.Background(), &fakeProcess{}, tty)
	if stop == nil {
		t.Fatal("stop func must be returned")
	}
	stop()
}
