package cmd

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/nektos/act/pkg/artifactcache"
)

func newCacheArchiveEnrollmentCommand(ctx context.Context, input *Input) *cobra.Command {
	var source, namespace string
	var maxBytes int64
	var apply, fenceOnly, quiescent, requireReady bool
	command := &cobra.Command{Use: "archive-enroll", Short: "Fence a legacy archive namespace, then publish and validate its coordinated route", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			phase := artifactcache.ArchiveEnrollmentComplete
			if fenceOnly {
				phase = artifactcache.ArchiveEnrollmentFence
			} else if requireReady {
				phase = artifactcache.ArchiveEnrollmentRequire
			}
			if !requireReady && !apply || !fenceOnly && !requireReady && !quiescent {
				return fmt.Errorf("archive enrollment requires --apply; completion also requires --source-quiescent after draining old writers")
			}
			report := artifactcache.EnrollArchive(ctx, source, input.cacheServerPath, namespace, maxBytes, phase)
			if err := json.NewEncoder(cmd.OutOrStdout()).Encode(report); err != nil {
				return err
			}
			if report.Partial || !report.SourceFenced || !fenceOnly && !report.Ready {
				return fmt.Errorf("archive enrollment incomplete: %s", report.Error)
			}
			return nil
		},
	}
	command.Flags().StringVar(&source, "from", "", "Legacy direct-child namespace; its path becomes a durable refusal file")
	command.Flags().StringVar(&namespace, "namespace", "", "Repository namespace in the sibling destination cohort")
	command.Flags().Int64Var(&maxBytes, "max-bytes", 0, "Positive completed-archive import bound")
	command.Flags().BoolVar(&apply, "apply", false, "Persist source fencing or publication state")
	command.Flags().BoolVar(&fenceOnly, "fence-only", false, "Fence new old-path writers without claiming existing directory-FD writers have stopped")
	command.Flags().BoolVar(&quiescent, "source-quiescent", false, "Confirm all source writers remain stopped through publication")
	command.Flags().BoolVar(&requireReady, "require-ready", false, "Validate existing fencing, publication receipt and current inventory without bootstrapping")
	command.MarkFlagsMutuallyExclusive("fence-only", "require-ready")
	command.MarkFlagsMutuallyExclusive("apply", "require-ready")
	command.MarkFlagsMutuallyExclusive("source-quiescent", "require-ready")
	return command
}
