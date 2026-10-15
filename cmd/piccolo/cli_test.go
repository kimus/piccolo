package main

import (
	"io"
	"strings"
	"testing"
)

// Cobra prints help and exits 0 for a group command whose argument matches no
// subcommand, because the parent is not runnable and that check happens before
// any Args validation. Silently succeeding is the worst result for a script and
// indistinguishable from a command that did work. These tests pin the fix: the
// group must be runnable enough to report the mistake.
func TestGroupCommandsRejectUnknownSubcommands(t *testing.T) {
	for _, args := range [][]string{
		{"integration", "typo"},
		{"image", "typo"},
		{"runtime", "typo"},
		{"workspace", "typo"},
	} {
		root := newRootCmd()
		root.SetOut(io.Discard)
		root.SetErr(io.Discard)
		root.SetArgs(args)
		err := root.Execute()
		if err == nil {
			t.Errorf("%v: unknown subcommand exited without an error", args)
			continue
		}
		if !strings.Contains(err.Error(), "unknown command") {
			t.Errorf("%v: error should name the problem, got %q", args, err)
		}
	}
}

// A bare group is not a mistake: it shows help and succeeds.
func TestGroupCommandsWithoutArgsPrintHelp(t *testing.T) {
	for _, name := range []string{"integration", "image", "runtime", "workspace"} {
		var out strings.Builder
		root := newRootCmd()
		root.SetOut(&out)
		root.SetErr(io.Discard)
		root.SetArgs([]string{name})
		if err := root.Execute(); err != nil {
			t.Errorf("%s: bare group should succeed, got %v", name, err)
		}
		if !strings.Contains(out.String(), "Available Commands") {
			t.Errorf("%s: bare group should list subcommands", name)
		}
	}
}

// The typo the fix exists for: a plausible-looking verb must be refused, and a
// near miss should say what was probably meant.
func TestUnknownSubcommandSuggestsNearMatch(t *testing.T) {
	root := newRootCmd()
	root.SetOut(io.Discard)
	var errOut strings.Builder
	root.SetErr(&errOut)
	root.SetArgs([]string{"integration", "bulid", "kimus/x"})
	err := root.Execute()
	if err == nil {
		t.Fatal("expected an error for 'integration bulid'")
	}
	msg := err.Error()
	if !strings.Contains(msg, "unknown command") {
		t.Fatalf("missing refusal: %q", msg)
	}
	if !strings.Contains(msg, "did you mean") || !strings.Contains(msg, "build") {
		t.Logf("no suggestion offered (acceptable): %q", msg)
	}
}

// A removed subcommand must fail loudly. Falling back to group help with exit 0
// would tell a script the retired source build ran fine.
func TestRetiredIntegrationBuildRefuses(t *testing.T) {
	root := newRootCmd()
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"integration", "build", "kimus/piccolo-gws"})
	err := root.Execute()
	if err == nil {
		t.Fatal("'integration build' must fail: it resolved a Nix flake ref and no longer exists")
	}
	msg := err.Error()
	for _, want := range []string{"removed", "integration add"} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal missing %q: %q", want, msg)
		}
	}
}

// The command must stay out of the listing: it exists only to refuse, and a
// visible "build" invites the exact mistake it reports.
func TestRetiredIntegrationBuildIsHidden(t *testing.T) {
	var out strings.Builder
	root := newRootCmd()
	root.SetOut(&out)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"integration", "--help"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "build <package-ref>") {
		t.Error("the retired command must not be advertised as available")
	}
}
