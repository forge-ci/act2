package cmd

import (
	"context"
	"fmt"
	"time"

	"github.com/nektos/act/pkg/artifactcache"
	"github.com/spf13/cobra"
)

// A timeout is opt-in so callers without the flag retain every prior limit.
func toolOperationContext(ctx context.Context, cmd *cobra.Command, seconds int64) (context.Context, error) {
	if !cmd.Flags().Changed("timeout-secs") {
		return ctx, nil
	}
	if seconds < 1 || seconds > 43200 {
		return nil, fmt.Errorf("tool operation timeout must be between 1 and 43200 seconds")
	}
	return artifactcache.WithToolOperationBudget(ctx, time.Duration(seconds)*time.Second)
}
