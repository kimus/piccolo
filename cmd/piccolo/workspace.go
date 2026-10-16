package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/kimus/piccolo/internal/generate"
	"github.com/kimus/piccolo/internal/workspace"
)

type workspaceFlags struct{ yes bool }

func newWorkspaceCmd() *cobra.Command {
	var f workspaceFlags
	cmd := &cobra.Command{Use: "workspace", Short: "Inspect and migrate Piccolo workspace layout"}
	migrate := &cobra.Command{
		Use:   "migrate",
		Short: "Migrate a legacy flat workspace into the structured layout",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWorkspaceMigrate(cmd, f)
		},
	}
	migrate.Flags().BoolVar(&f.yes, "yes", false, "confirm file moves without prompting")
	cmd.AddCommand(migrate)
	return rejectUnknownArgs(cmd)
}

func runWorkspaceMigrate(cmd *cobra.Command, f workspaceFlags) error {
	out := cmd.OutOrStdout()
	dir, err := workspace.Require(dirFlag)
	if err != nil {
		return err
	}
	layout := workspace.NewLayout(dir)
	moves := legacyMoves(dir, layout)
	if len(moves) == 0 {
		if err := layout.EnsureDirs(); err != nil {
			return err
		}
		fmt.Fprintln(out, "workspace already uses the structured layout")
		return nil
	}
	var conflicts []string
	for _, mv := range moves {
		if _, err := os.Stat(mv.dst); err == nil {
			conflicts = append(conflicts, fmt.Sprintf("%s -> %s", rel(dir, mv.src), rel(dir, mv.dst)))
		}
	}
	if len(conflicts) > 0 && !f.yes {
		fmt.Fprintln(out, "Migration would overwrite existing structured files:")
		for _, c := range conflicts {
			fmt.Fprintf(out, "  %s\n", c)
		}
		fmt.Fprintln(out, "Resolve the conflicts or re-run with --yes to replace them.")
		return nil
	}
	if !f.yes {
		fmt.Fprintf(out, "Migrate %d legacy paths into picoclaw/? [y/N] ", len(moves))
		line, _ := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
		if !strings.EqualFold(strings.TrimSpace(line), "y") && !strings.EqualFold(strings.TrimSpace(line), "yes") {
			fmt.Fprintln(out, "Aborted — nothing moved.")
			return nil
		}
	}
	if err := layout.EnsureDirs(); err != nil {
		return err
	}
	var summary []string
	for _, mv := range moves {
		if err := os.MkdirAll(filepath.Dir(mv.dst), 0o755); err != nil {
			return err
		}
		if f.yes {
			_ = os.RemoveAll(mv.dst)
		}
		if err := os.Rename(mv.src, mv.dst); err != nil {
			return fmt.Errorf("move %s to %s: %w", rel(dir, mv.src), rel(dir, mv.dst), err)
		}
		line := fmt.Sprintf("moved %s -> %s", rel(dir, mv.src), rel(dir, mv.dst))
		summary = append(summary, line)
		fmt.Fprintln(out, line)
	}
	changed, err := updateGitignoreForMigration(dir)
	if err != nil {
		return err
	}
	if changed {
		summary = append(summary, "updated .gitignore")
		fmt.Fprintln(out, "updated .gitignore")
	}
	summaryPath := filepath.Join(dir, "piccolo-migration-summary.md")
	body := "# Piccolo Workspace Migration\n\n" + time.Now().Format(time.RFC3339) + "\n\n" + strings.Join(summary, "\n") + "\n"
	if err := os.WriteFile(summaryPath, []byte(body), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(out, "wrote %s\n", rel(dir, summaryPath))
	return nil
}

type fileMove struct{ src, dst string }

func legacyMoves(root string, layout workspace.Layout) []fileMove {
	candidates := map[string]string{
		"config.json":      filepath.Join(layout.PicoclawHome(), "config.json"),
		"agents":           filepath.Join(layout.PicoclawHome(), "agents"),
		"skills":           filepath.Join(layout.PicoclawHome(), "skills"),
		"workspace":        filepath.Join(layout.PicoclawHome(), "workspace"),
		"auth.json":        filepath.Join(layout.PicoclawHome(), "auth.json"),
		".security.yml":    filepath.Join(layout.PicoclawHome(), ".security.yml"),
		".picoclaw.pid":    filepath.Join(layout.PicoclawHome(), ".picoclaw.pid"),
		"launcher-auth.db": filepath.Join(layout.PicoclawHome(), "launcher-auth.db"),
		"logs":             filepath.Join(layout.PicoclawHome(), "logs"),
	}
	var out []fileMove
	for srcRel, dst := range candidates {
		src := filepath.Join(root, srcRel)
		if _, err := os.Stat(src); err == nil {
			out = append(out, fileMove{src: src, dst: dst})
		}
	}
	return out
}

func updateGitignoreForMigration(dir string) (bool, error) {
	path := filepath.Join(dir, ".gitignore")
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return false, err
	}
	// One shared list with init and doctor: this third copy is how entries ended up
	// differing between the three places that write .gitignore.
	entries := generate.GitignoreEntries
	seen := map[string]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		seen[strings.TrimSpace(line)] = true
	}
	out := string(data)
	changed := false
	for _, entry := range entries {
		if seen[entry] {
			continue
		}
		if out != "" && !strings.HasSuffix(out, "\n") {
			out += "\n"
		}
		out += entry + "\n"
		changed = true
	}
	if !changed {
		return false, nil
	}
	return true, os.WriteFile(path, []byte(out), 0o644)
}

func rel(root, path string) string {
	if r, err := filepath.Rel(root, path); err == nil {
		return r
	}
	return path
}
