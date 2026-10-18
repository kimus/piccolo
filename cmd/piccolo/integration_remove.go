package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/kimus/piccolo/internal/configdoc"
	"github.com/kimus/piccolo/internal/docker"
	"github.com/kimus/piccolo/internal/manifest"
	"github.com/kimus/piccolo/internal/runtimeimage"
	"github.com/kimus/piccolo/internal/setup"
	"github.com/kimus/piccolo/internal/workspace"
)

type integrationRemoveFlags struct {
	yes        bool
	purge      bool
	noRebuild  bool
	pull       bool
	tag        string
	keepEntry  bool
	dirFlagUse bool
}

func newIntegrationRemoveCmd() *cobra.Command {
	var f integrationRemoveFlags
	cmd := &cobra.Command{
		Use:   "remove <name>",
		Short: "Uninstall an integration: manifest record, runtime image, and its registered MCP server",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runIntegrationRemove(cmd, args[0], f)
		},
	}
	cmd.Flags().BoolVar(&f.yes, "yes", false, "non-interactive: accept prompts")
	cmd.Flags().BoolVar(&f.purge, "purge", false, "also delete integrations/<name>/ state, including stored credentials (permanent)")
	cmd.Flags().BoolVar(&f.noRebuild, "no-rebuild", false, "edit the workspace only; leave the runtime image to 'piccolo image build'")
	cmd.Flags().BoolVar(&f.keepEntry, "keep-server-entry", false, "leave tools.mcp.servers.<key> in config.json (a half-removal: with the MCP tool on it breaks every chat turn)")
	cmd.Flags().BoolVar(&f.pull, "pull", false, "ask Docker to pull a newer base image before building")
	cmd.Flags().StringVar(&f.tag, "tag", "", "local image tag to build (default: deterministic content-derived tag)")
	return cmd
}

