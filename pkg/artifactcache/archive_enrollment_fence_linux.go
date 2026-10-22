//go:build linux

package artifactcache

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

const archiveSourceIdentity = ".archive-source-identity-v1"

func archiveFenceBytes(h *archiveEnrollmentHistory) []byte {
	return []byte("act2-archive-source-fence-v1\n" + h.Identity + "\n")
}
func requireArchiveFence(h *archiveEnrollmentHistory) error {
	return requireToolEnrollmentMarker(filepath.Dir(h.Source), filepath.Base(h.Source), archiveFenceBytes(h))
}
func requireArchiveSnapshot(h *archiveEnrollmentHistory) error {
	if !h.SourcePresent && !h.ImportStarted && !h.Ready {
		if _, err := os.Lstat(h.snapshot()); os.IsNotExist(err) {
			return nil
		}
	}
	if err := requireToolEnrollmentDirectory(h.snapshot(), filepath.Dir(h.Source)); err != nil {
		return err
	}
	return requireToolEnrollmentMarker(h.snapshot(), archiveSourceIdentity, archiveFenceBytes(h))
}
func fenceArchiveSource(h *archiveEnrollmentHistory) error {
	info, err := os.Lstat(h.Source)
	if err == nil && info.Mode().IsRegular() {
		if err = requireArchiveFence(h); err != nil {
			return err
		}
		if err = requireArchiveSnapshot(h); err != nil {
			return err
		}
		return syncToolDirectory(filepath.Dir(h.Source))
	}
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	flags := uint(unix.RENAME_NOREPLACE)
	if h.SourcePresent {
		if err != nil || !info.IsDir() {
			return fmt.Errorf("original archive source is missing or changed")
		}
		if err = requireToolEnrollmentDirectory(h.Source, filepath.Dir(h.Source)); err != nil {
			return err
		}
		if err = prepareToolEnrollmentMarker(h.Source, archiveSourceIdentity, archiveFenceBytes(h)); err != nil {
			return err
		}
		flags = unix.RENAME_EXCHANGE
	} else if err == nil {
		return fmt.Errorf("previously absent archive source appeared before fencing")
	}
	if err = prepareToolEnrollmentMarker(filepath.Dir(h.Source), filepath.Base(h.snapshot()), archiveFenceBytes(h)); err != nil {
		return err
	}
	if err = unix.Renameat2(unix.AT_FDCWD, h.snapshot(), unix.AT_FDCWD, h.Source, flags); err != nil {
		return err
	}
	if err = syncToolDirectory(filepath.Dir(h.Source)); err != nil {
		return err
	}
	if err = requireArchiveFence(h); err != nil {
		return err
	}
	return requireArchiveSnapshot(h)
}
