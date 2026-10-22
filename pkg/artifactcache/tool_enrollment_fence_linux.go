//go:build linux

package artifactcache

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

const toolEnrollmentSourceIdentityName = ".tool-enrollment-source-identity-v1"

func toolEnrollmentSourceSnapshot(history toolEnrollmentHistory) string {
	return filepath.Join(filepath.Dir(history.Source), "."+filepath.Base(history.Source)+".native-enrollment-"+history.StoreIdentity)
}

func toolEnrollmentFenceBytes(identity string) []byte {
	return []byte("bosn-tool-enrollment-source-fence-v1\n" + identity + "\n")
}

func prepareToolEnrollmentSourceFence(history toolEnrollmentHistory) error {
	return fenceToolEnrollmentSource(history, func(from, to string, flags uint) error {
		return unix.Renameat2(unix.AT_FDCWD, from, unix.AT_FDCWD, to, flags)
	}, syncToolDirectory)
}

// No fallback to copy, ordinary rename, or an interval with a missing warm
// source: new legacy mkdir calls must meet the refusal file atomically.
func fenceToolEnrollmentSource(history toolEnrollmentHistory, rename func(string, string, uint) error, syncParent func(string) error) error {
	source, snapshot := history.Source, toolEnrollmentSourceSnapshot(history)
	parent := filepath.Dir(source)
	info, err := os.Lstat(source)
	if err == nil && info.Mode().IsRegular() {
		if err := requireToolEnrollmentSourceFence(history); err != nil {
			return err
		}
		if err := requirePendingToolEnrollmentSnapshot(history); err != nil {
			// A completed first publication may outlive its old payload while
			// its Ready acknowledgement is lost. Completion verifies that
			// selection before committing Ready; a missing selection cannot
			// use this path to re-bootstrap missing source data.
			_, selectionErr := os.Lstat(filepath.Join(history.Root, toolGenerationCurrent))
			_, snapshotErr := os.Lstat(snapshot)
			if selectionErr != nil || !os.IsNotExist(snapshotErr) {
				return err
			}
		}
		return syncParent(parent)
	}
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	flags := uint(unix.RENAME_NOREPLACE)
	if history.SourcePresent {
		if err != nil || !info.IsDir() {
			return fmt.Errorf("original legacy source is missing, aliased or changed before cutover")
		}
		if err := requireToolEnrollmentDirectory(source, parent); err != nil {
			return err
		}
		if err := prepareToolEnrollmentMarker(source, toolEnrollmentSourceIdentityName, toolEnrollmentIdentityBytes(history.StoreIdentity)); err != nil {
			return err
		}
		flags = unix.RENAME_EXCHANGE
	} else if err == nil {
		return fmt.Errorf("previously absent legacy source appeared before cutover")
	}
	if err := prepareToolEnrollmentMarker(parent, filepath.Base(snapshot), toolEnrollmentFenceBytes(history.StoreIdentity)); err != nil {
		return err
	}
	if err := rename(snapshot, source, flags); err != nil {
		return err
	}
	if err := syncParent(parent); err != nil {
		return err
	}
	if err := requireToolEnrollmentSourceFence(history); err != nil {
		return err
	}
	return requirePendingToolEnrollmentSnapshot(history)
}

func requireToolEnrollmentSourceFence(history toolEnrollmentHistory) error {
	return requireToolEnrollmentMarker(filepath.Dir(history.Source), filepath.Base(history.Source), toolEnrollmentFenceBytes(history.StoreIdentity))
}

func requireToolEnrollmentDirectory(path, parent string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("enrollment source snapshot is missing or not a directory: %v", err)
	}
	parentMount, err := toolMountID(parent)
	if err != nil {
		return err
	}
	mount, err := toolMountID(path)
	if err != nil || mount != parentMount {
		return fmt.Errorf("enrollment source directory crosses mount boundary: %v", err)
	}
	return nil
}

func requirePendingToolEnrollmentSnapshot(history toolEnrollmentHistory) error {
	snapshot := toolEnrollmentSourceSnapshot(history)
	if !history.SourcePresent {
		if _, err := os.Lstat(snapshot); !os.IsNotExist(err) {
			return fmt.Errorf("unexpected private snapshot for an absent cold source")
		}
		return nil
	}
	if err := requireToolEnrollmentDirectory(snapshot, filepath.Dir(history.Source)); err != nil {
		return err
	}
	return requireToolEnrollmentMarker(snapshot, toolEnrollmentSourceIdentityName, toolEnrollmentIdentityBytes(history.StoreIdentity))
}

func requireColdToolEnrollmentSnapshot(history toolEnrollmentHistory) error {
	empty, err := coldToolEnrollmentSnapshotIsEmpty(history)
	if err != nil {
		return err
	}
	if !empty {
		return fmt.Errorf("cold enrollment source gained data; refusing an empty generation")
	}
	return nil
}

func coldToolEnrollmentSnapshotIsEmpty(history toolEnrollmentHistory) (bool, error) {
	if err := requirePendingToolEnrollmentSnapshot(history); err != nil {
		return false, err
	}
	if !history.SourcePresent {
		return true, nil
	}
	file, err := os.Open(toolEnrollmentSourceSnapshot(history))
	if err != nil {
		return false, err
	}
	defer file.Close()
	entries, err := file.ReadDir(2)
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	if len(entries) != 1 || entries[0].Name() != toolEnrollmentSourceIdentityName {
		return false, nil
	}
	return true, nil
}
