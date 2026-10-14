//go:build integration

// Cross-repo contract test: it drives the REAL integration binary inside the
// built runtime image using exactly the argv Piccolo constructs.
//
// Why this exists: unit tests inject a fake runner and fake fetchers, so both
// sides can stay self-consistent while disagreeing about the CLI. That is how
// `call` shipped with its flag before the subcommand
// (piccolo-gws: "unexpected argument '--config-dir' found") with the whole
// suite green. This test runs the actual parser.
//
// Usage (skipped unless the image is supplied, so the default daemon-free
// `go test ./...` and CI are unaffected):
//
//	PICCOLO_CONTRACT_IMAGE=$(grep -m1 'image:' .data/piccolo.yaml | sed 's/.*image: //') \
//	  go test -count=1 -timeout 300s -tags integration ./cmd/piccolo/
//
// Every container gets a hard deadline and is always shut down: an interactive
// action (browser login) must never be able to hang the suite.
package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kimus/piccolo/internal/docker"
	picintegration "github.com/kimus/piccolo/internal/integration"
)

// actionDeadline bounds a single container run. Actions that legitimately wait
// for a human (browser authorization) are expected to hit it; that still proves
// the argv parsed, which is the contract under test.
const actionDeadline = 25 * time.Second

// clapRejections are the fingerprints of the integration binary refusing
// Piccolo's argv outright, as opposed to an action failing at runtime.
var clapRejections = []string{
	"unexpected argument",
	"unrecognized subcommand",
	"Usage: piccolo-gws",
	"test-runner-integration-contract: no integration binary on PATH",
}

func TestIntegrationCLIAcceptsPiccoloDerivedArgv(t *testing.T) {
	image := strings.TrimSpace(os.Getenv("PICCOLO_CONTRACT_IMAGE"))
	if image == "" {
		t.Skip("set PICCOLO_CONTRACT_IMAGE to a piccolo runtime image with an installed integration")
	}

	runner := docker.New()
	ctx := context.Background()
	if err := runner.Ping(ctx); err != nil {
		t.Fatalf("docker daemon not reachable for contract test: %v", err)
	}

	// Mount a fresh host dir at the same container path Piccolo uses, so the
	// derived --config-dir value is the real one and no leftover state can make
	// a gated action proceed to a browser wait.
	stateHost := filepath.Join(t.TempDir(), "integrations")
	if err := os.MkdirAll(stateHost, 0o700); err != nil {
		t.Fatal(err)
	}
	const containerState = "/root/.piccolo/integrations"

	// entry MUST be explicit: leaving it nil runs the image's default entrypoint,
	// i.e. boots the PicoClaw gateway, which never exits.
	run := func(stdin string, entry string, args ...string) (string, string, error) {
		runCtx, cancel := context.WithTimeout(ctx, actionDeadline)
		defer cancel()

		proc, err := runner.Run(runCtx, docker.Spec{
			Image:      image,
			Network:    "none",
			Source:     stateHost,
			Volumes:    []string{stateHost + ":" + containerState + ":rw"},
			Entrypoint: []string{entry},
			Command:    args,
		})
		if err != nil {
			return "", "", err
		}
		defer func() {
			// Detached from runCtx, which by now may be expired: cleanup must still
			// happen so a timed-out action cannot leave a container behind.
			_ = proc.Shutdown(2 * time.Second)
		}()

		go func() {
			if stdin != "" {
				_, _ = io.WriteString(proc.Stdin(), stdin)
			}
			_ = proc.Stdin().Close()
		}()

		var stdout, stderr bytes.Buffer
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); _, _ = io.Copy(&stdout, proc.Stdout()) }()
		go func() { defer wg.Done(); _, _ = io.Copy(&stderr, proc.Stderr()) }()

		_, waitErr := proc.Wait(runCtx)
		wg.Wait()
		return stdout.String(), stderr.String(), waitErr
	}

	// 1. discover the integration's own setup command and action list.
	descOut, descErr, err := run("", "sh", "-lc", "piccolo-gws describe --json || { echo 'test-runner-integration-contract: no integration binary on PATH' >&2; exit 3; }")
	if err != nil && !strings.Contains(descOut+descErr, "no integration binary") {
		t.Fatalf("describe failed: %v (%s)", err, descErr)
	}
	if strings.Contains(descOut+descErr, "no integration binary") {
		t.Skipf("image %s has no piccolo-gws installed", image)
	}
	desc, err := picintegration.ParseDescriptor([]byte(strings.TrimSpace(descOut)))
	if err != nil {
		t.Fatalf("parse descriptor: %v\n%s", err, descOut)
	}
	if !desc.Declarative() {
		t.Skipf("installed integration %q declares no actions; nothing to assert", desc.Name)
	}

	setupCommand := desc.Setup.Command
	binary := setupCommand[0]
	requireAccepted := func(what, stdout, stderr string, runErr error) {
		t.Helper()
		joined := stdout + "\n" + stderr
		for _, bad := range clapRejections {
			if strings.Contains(joined, bad) {
				t.Fatalf("integration CLI rejected Piccolo argv for %s: %q\nstdout=%s\nstderr=%s",
					what, bad, stdout, stderr)
			}
		}
		if runErr != nil && !strings.Contains(stdout, picintegration.EventSchemaV1) {
			t.Fatalf("%s produced no protocol payload (runErr=%v)\nstdout=%s\nstderr=%s", what, runErr, stdout, stderr)
		}
	}

	// 2. `state` must parse: it gates the whole menu.
	stateArgv, err := deriveArgv(setupCommand, "state")
	if err != nil {
		t.Fatal(err)
	}
	sOut, sErr, sErrRun := run("", binary, stateArgv[1:]...)
	requireAccepted("`state`", sOut, sErr, sErrRun)
	if _, err := picintegration.ParseState([]byte(strings.TrimSpace(sOut))); err != nil {
		t.Fatalf("state payload not parseable: %v\n%s", err, sOut)
	}

	// 3. every setup action id must be accepted by the real parser. A gated or
	//    failing action is fine; a clap parse error is the regression.
	actions := desc.ActionsByRole(picintegration.RoleSetup)
	if len(actions) == 0 {
		t.Fatal("descriptor claimed to be declarative but exposed no setup actions")
	}
	for _, action := range actions {
		callArgv, err := actionArgv(setupCommand, action.ID)
		if err != nil {
			t.Fatal(err)
		}
		entry, args := splitEntrypointCommand(callArgv)
		if len(entry) == 0 {
			t.Fatalf("derived argv for %q has no entrypoint: %v", action.ID, callArgv)
		}
		out, errOut, runErr := run("{}", entry[0], args...)
		requireAccepted("action "+action.ID, out, errOut, runErr)
		t.Logf("action %s accepted: %s", action.ID, strings.TrimSpace(firstEventLine(out)))
	}
}

// firstEventLine returns the first protocol line for logs, else the raw string.
func firstEventLine(stdout string) string {
	for _, line := range strings.Split(stdout, "\n") {
		if strings.Contains(line, picintegration.EventSchemaV1) {
			return line
		}
	}
	return stdout
}
