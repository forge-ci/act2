package cmd

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/nektos/act/pkg/serve"
	"github.com/spf13/cobra"
)

func newCacheToolRecoveryOverlay(ctx context.Context, input *Input) *cobra.Command {
	var record, sourceRoot, target, recipe string
	var maxBytes int64
	var apply, quiescent bool
	command := &cobra.Command{Use: "recover", Short: "Recover and publish a stopped disk's read-only native overlay in a dedicated helper", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if !apply {
			return fmt.Errorf("tool overlay recovery requires --apply")
		}
		if !quiescent {
			return fmt.Errorf("tool overlay recovery requires --source-quiescent and continuous stopped-source lifetime exclusion")
		}
		pin, err := readToolRecoveryIntent(record)
		if err != nil {
			return err
		}
		report := serve.RecoverToolCache(ctx, serve.ToolRecoveryOverlayRequest{Generation: serve.ToolGeneration{ID: pin.Generation, Store: input.cacheServerPath, OverlayRecipeSHA256: recipe}, Pin: pin, SourceRoot: sourceRoot, ToolCacheMount: target, MaxBytes: maxBytes})
		if err := json.NewEncoder(cmd.OutOrStdout()).Encode(report); err != nil {
			return err
		}
		if report.Partial || !report.Completed {
			return fmt.Errorf("tool overlay recovery incomplete: %s", report.Error)
		}
		return nil
	}}
	command.Flags().StringVar(&record, "record", "", "Required schema-1 frozen recovery intent JSON, maximum 1024 bytes")
	command.Flags().StringVar(&sourceRoot, "source-root", "", "Exclusive retained engine disk mounted in this helper")
	command.Flags().StringVar(&target, "toolcache-target", "", "Original producer's frozen tool cache target")
	command.Flags().StringVar(&recipe, "overlay-recipe-sha256", "", "Required exact frozen producer recipe digest")
	command.Flags().Int64Var(&maxBytes, "max-bytes", 0, "Required positive complete payload validation ceiling")
	command.Flags().BoolVar(&quiescent, "source-quiescent", false, "Confirm source writers and removal stay excluded until this dedicated helper exits")
	command.Flags().BoolVar(&apply, "apply", false, "Recover and publish; never release protection or delete the source")
	return command
}
