//go:build !linux

package artifactcache

import (
	"context"
	"fmt"
)

func enrollArchive(context.Context, *ArchiveEnrollmentReport, ArchiveEnrollmentPhase) error {
	return fmt.Errorf("archive enrollment requires Linux atomic source fencing")
}
