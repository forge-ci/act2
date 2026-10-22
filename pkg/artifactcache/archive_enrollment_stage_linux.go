//go:build linux

package artifactcache

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

const archiveStageMarker = ".archive-import-stage-v1"

func (h *archiveEnrollmentHistory) stage() string {
	return filepath.Join(h.Root, ".import-archive-"+h.Identity)
}
func archiveImportStages(ctx context.Context, h *archiveEnrollmentHistory) importStagePolicy {
	return importStagePolicy{
		create: func(root, destination string) (string, transferLease, error) {
			if root != h.Root || destination != filepath.Join(h.Root, h.Namespace) || !h.ImportStarted {
				return "", nil, fmt.Errorf("archive stage differs from durable intent")
			}
			lease, err := openImportStageGate(root, destination)
			if err != nil {
				return "", nil, err
			}
			if err = prepareArchiveStage(ctx, h); err != nil {
				_ = lease.Close()
				return "", nil, err
			}
			return h.stage(), lease, nil
		},
		cleanup: func(_ string, report *ImportReport) {
			// Failed copy retains its one owned stage. A later pass clears it under
			// the same root lease; losing this process cannot create another stage.
			if report.Published {
				report.PendingStage = ""
			}
		},
	}
}
func prepareArchiveStage(ctx context.Context, h *archiveEnrollmentHistory) error {
	stage := h.stage()
	if err := os.Mkdir(stage, 0700); err != nil && !os.IsExist(err) {
		return err
	}
	if err := requireToolEnrollmentDirectory(stage, h.Root); err != nil {
		return err
	}
	marker := filepath.Join(stage, archiveStageMarker)
	if _, err := os.Lstat(marker); os.IsNotExist(err) {
		entries, err := readDirectoryPage(stage, 1)
		if err != nil {
			return err
		}
		if len(entries) != 0 {
			return fmt.Errorf("archive stage has no ownership witness")
		}
	} else if err != nil {
		return err
	}
	if err := prepareToolEnrollmentMarker(stage, archiveStageMarker, archiveFenceBytes(h)); err != nil {
		return err
	}
	if err := reclaimArchiveStage(ctx, h); err != nil {
		return err
	}
	// The preserved stage marker makes this a nonempty directory. Establish
	// the cohort marker explicitly under ownership before ordinary import init.
	return prepareToolEnrollmentMarker(stage, cohortMarker, []byte(cohortIdentity))
}
func reclaimArchiveStage(ctx context.Context, h *archiveEnrollmentHistory) error {
	return reclaimArchiveStageWithBudget(ctx, h, 100000)
}
func reclaimArchiveStageWithBudget(ctx context.Context, h *archiveEnrollmentHistory, budget int) error {
	fd, err := unix.Open(h.stage(), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), h.stage())
	defer file.Close()
	anchored := fmt.Sprintf("/proc/self/fd/%d/", fd)
	if err = requireToolEnrollmentMarker(anchored, archiveStageMarker, archiveFenceBytes(h)); err != nil {
		return err
	}
	mount, err := enrollmentReclaimMount(fd)
	if err != nil {
		return err
	}
	parentMount, err := toolMountID(h.Root)
	if err != nil || mount != parentMount {
		return fmt.Errorf("archive stage crosses a mount boundary: %v", err)
	}
	// One attempt traverses at most 100,000 entries. Partial removal retains
	// the root witness and resumes next time; nesting uses the bounded FD walker.
	for budget > 0 {
		done, err := reclaimEnrollmentDirectory(ctx, file, fd, mount, 0, &budget, archiveStageMarker)
		if err != nil {
			return err
		}
		if done {
			if _, err = file.Seek(0, io.SeekStart); err != nil {
				return err
			}
			names, err := file.Readdirnames(2)
			if err != nil && err != io.EOF {
				return err
			}
			if len(names) == 1 && names[0] == archiveStageMarker {
				return file.Sync()
			}
			if len(names) == 0 {
				return fmt.Errorf("archive stage lost its ownership witness")
			}
		}
		if _, err = file.Seek(0, io.SeekStart); err != nil {
			return err
		}
	}
	return errArchiveStageBudgetPending
}
