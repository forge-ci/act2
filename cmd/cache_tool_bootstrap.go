package cmd

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/nektos/act/pkg/artifactcache"
)

func newCacheToolBootstrapCommand(ctx context.Context, input *Input) *cobra.Command {
	var source string
	var maxBytes int64
	var apply, quiescent bool
	command := &cobra.Command{Use: "tool-bootstrap", Short: "Deliberately migrate a quiescent legacy tool cache to its first selected immutable generation", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !apply || !quiescent {
				return fmt.Errorf("tool bootstrap requires --apply and --source-quiescent; exclude all legacy source writers first")
			}
			report := artifactcache.BootstrapToolGeneration(ctx, source, input.cacheServerPath, maxBytes)
			if err := json.NewEncoder(cmd.OutOrStdout()).Encode(report); err != nil {
				return err
			}
			if report.Partial {
				return fmt.Errorf("tool bootstrap incomplete: %s", report.Error)
			}
			return nil
		},
	}
	command.Flags().StringVar(&source, "from", "", "Quiescent legacy tool-cache root containing completed installs")
	command.Flags().Int64Var(&maxBytes, "max-bytes", 0, "Positive logical payload-byte bound for the complete warm generation")
	command.Flags().BoolVar(&apply, "apply", false, "Publish and select the first immutable warm generation; never an automatic missing-cache fallback")
	command.Flags().BoolVar(&quiescent, "source-quiescent", false, "Confirm all source writers, including older peers, are excluded throughout bootstrap")
	return command
}
