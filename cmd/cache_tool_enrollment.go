package cmd

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/nektos/act/pkg/artifactcache"
)

func newCacheToolEnrollmentCommand(ctx context.Context, input *Input) *cobra.Command {
	var source string
	var maxBytes int64
	var apply, quiescent, fenceOnly, requireFenced bool
	command := &cobra.Command{Use: "tool-enroll", Short: "Enroll once, then select an enrolled tool generation without cold fallback", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !apply || (!fenceOnly && !quiescent) {
				return fmt.Errorf("tool enrollment requires --apply; publication also requires --source-quiescent after draining all legacy writers")
			}
			var report artifactcache.ToolEnrollmentReport
			if fenceOnly {
				report = artifactcache.FenceToolEnrollmentSource(ctx, source, input.cacheServerPath, maxBytes)
			} else if requireFenced {
				report = artifactcache.CompleteToolEnrollment(ctx, source, input.cacheServerPath, maxBytes)
			} else {
				report = artifactcache.EnrollToolGeneration(ctx, source, input.cacheServerPath, maxBytes)
			}
			if err := json.NewEncoder(cmd.OutOrStdout()).Encode(report); err != nil {
				return err
			}
			if report.Partial || (!fenceOnly && !report.Enrolled) || !report.SourceFenced {
				return fmt.Errorf("tool enrollment incomplete: %s", report.Error)
			}
			return nil
		},
	}
	command.Flags().StringVar(&source, "from", "", "Quiescent legacy sibling root; atomically replaced by a refusal file while its tree is retained privately")
	command.Flags().Int64Var(&maxBytes, "max-bytes", 0, "Positive logical payload-byte admission bound")
	command.Flags().BoolVar(&apply, "apply", false, "Persist first-enrollment history and select a generation for reader admission")
	command.Flags().BoolVar(&quiescent, "source-quiescent", false, "Confirm all legacy writers, including older peers, remain excluded throughout enrollment")
	command.Flags().BoolVar(&fenceOnly, "fence-only", false, "Install only the durable source fence; drain old directory-FD writers before completing enrollment")
	command.Flags().BoolVar(&requireFenced, "require-fenced", false, "Require an existing exact source fence and complete enrollment without replacing that boundary")
	command.MarkFlagsMutuallyExclusive("fence-only", "require-fenced")
	return command
}