// runIntegrationRemove undoes everything `integration add` and `integration setup`
// wrote, in the one order that cannot leave the gateway unable to chat.
//
// An integration is not one fact but four: a manifest record, image content, a
// registered MCP server entry in config.json, and state under integrations/. The
// dangerous pairing is a servers.<key> entry with no integration behind it — with
// tools.mcp.enabled on, PicoClaw tries to start every listed server and a missing
// command fails the whole turn, so EVERY chat message errors. Hence the ordering:
// build the replacement image first (nothing edited yet), unregister the server
// second, drop the manifest record last. The failure window that leaves is
// "record present, entry absent", which doctor already reports as an unfinished
// setup and which `integration setup` can redo — the recoverable direction.
func runIntegrationRemove(cmd *cobra.Command, name string, f integrationRemoveFlags) error {
	out := cmd.OutOrStdout()
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	dir, err := workspace.Require(dirFlag)
	if err != nil {
		return err
	}
	layout := workspace.NewLayout(dir)
	manifestPath := layout.Manifest()
	m, err := manifest.Load(manifestPath)
	if err != nil {
		return err
	}
	if len(m.Runtime.Integrations) == 0 {
		return errors.New("no integrations installed — nothing to remove")
	}
	idx := -1
	for i, in := range m.Runtime.Integrations {
		if integrationMatches(in, name) {
			idx = i
			break
		}
	}
	if idx < 0 {
		return fmt.Errorf("integration %q is not installed — 'piccolo integration list' shows what is", name)
	}
	target := m.Runtime.Integrations[idx]
	remaining := append([]manifest.Integration(nil), m.Runtime.Integrations[:idx]...)
	remaining = append(remaining, m.Runtime.Integrations[idx+1:]...)

	ref := displayPackage(target)
	label := target.Name
	if label == "" {
		label = ref
	}
	serverKey := strings.TrimSpace(target.MCPServerKey)

	doc, err := configdoc.Load(layout.Config())
	if err != nil {
		return err
	}
	entryPresent := serverKey != "" && setup.HasMCPServer(doc, serverKey)
	serversAfter := 0
	for _, k := range setup.MCPServerKeys(doc) {
		if k != serverKey {
			serversAfter++
		}
	}

	// --- plan -----------------------------------------------------------------
	fmt.Fprintf(out, "Removing integration: %s\n", label)
	if target.ResolvedVersion != "" {
		fmt.Fprintf(out, "  release:   %s (sha256:%s)\n", target.ResolvedVersion, shortDigest(target.ArtifactDigest))
	}
	fmt.Fprintf(out, "  image now: %s\n", m.Container.Image)
	fmt.Fprintf(out, "  changes:\n")
	fmt.Fprintf(out, "    - drop the runtime.integrations record for %s\n", ref)
	if entryPresent && !f.keepEntry {
		fmt.Fprintf(out, "    - unregister tools.mcp.servers.%s in %s\n", serverKey, "picoclaw/config.json")
		if serversAfter == 0 {
			// Nothing left that agents could reach, so the promise the toggle makes
			// has no referent. Leaving it on would advertise a capability that is gone.
			fmt.Fprintf(out, "    - turn tools.mcp.enabled off: this was the last registered server\n")
		}
	}
	if entryPresent && f.keepEntry {
		fmt.Fprintf(out, "    - KEEP tools.mcp.servers.%s as requested — config.json will name a server no installed integration provides\n", serverKey)
	}
	if len(remaining) > 0 && !f.noRebuild {
		fmt.Fprintf(out, "    - rebuild the runtime image without it (integrations kept: %s)\n", strings.Join(integrationRefs(remaining), ", "))
	} else if !f.noRebuild {
		fmt.Fprintf(out, "    - rebuild the runtime image with the toolchain only (no integrations remain)\n")
	} else {
		fmt.Fprintf(out, "    - leave the runtime image alone (--no-rebuild): it still CONTAINS this integration until you rebuild\n")
	}
	if target.StatePath != "" {
		if f.purge {
			fmt.Fprintf(out, "    - DELETE %s — stored credentials included, and they are not recoverable\n", target.StatePath)
			// This directory is bind-mounted into the gateway at
			// /root/.piccolo/integrations, so a purge while it runs deletes through the
			// mount: the running process loses its own state without any file write on
			// its side.
			fmt.Fprintf(out, "      (it is bind-mounted into the gateway — stop it first with 'piccolo stop', or expect the running process to lose its state)\n")
		} else {
			fmt.Fprintf(out, "    - preserve %s (use --purge to delete it)\n", target.StatePath)
		}
	}
	if len(target.SetupPorts) > 0 {
		fmt.Fprintf(out, "  note: container.ports entries %v were added for this integration and are LEFT AS-IS; Piccolo does not guess which of your port mappings belong to it\n", target.SetupPorts)
	}

	if !f.yes {
		fmt.Fprintf(out, "\nRemove this integration? [y/N] ")
		line, readErr := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
		answer := strings.ToLower(strings.TrimSpace(line))
		if readErr != nil && answer == "" {
			return errors.New("integration remove needs confirmation; rerun with --yes to accept non-interactively")
		}
		if answer != "y" && answer != "yes" {
			return errors.New("integration remove cancelled")
		}
	}
	// A purge deletes credentials, so it gets its own yes: the plan above can state
	// that the files are unrecoverable and be skimmed past on the way to the single
	// confirmation below it.
	if f.purge && !f.yes {
		fmt.Fprintf(out, "Also delete %s, including any stored credentials? This cannot be undone. [y/N] ", target.StatePath)
		line, _ := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
		if strings.ToLower(strings.TrimSpace(line)) != "y" {
			f.purge = false
			fmt.Fprintf(out, "  state kept at %s\n", target.StatePath)
		}
	}

	// --- 1. replacement image, before anything is edited -----------------------
	oldImage := m.Container.Image
	newImage := oldImage
	if !f.noRebuild {
		runner := newIntegrationRunner()
		pingCtx, cancel := context.WithTimeout(ctx, defaultImageBuildProbeTimeout)
		defer cancel()
		if err := runner.Ping(pingCtx); err != nil {
			return fmt.Errorf("docker daemon not reachable: %w", err)
		}
		fetcher := newReleaseFetcher()
		// Bundles come from the RECORDS that survive, so the Dockerfile — and thus the
		// content-addressed tag — describes the image without this integration. A tag
		// cannot be predicted here: the bundle install blocks are part of the rendered
		// Dockerfile, so a desired tag computed from metadata alone would be a guess.
		bundles, err := fetchReleaseBundlesForManifest(ctx, fetcher, remaining)
		if err != nil {
			return err
		}
		plan, err := runtimeimage.BuildPlan(runtimeimage.Options{
			BaseImage: integrationBuildBase(m),
			Tag:       f.tag,
			Packages:  effectiveToolchain(m).Packages,
			Commands:  effectiveToolchain(m).Commands,
			Bundles:   bundles,
		})
		if err != nil {
			return err
		}
		if plan.Tag == oldImage {
			// Nothing to build: the manifest already points at an image that does not
			// contain this integration, which means it was recorded but never installed.
			fmt.Fprintf(out, "\n✓ Runtime image already excludes this integration (%s); nothing to rebuild.\n", plan.Tag)
			newImage = plan.Tag
		} else {
			buildCtx, err := plan.ContextTar()
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "\nBuilding Piccolo runtime image without %s\n", label)
			fmt.Fprintf(out, "  base:    %s\n", plan.BaseImage)
			fmt.Fprintf(out, "  from:    %s\n", oldImage)
			fmt.Fprintf(out, "  to:      %s\n", plan.Tag)
			fmt.Fprintf(out, "  kept:    %s\n\n", strings.Join(integrationRefs(remaining), ", "))
			if err := runner.BuildImage(ctx, buildCtx, docker.ImageBuildOptions{Tag: plan.Tag, Dockerfile: "Dockerfile", Pull: f.pull, Remove: true}, out); err != nil {
				// The workspace is untouched at this point: a failed build costs a
				// build, not a config.
				return fmt.Errorf("build image: %w (workspace left unchanged)", err)
			}
			newImage = plan.Tag
		}
	}

	// --- 2. unregister the server entry ---------------------------------------
	if err := unregisterRemovedServer(layout, doc, serverKey, entryPresent, f, out); err != nil {
		return err
	}

	// --- 3. manifest record last ----------------------------------------------
	m.Runtime.Integrations = remaining
	if newImage != oldImage {
		// Recorded so `piccolo integration rollback` and the image-prune floor both
		// still see the image this workspace came from.
		m.Container.PreviousImage = oldImage
		m.Container.Image = newImage
	}
	if len(m.Runtime.Integrations) == 0 {
		m.Runtime.Integrations = nil
	}
	if err := m.Write(manifestPath); err != nil {
		return fmt.Errorf("write manifest: %w", err)
	}

	// --- 4. state -------------------------------------------------------------
	if f.purge && target.StatePath != "" {
		if err := purgeIntegrationState(layout, target.StatePath, out); err != nil {
			return err
		}
	}

	fmt.Fprintf(out, "\n✓ removed %s\n", label)
	if f.noRebuild {
		fmt.Fprintf(out, "  image:   %s still contains it — rebuild with 'piccolo image build'\n", oldImage)
	} else if newImage != oldImage {
		fmt.Fprintf(out, "  image:   %s (previous: %s)\n", newImage, oldImage)
	} else {
		fmt.Fprintf(out, "  image:   %s\n", newImage)
	}
	if !f.purge && target.StatePath != "" {
		fmt.Fprintf(out, "  state:   %s preserved (delete it with 'piccolo integration remove %s --purge --yes')\n", target.StatePath, ref)
	}
	// Serve owns the park/gate/commit-or-revert sequence; remove must not restart the
	// gateway itself, because an un-gated restart onto a bad image is an outage with no
	// fallback.
	if newImage != oldImage || entryPresent {
		fmt.Fprintf(out, "\nNext: 'piccolo serve' promotes the running gateway onto the new image (the old one is parked as a rollback target until the health and integration checks pass).\n")
	}
	if newImage != oldImage {
		fmt.Fprintf(out, "        'piccolo image list' will keep the old image while any container references it; 'piccolo image prune' reclaims it afterwards.\n")
	}
	return nil
}

