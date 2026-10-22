//go:build linux

package artifactcache

import (
	"errors"
	"fmt"
	"path/filepath"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

// Preserve a closed graph's ENOENT behavior without repairing its payload.
// Each raw parent is resolved beneath the root before inspecting its final
// component. Never clean away symlink/.. semantics or accept a missing ancestor.
func closedMissingToolLeaf(root, relative, link string) error {
	rootFD, err := unix.Open(root, unix.O_PATH|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("tool missing-link root cannot be opened: %w", err)
	}
	defer unix.Close(rootFD)
	target := filepath.Dir(relative) + "/" + link
	for followed := 1; followed <= 40; followed++ {
		parent, leaf := filepath.Split(target)
		if leaf == "" || leaf == "." || leaf == ".." || len(target) > 4096 {
			return fmt.Errorf("tool missing-link target is not a bounded final leaf")
		}
		next, missing, err := inspectMissingToolTarget(rootFD, parent, leaf)
		if err != nil {
			return err
		}
		if missing {
			return verifyMissingToolPath(rootFD, relative)
		}
		if followed == 40 {
			break
		}
		// Split preserves the raw parent, including its trailing slash.
		target = parent + next
	}
	return fmt.Errorf("tool missing-link chain exceeds 40 symlinks")
}

func inspectMissingToolTarget(rootFD int, parent, leaf string) (string, bool, error) {
	parentFD, err := unix.Openat2(rootFD, parent, &unix.OpenHow{
		Flags:   uint64(unix.O_PATH | unix.O_DIRECTORY | unix.O_CLOEXEC),
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_MAGICLINKS,
	})
	if err != nil {
		return "", false, fmt.Errorf("tool missing-link parent is not provably inside the closed install: %w", err)
	}
	defer unix.Close(parentFD)
	var info unix.Stat_t
	err = unix.Fstatat(parentFD, leaf, &info, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(err, unix.ENOENT) {
		return "", true, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("tool missing-link leaf cannot be verified: %w", err)
	}
	if info.Mode&unix.S_IFMT != unix.S_IFLNK {
		return "", false, fmt.Errorf("tool missing-link target exists or changed")
	}
	var buffer [4097]byte
	count, err := unix.Readlinkat(parentFD, leaf, buffer[:])
	if err != nil || count == 0 || count > 4096 || !utf8.Valid(buffer[:count]) {
		return "", false, fmt.Errorf("tool missing-link chain has an invalid or oversized target")
	}
	next := string(buffer[:count])
	if filepath.IsAbs(next) {
		return "", false, fmt.Errorf("tool missing-link chain has an absolute target")
	}
	return next, false, nil
}

// The kernel also counts directory symlinks across the original whole lookup.
// Per-parent lookups alone would reset that aggregate traversal budget.
func verifyMissingToolPath(rootFD int, relative string) error {
	fd, err := unix.Openat2(rootFD, relative, &unix.OpenHow{
		Flags:   uint64(unix.O_PATH | unix.O_CLOEXEC),
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_MAGICLINKS,
	})
	if err == nil {
		unix.Close(fd)
		return fmt.Errorf("tool missing-link original target exists or changed")
	}
	if !errors.Is(err, unix.ENOENT) {
		return fmt.Errorf("tool missing-link original path is not a closed missing target: %w", err)
	}
	return nil
}
