//go:build linux

package artifactcache

import (
	"context"
	"fmt"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

// A bounded descriptor walk keeps the root witness through interrupted removal.
type enrollmentReclaimRoot struct {
	path, parent, marker string
	identity             []byte
	pending              error
}

func (r enrollmentReclaimRoot) reclaim(ctx context.Context, budget int) error {
	fd, err := unix.Open(r.path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), r.path)
	defer file.Close()
	anchored := fmt.Sprintf("/proc/self/fd/%d/", fd)
	if err = requireToolEnrollmentMarker(anchored, r.marker, r.identity); err != nil {
		return err
	}
	mount, err := enrollmentReclaimMount(fd)
	if err != nil {
		return err
	}
	parentMount, err := toolMountID(r.parent)
	if err != nil || mount != parentMount {
		return fmt.Errorf("enrollment reclamation crosses a mount boundary: %v", err)
	}
	for budget > 0 {
		done, err := reclaimEnrollmentDirectory(ctx, file, fd, mount, 0, &budget, r.marker)
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
			if len(names) == 1 && names[0] == r.marker {
				return file.Sync()
			}
			if len(names) == 0 {
				return fmt.Errorf("enrollment reclamation lost its ownership witness")
			}
		}
		if _, err = file.Seek(0, io.SeekStart); err != nil {
			return err
		}
	}
	return r.pending
}
