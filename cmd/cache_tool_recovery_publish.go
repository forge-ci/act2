package cmd

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/nektos/act/pkg/artifactcache"
	"github.com/spf13/cobra"
)

func newCacheToolRecoveryPublish(ctx context.Context, input *Input) *cobra.Command {
	var record, source string
	var maxBytes int64
	var apply, quiescent bool
	command := &cobra.Command{Use: "publish", Short: "Publish completed updates from an exclusively held stopped-source merged tree", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if !apply {
			return fmt.Errorf("tool recovery publication requires --apply")
		}
		if !quiescent {
			return fmt.Errorf("tool recovery publication requires --source-quiescent and continuous stopped-source lifetime exclusion")
		}
		pin, err := readToolRecoveryIntent(record)
		if err != nil {
			return err
		}
		report := artifactcache.PublishToolRecovery(ctx, input.cacheServerPath, source, pin, maxBytes)
		if err := json.NewEncoder(cmd.OutOrStdout()).Encode(report); err != nil {
			return err
		}
		if report.Partial || !report.Completed {
			return fmt.Errorf("tool recovery publication incomplete: %s", report.Error)
		}
		return nil
	}}
	command.Flags().StringVar(&record, "record", "", "Required schema-1 frozen recovery intent JSON, maximum 1024 bytes")
	command.Flags().StringVar(&source, "source", "", "Quiescent merged tree; caller must hold stopped source lifetime and exclude all writers")
	command.Flags().Int64Var(&maxBytes, "max-bytes", 0, "Required positive complete payload validation ceiling")
	command.Flags().BoolVar(&quiescent, "source-quiescent", false, "Confirm stopped-source writers and removal remain excluded throughout publication")
	command.Flags().BoolVar(&apply, "apply", false, "Apply explicit post-stop publication; never releases the pin")
	return command
}
