package cmd

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/nektos/act/pkg/serve"
	"github.com/spf13/cobra"
)

func newCacheToolRecoveryFinalize(ctx context.Context, input *Input) *cobra.Command {
	var record, sourceRoot, target, recipe string
	var maxBytes int64
	var apply, quiescent bool
	command := &cobra.Command{Use: "finalize-source", Short: "Reconcile source publication, reference release and producer exclusion", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if !apply || !quiescent {
			return fmt.Errorf("source finalization requires --apply and --source-quiescent")
		}
		pin, err := readToolRecoveryIntent(record)
		if err != nil {
			return err
		}
		report := serve.FinalizeToolCache(ctx, serve.ToolRecoveryOverlayRequest{
			Generation: serve.ToolGeneration{ID: pin.Generation, Store: input.cacheServerPath, OverlayRecipeSHA256: recipe},
			Pin:        pin, SourceRoot: sourceRoot, ToolCacheMount: target, MaxBytes: maxBytes,
		})
		if err := json.NewEncoder(cmd.OutOrStdout()).Encode(report); err != nil {
			return err
		}
		if report.Partial || !report.Disposed || !report.ReferenceAbsent {
			return fmt.Errorf("source finalization incomplete: %s", report.Error)
		}
		return nil
	}}
	command.Flags().StringVar(&record, "record", "", "Required exact frozen schema-1 recovery intent")
	command.Flags().StringVar(&sourceRoot, "source-root", "", "Retained source mount, needed only for unexpired unpublished recovery")
	command.Flags().StringVar(&target, "toolcache-target", "", "Original producer tool-cache target when recovery is needed")
	command.Flags().StringVar(&recipe, "overlay-recipe-sha256", "", "Frozen producer recipe digest when recovery is needed")
	command.Flags().Int64Var(&maxBytes, "max-bytes", 0, "Required positive complete payload validation ceiling")
	command.Flags().BoolVar(&quiescent, "source-quiescent", false, "Confirm exact source ownership and continuous writer/removal exclusion")
	command.Flags().BoolVar(&apply, "apply", false, "Reconcile producer authority; never deletes source data")
	return command
}
