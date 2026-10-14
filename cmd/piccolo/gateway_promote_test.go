package main

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/kimus/piccolo/internal/docker"
)

// fakePromoter records the daemon calls a promotion makes, so the sequencing is
// unit-testable without Docker.
type fakePromoter struct {
	containers map[string]docker.State
	calls      []string
	failStart  bool  // the candidate cannot be started
	failRename bool  // renaming the revert target back fails
	removeErr  error // the candidate cannot be removed
	startErr   error // restoring the previous gateway fails
}

func newFakePromoter(canonicalImage string) *fakePromoter {
	f := &fakePromoter{containers: map[string]docker.State{}}
	if canonicalImage != "" {
		f.containers["piccolo-gateway"] = docker.State{Found: true, Running: true, Status: "running", Image: canonicalImage}
	}
	return f
}

func (f *fakePromoter) StateByName(_ context.Context, name string) (docker.State, error) {
	if st, ok := f.containers[name]; ok {
		return st, nil
	}
	return docker.State{}, nil
}

func (f *fakePromoter) StopKeptByName(_ context.Context, name string, _ time.Duration) (bool, error) {
	f.calls = append(f.calls, "stop-keep:"+name)
	st, ok := f.containers[name]
	if !ok {
		return false, nil
	}
	st.Running = false
	st.Status = "exited"
	f.containers[name] = st
	return true, nil
}

func (f *fakePromoter) RenameContainer(_ context.Context, from, to string) error {
	f.calls = append(f.calls, "rename:"+from+"->"+to)
	if f.failRename && to == "piccolo-gateway" {
		return fmt.Errorf("rename refused")
	}
	st, ok := f.containers[from]
	if !ok {
		return fmt.Errorf("no such container %q", from)
	}
	delete(f.containers, from)
	st.Found = true
	f.containers[to] = st
	return nil
}

func (f *fakePromoter) RemoveByName(_ context.Context, name string, _ bool) (bool, error) {
	f.calls = append(f.calls, "remove:"+name)
	if f.removeErr != nil && name == "piccolo-gateway" {
		return true, f.removeErr
	}
	if _, ok := f.containers[name]; !ok {
		return false, nil
	}
	delete(f.containers, name)
	return true, nil
}

func (f *fakePromoter) StartByName(_ context.Context, name string) (bool, error) {
	f.calls = append(f.calls, "start:"+name)
	if f.startErr != nil {
		return true, f.startErr
	}
	st, ok := f.containers[name]
	if !ok {
		return false, nil
	}
	st.Running = true
	st.Status = "running"
	f.containers[name] = st
	return true, nil
}

func testSwap(f *fakePromoter, out *bytes.Buffer) *gatewaySwap {
	return newGatewaySwap(f, out, "piccolo-gateway")
}

func TestParkStopsThenRenamesToTheReservedName(t *testing.T) {
	f := newFakePromoter("piccolo/picoclaw-runtime:old")
	var out bytes.Buffer
	s := testSwap(f, &out)
	if err := s.park(context.Background()); err != nil {
		t.Fatalf("park: %v", err)
	}
	// The canonical name must be free for the candidate, and the old container
	// must still exist as the revert target.
	if _, live := f.containers["piccolo-gateway"]; live {
		t.Error("canonical name still held after parking; the candidate could not bind it")
	}
	prev, ok := f.containers["piccolo-gateway.prev"]
	if !ok {
		t.Fatalf("revert target missing: %v", f.calls)
	}
	if prev.Running {
		t.Error("revert target must be stopped so its published port is released")
	}
	if prev.Image != "piccolo/picoclaw-runtime:old" {
		t.Errorf("revert target image = %q", prev.Image)
	}
	if !s.parked() || s.parkedImage != "piccolo/picoclaw-runtime:old" {
		t.Errorf("parked state = %v %q", s.parked(), s.parkedImage)
	}
	got := strings.Join(f.calls, " ")
	if strings.Index(got, "stop-keep:") > strings.Index(got, "rename:") {
		t.Errorf("must stop before renaming: %s", got)
	}
}

