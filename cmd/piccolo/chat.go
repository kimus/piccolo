package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/kimus/piccolo/internal/docker"
)

func newChatCmd() *cobra.Command {
	var message string
	cmd := &cobra.Command{
		Use:   "chat",
		Short: "Talk to the assistant in the terminal",
		Long: "Runs PicoClaw's interactive agent client inside the gateway " +
			"container (docker exec picoclaw agent) and passes its output " +
			"through unmodified. With -m, sends a single message and prints the answer.",
		RunE: func(cmd *cobra.Command, args []string) error {
			name, _, runner, ctx, err := containerContext(cmd)
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
			if message != "" {
				return runChatOneShot(ctx, runner, name, message, cmd)
			}
			return runChatInteractive(ctx, runner, name)
		},
	}
	cmd.Flags().StringVarP(&message, "message", "m", "", "send a single message (non-interactive)")
	return cmd
}

// runChatOneShot executes `picoclaw agent -m <msg>` without a TTY and demuxes
// the output verbatim to stdout/stderr.
func runChatOneShot(ctx context.Context, runner *docker.Runner, name, message string, cmd *cobra.Command) error {
	session, err := runner.Exec(ctx, name, []string{"picoclaw", "agent", "-m", message}, false)
	if err != nil {
		return err
	}
	defer session.Close()

	demuxDone := make(chan error, 1)
	go func() {
		demuxDone <- docker.Demux(session.Reader, cmd.OutOrStdout(), cmd.ErrOrStderr())
	}()

	code, waitErr := session.Wait(ctx)
	<-demuxDone
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
// goes raw and bytes flow both ways, unmodified, until the remote client exits.
func runChatInteractive(ctx context.Context, runner *docker.Runner, name string) error {
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

	go func() {
		_, _ = io.Copy(session.Stdin(), os.Stdin)
		_ = session.Stdin().Close()
	}()

	outDone := make(chan struct{})
	go func() {
		defer close(outDone)
		_, _ = io.Copy(os.Stdout, session.Reader)
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
