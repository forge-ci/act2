//go:build linux

package artifactcache

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// Ready is durable before reclamation starts. Keep the small root identity
// directory so interrupted deletion retains its original ownership witness.
// All traversal and deletion is relative to no-follow directory descriptors.
func reclaimToolEnrollmentSnapshot(ctx context.Context, history toolEnrollmentHistory) error {
	if !history.Ready {
		return fmt.Errorf("cannot reclaim a pending enrollment source")
	}
	if err := requireToolEnrollmentSourceFence(history); err != nil {
		return err
	}
	snapshot := toolEnrollmentSourceSnapshot(history)
	if _, err := os.Lstat(snapshot); os.IsNotExist(err) {
		return nil
	}
	if err := requirePendingToolEnrollmentSnapshot(history); err != nil {
		return err
	}
	fd, err := unix.Open(snapshot, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), snapshot)
	defer file.Close()
	anchored := fmt.Sprintf("/proc/self/fd/%d/", fd)
	if err := requireToolEnrollmentMarker(anchored, toolEnrollmentSourceIdentityName, toolEnrollmentIdentityBytes(history.StoreIdentity)); err != nil {
		return err
	}
	mount, err := enrollmentReclaimMount(fd)
	if err != nil {
		return err
	}
	parentMount, err := toolMountID(filepath.Dir(snapshot))
	if err != nil || mount != parentMount {
		return fmt.Errorf("snapshot reclamation crosses a mount boundary: %v", err)
	}
	for {
		budget := 4096
		done, err := reclaimToolEnrollmentDirectory(ctx, file, fd, mount, 0, &budget)
		if err != nil {
			return err
		}
		if done {
			empty, err := toolEnrollmentReclaimedDirectory(file)
			if err != nil || empty {
				return err
			}
		}
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			return err
		}
	}
}

func toolEnrollmentReclaimedDirectory(directory *os.File) (bool, error) {
	if _, err := directory.Seek(0, io.SeekStart); err != nil {
		return false, err
	}
	names, err := directory.Readdirnames(2)
	if err != nil && err != io.EOF {
		return false, err
	}
	if len(names) == 0 {
		return false, fmt.Errorf("snapshot reclamation lost its identity witness")
	}
	return len(names) == 1 && names[0] == toolEnrollmentSourceIdentityName, nil
}

func enrollmentReclaimMount(fd int) (uint64, error) {
	var stat unix.Statx_t
	if err := unix.Statx(fd, "", unix.AT_EMPTY_PATH|unix.AT_NO_AUTOMOUNT, unix.STATX_MNT_ID, &stat); err != nil {
		return 0, err
	}
	if stat.Mask&unix.STATX_MNT_ID == 0 {
		return 0, fmt.Errorf("snapshot mount identity unavailable")
	}
	return stat.Mnt_id, nil
}

func reclaimToolEnrollmentDirectory(ctx context.Context, directory *os.File, root int, mount uint64, depth int, budget *int) (bool, error) {
	for *budget > 0 {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		names, err := directory.Readdirnames(1)
		if err == io.EOF {
			return true, directory.Sync()
		}
		if err != nil {
			return false, err
		}
		*budget--
		name := names[0]
		if depth == 0 && name == toolEnrollmentSourceIdentityName {
			continue
		}
		done, err := reclaimToolEnrollmentEntry(ctx, int(directory.Fd()), root, name, mount, depth, budget)
		if err != nil || !done {
			return false, err
		}
	}
	return false, directory.Sync()
}

func reclaimToolEnrollmentEntry(ctx context.Context, parent, root int, name string, mount uint64, depth int, budget *int) (bool, error) {
	fd, err := unix.Openat(parent, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err == unix.ENOTDIR || err == unix.ELOOP {
		return true, unix.Unlinkat(parent, name, 0)
	}
	if err != nil {
		return false, err
	}
	directory := os.NewFile(uintptr(fd), name)
	defer directory.Close()
	observed, err := enrollmentReclaimMount(fd)
	if err != nil || observed != mount {
		return false, fmt.Errorf("snapshot reclamation crosses a mount boundary: %v", err)
	}
	if depth >= 64 {
		// Private unfinished trees have no bootstrap depth bound. Lift the
		// next owned subtree inside this same snapshot instead of retaining
		// an unbounded descriptor stack or permanently refusing admission.
		// Either rename outcome stays below the same durable identity root.
		nameAtRoot := ".tool-enrollment-reclaim-" + rand.Text()
		// Restart at the root: readdir may not observe the new name at its
		// current offset, and EOF must never acknowledge a remaining tree.
		return false, unix.Renameat2(parent, name, root, nameAtRoot, unix.RENAME_NOREPLACE)
	}
	done, err := reclaimToolEnrollmentDirectory(ctx, directory, root, mount, depth+1, budget)
	if err != nil || !done {
		return false, err
	}
	return true, unix.Unlinkat(parent, name, unix.AT_REMOVEDIR)
}
