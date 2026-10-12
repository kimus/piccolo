package main

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/kimus/piccolo/internal/docker"
	"github.com/kimus/piccolo/internal/doctor"
	"github.com/kimus/piccolo/internal/workspace"
)

func newDoctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Validate Docker, workspace, and generated files",
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := workspace.Require(dirFlag)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()

			results := doctor.Run(ctx, doctor.Options{
				Workspace: dir,
				Engine:    docker.New(),
			})
			out := cmd.OutOrStdout()
			for _, r := range results {
				fmt.Fprintf(out, "%s %s\n", r.Status, r.Message)
			}
			if doctor.Failed(results) {
				cmd.SilenceErrors = true
				return fmt.Errorf("doctor found failures")
			}
			return nil
		},
	}
}