// unregisterRemovedServer deletes the removed integration's MCP server entry from
// config.json and settles the tools.mcp.enabled toggle for whatever remains.
func unregisterRemovedServer(layout workspace.Layout, doc *configdoc.Doc, serverKey string, entryPresent bool, f integrationRemoveFlags, out io.Writer) error {
	if !entryPresent {
		return nil
	}
	if f.keepEntry {
		fmt.Fprintf(out, "\n[WARN] keeping tools.mcp.servers.%s as requested. No installed integration provides it: with tools.mcp.enabled on, the gateway will try to start it and EVERY chat turn fails if the command is missing. doctor reports this state.\n", serverKey)
		return nil
	}
	changed, err := setup.RemoveMCPServer(doc, serverKey)
	if err != nil {
		return err
	}
	if !changed {
		return nil
	}
	if len(setup.MCPServerKeys(doc)) == 0 {
		if st := setup.GetTools(doc); st.MCP == nil || *st.MCP {
			setup.SetTool(doc, "mcp", false)
			fmt.Fprintf(out, "\nUnregistered tools.mcp.servers.%s and turned tools.mcp.enabled off — no servers remain, so there is nothing left for the toggle to promise.\n", serverKey)
		} else {
			fmt.Fprintf(out, "\nUnregistered tools.mcp.servers.%s (the last server; tools.mcp.enabled was already off).\n", serverKey)
		}
	} else {
		// The surviving entries were verified when they were registered. Removal must
		// not silently re-promise them, and must not re-probe servers the user did not
		// touch either: the toggle's state is still whatever the last verification set.
		fmt.Fprintf(out, "\nUnregistered tools.mcp.servers.%s; tools.mcp.enabled left as-is for the %d remaining server(s).\n", serverKey, len(setup.MCPServerKeys(doc)))
	}
	configPath := layout.Config()
	if err := configdoc.Write(configPath, doc); err != nil {
		return fmt.Errorf("write %s: %w", filepath.Base(configPath), err)
	}
	return nil
}

// purgeIntegrationState deletes an integration's state directory, refusing anything
// outside the workspace's integrations/ root.
func purgeIntegrationState(layout workspace.Layout, statePath string, out io.Writer) error {
	root := layout.Integrations()
	// StatePath is recorded relative to the workspace root, and a recorded value is
	// not trusted: an absolute path or a .. escape would delete outside the workspace.
	clean := filepath.Clean("/" + strings.TrimPrefix(statePath, "./"))
	abs := filepath.Join(layout.Root, clean)
	if !strings.HasPrefix(abs, filepath.Clean(root)+string(os.PathSeparator)) {
		return fmt.Errorf("refusing to delete %s: not inside %s", statePath, root)
	}
	if _, err := os.Stat(abs); err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintf(out, "  state:   %s did not exist\n", statePath)
			return nil
		}
		return err
	}
	if err := os.RemoveAll(abs); err != nil {
		return fmt.Errorf("delete %s: %w", statePath, err)
	}
	fmt.Fprintf(out, "  state:   deleted %s\n", statePath)
	return nil
}

func integrationRefs(items []manifest.Integration) []string {
	out := make([]string, 0, len(items))
	for _, in := range items {
		out = append(out, displayPackage(in))
	}
	return out
}
