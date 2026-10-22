package cmd

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/nektos/act/pkg/artifactcache"
)

func newCacheUsageCommand(ctx context.Context, input *Input) *cobra.Command {
	var maxEntries int
	command := &cobra.Command{
		Use: "usage", Short: "Measure bounded unique inode allocation across one cache root", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			report := artifactcache.AuditCacheFootprint(ctx, input.cacheServerPath, maxEntries)
			if err := json.NewEncoder(cmd.OutOrStdout()).Encode(report); err != nil {
				return err
			}
			if report.Partial {
				return fmt.Errorf("cache usage incomplete: %s", report.Error)
			}
			return nil
		},
	}
	command.Flags().IntVar(&maxEntries, "max-entries", 0, "Required metadata entry bound, 1..1000000; incomplete byte totals are unknown")
	return command
}