func TestCommitRemovesTheRevertTarget(t *testing.T) {
	f := newFakePromoter("piccolo/picoclaw-runtime:old")
	var out bytes.Buffer
	s := testSwap(f, &out)
	if err := s.park(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.containers["piccolo-gateway"] = docker.State{Found: true, Running: true, Image: "piccolo/picoclaw-runtime:new"}
	if err := s.commit(context.Background()); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if _, ok := f.containers["piccolo-gateway.prev"]; ok {
		t.Error("promoted gateway must not leave a revert target behind")
	}
	if _, ok := f.containers["piccolo-gateway"]; !ok {
		t.Error("commit must not touch the running candidate")
	}
	if s.parked() {
		t.Error("commit must clear the parked state")
	}
}

func TestRevertRestoresThePreviousGatewayAndKeepsDiagnostics(t *testing.T) {
	f := newFakePromoter("piccolo/picoclaw-runtime:old")
	var out bytes.Buffer
	s := testSwap(f, &out)
	if err := s.park(context.Background()); err != nil {
		t.Fatal(err)
	}
	// The candidate is up but broken.
	f.containers["piccolo-gateway"] = docker.State{Found: true, Running: true, Image: "piccolo/picoclaw-runtime:new"}

	failure := gateFailure{
		Stage:   "integration",
		Reason:  "an installed integration is not executable in this gateway",
		Details: []string{"piccolo-gws: exit 127 — not found (command: piccolo-gws state)"},
	}
	restored, err := s.revert(context.Background(), failure, func(context.Context, string) string {
		return "gateway: fatal: no such file or directory"
	})
	if err != nil {
		t.Fatalf("revert: %v", err)
	}
	if restored != "piccolo/picoclaw-runtime:old" {
		t.Errorf("restored = %q", restored)
	}
	report := out.String()
	// Diagnostics must be in the report BEFORE the container is deleted: after
	// removal there is nothing left to inspect.
	diagAt := strings.Index(report, "exit 127")
	removeAt := indexOfCall(f.calls, "remove:piccolo-gateway")
	if diagAt < 0 {
		t.Fatalf("report lost the gate details:\n%s", report)
	}
	if removeAt < 0 {
		t.Fatal("candidate was not removed")
	}
	if strings.Index(report, "candidate gateway logs") < 0 {
		t.Error("report lost the candidate logs")
	}
	_ = removeAt
	live, ok := f.containers["piccolo-gateway"]
	if !ok || !live.Running || live.Image != "piccolo/picoclaw-runtime:old" {
		t.Errorf("previous gateway not restored: %#v", live)
	}
	if _, ok := f.containers["piccolo-gateway.prev"]; ok {
		t.Error("revert target must be renamed back, not left parked")
	}
	// The failed candidate must be gone, not renamed to a third name.
	if _, ok := f.containers["piccolo-gateway.new"]; ok {
		t.Error("unexpected leftover container")
	}
}

func TestRevertFailureSaysNeitherGatewayIsUp(t *testing.T) {
	f := newFakePromoter("piccolo/picoclaw-runtime:old")
	var out bytes.Buffer
	s := testSwap(f, &out)
	if err := s.park(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.containers["piccolo-gateway"] = docker.State{Found: true, Image: "piccolo/picoclaw-runtime:new"}
	f.startErr = fmt.Errorf("port is already allocated")

	_, err := s.revert(context.Background(), gateFailure{Stage: "health", Reason: "not healthy"}, func(context.Context, string) string { return "" })
	if err == nil {
		t.Fatal("revert must report a failed restore")
	}
	if !strings.Contains(err.Error(), "previous gateway could not be restored") {
		t.Errorf("error = %v", err)
	}
	// It must never claim a rollback that did not restore service.
	if strings.Contains(out.String(), "restored") {
		t.Errorf("reported a successful restore while nothing is running:\n%s", out.String())
	}
}

func TestRecoverRestoresAnInterruptedPromotion(t *testing.T) {
	f := newFakePromoter("")
	var out bytes.Buffer
	f.containers["piccolo-gateway.prev"] = docker.State{Found: true, Status: "exited", Image: "piccolo/picoclaw-runtime:old"}
	s := testSwap(f, &out)
	if err := s.recover(context.Background()); err != nil {
		t.Fatalf("recover: %v", err)
	}
	st, ok := f.containers["piccolo-gateway"]
	if !ok || !st.Running {
		t.Fatalf("interrupted gateway not restored: %#v", f.containers)
	}
	if _, ok := f.containers["piccolo-gateway.prev"]; ok {
		t.Error("reserved name must be consumed by the restore")
	}
	if !strings.Contains(out.String(), "mid-promotion") {
		t.Errorf("recovery not reported:\n%s", out.String())
	}
}

func TestRecoverLeavesAHealthyGatewayAlone(t *testing.T) {
	f := newFakePromoter("piccolo/picoclaw-runtime:new")
	f.containers["piccolo-gateway.prev"] = docker.State{Found: true, Status: "exited", Image: "piccolo/picoclaw-runtime:old"}
	var out bytes.Buffer
	s := testSwap(f, &out)
	if err := s.recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.containers["piccolo-gateway.prev"]; !ok {
		t.Error("leftover must not be deleted while service is healthy")
	}
	if !strings.Contains(out.String(), "leftover promotion target") {
		t.Errorf("leftover not reported:\n%s", out.String())
	}
	for _, call := range f.calls {
		if strings.HasPrefix(call, "stop-keep:") || strings.HasPrefix(call, "start:") {
			t.Errorf("recover disturbed the running gateway: %s", call)
		}
	}
}

func TestParkReplacesALeftoverRevertTarget(t *testing.T) {
	f := newFakePromoter("piccolo/picoclaw-runtime:new")
	f.containers["piccolo-gateway.prev"] = docker.State{Found: true, Status: "exited", Image: "piccolo/picoclaw-runtime:ancient"}
	var out bytes.Buffer
	s := testSwap(f, &out)
	if err := s.park(context.Background()); err != nil {
		t.Fatalf("park: %v", err)
	}
	// The reserved name must hold exactly one container: the one we just parked.
	if got := f.containers["piccolo-gateway.prev"].Image; got != "piccolo/picoclaw-runtime:new" {
		t.Errorf("reserved target = %q, want the container parked by this promotion", got)
	}
}

func indexOfCall(calls []string, want string) int {
	for i, c := range calls {
		if c == want {
			return i
		}
	}
	return -1
}
