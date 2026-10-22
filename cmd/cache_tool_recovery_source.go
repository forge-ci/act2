package cmd

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/nektos/act/pkg/artifactcache"
	"github.com/spf13/cobra"
)

func newCacheToolRecoverySourceEnrollment(ctx context.Context, input *Input) *cobra.Command {
	var apply bool
	command := &cobra.Command{Use: "enroll-source-store", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if !apply {
			return fmt.Errorf("source store enrollment requires --apply")
		}
		report := artifactcache.EnrollToolRecoverySourceStore(ctx, input.cacheServerPath)
		if err := json.NewEncoder(cmd.OutOrStdout()).Encode(report); err != nil {
			return err
		}
		if report.Partial {
			return fmt.Errorf("source store enrollment incomplete: %s", report.Error)
		}
		return nil
	}}
	command.Flags().BoolVar(&apply, "apply", false, "Create a new empty source-protected store before bootstrap; never upgrade older stores")
	return command
}

func newCacheToolRecoverySourceDisposal(ctx context.Context, input *Input) *cobra.Command {
	var apply bool
	var record string
	command := &cobra.Command{Use: "dispose-source", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if !apply {
			return fmt.Errorf("source disposal fence requires --apply")
		}
		pin, err := readToolRecoveryIntent(record)
		if err != nil {
			return err
		}
		report := artifactcache.FenceToolRecoverySourceDisposal(ctx, input.cacheServerPath, pin)
		if err := json.NewEncoder(cmd.OutOrStdout()).Encode(report); err != nil {
			return err
		}
		if report.Partial {
			return fmt.Errorf("source disposal fence incomplete: %s", report.Error)
		}
		return nil
	}}
	command.Flags().BoolVar(&apply, "apply", false, "Fence source recovery access; does not delete data or prove engine writer absence")
	command.Flags().StringVar(&record, "record", "", "Required exact frozen schema-1 recovery intent JSON")
	return command
}
