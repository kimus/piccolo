package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/kimus/piccolo/internal/workspace"
)

// pidFileRel is PicoClaw's runtime state file, written UNCONDITIONALLY at
// gateway startup (verified v0.3.1 pkg/gateway Run -> pid.WritePidFile).
// The image home /root/.picoclaw is the piccolo workspace bind mount, so the
// file is readable on the host. Its Token authenticates POST /reload.
const pidFileRel = ".picoclaw.pid"

// pidFile models the JSON payload in .picoclaw.pid (pkg/pid PidFileData).
type pidFile struct {
	PID     int    `json:"pid"`
	Token   string `json:"token"`
	Version string `json:"version"`
	Port    int    `json:"port"`
	Host    string `json:"host"`
}

func newReloadCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "reload",
		Short: "Apply config.json changes without restarting the container",
		Long: "Triggers the gateway's authenticated /reload endpoint so config.json " +
			"changes (channels, models, tools, agent behavior) take effect in place. " +
			"The gateway reloads asynchronously; services briefly restart. Changes to " +
			"piccolo.yaml (image, env, ports) still require 'piccolo restart'.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runReload(cmd)
		},
	}
}

func runReload(cmd *cobra.Command) error {
	out := cmd.OutOrStdout()
	dir, err := workspace.Require(dirFlag)
	if err != nil {
		return err
	}

	pf, err := readPidFile(filepath.Join(dir, pidFileRel))
	if err != nil {
		return err
	}

	healthURL, err := healthURLFor(cmd)
	if err != nil {
		return err
	}
	reloadURL := strings.TrimSuffix(healthURL, "/health") + "/reload"

	ctx := cmd.Context()
	reqCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, reloadURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+pf.Token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("gateway not reachable at %s — is it running? (piccolo serve / piccolo restart): %w", reloadURL, err)
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		// Triggered; the reload itself is async inside the gateway.
	case http.StatusUnauthorized:
		return fmt.Errorf("reload token rejected — %s is stale for the running gateway (it regenerates the token on startup); apply changes with 'piccolo restart' instead", pidFileRel)
	default:
		return fmt.Errorf("reload failed (HTTP %d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	fmt.Fprintf(out, "\u2713 reload triggered on %s\n", reloadURL)
	// Give the async reload a moment, then confirm the gateway is back.
	time.Sleep(3 * time.Second)
	status, summary := probeHealth(ctx, healthURL)
	if strings.HasPrefix(status, "200") {
		fmt.Fprintf(out, "\u2713 gateway healthy after reload (%s %s)\n", status, summary)
	} else {
		fmt.Fprintf(out, "warning: gateway not answering /health yet (%s) — check 'piccolo logs'; if it stays down run 'piccolo restart'\n", status)
	}
	return nil
}

// readPidFile parses the gateway-authentication pid file from the workspace.
func readPidFile(path string) (*pidFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("no %s in the workspace — the gateway has not been started by 'piccolo serve' (or exited before writing it). Run 'piccolo serve' first", pidFileRel)
		}
		return nil, fmt.Errorf("read %s: %w", pidFileRel, err)
	}
	var pf pidFile
	if err := json.Unmarshal(data, &pf); err != nil {
		return nil, fmt.Errorf("%s does not parse (stale or unexpected format) — apply changes with 'piccolo restart' instead: %w", pidFileRel, err)
	}
	if strings.TrimSpace(pf.Token) == "" {
		return nil, fmt.Errorf("%s carries no reload token — apply changes with 'piccolo restart' instead", pidFileRel)
	}
	return &pf, nil
}
