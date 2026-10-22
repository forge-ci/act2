//go:build linux

package artifactcache

import (
	"os"
	"path/filepath"

	"go.etcd.io/bbolt"
)

// Before cutover, a pre-claim writer can win mkdir on an initially absent
// legacy path. Freeze the observed directory durably before exchanging it;
// never discard its newly arrived cache as an empty first start.
func refreshPendingToolEnrollmentSource(db *bbolt.DB, history *toolEnrollmentHistory) error {
	if history.SourcePresent {
		return nil
	}
	info, err := os.Lstat(history.Source)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode().IsRegular() {
		return requireToolEnrollmentSourceFence(*history)
	}
	if err := requireToolEnrollmentDirectory(history.Source, filepath.Dir(history.Source)); err != nil {
		return err
	}
	empty, err := toolEnrollmentSourceIsCold(history.Source)
	if err != nil {
		return err
	}
	history.SourcePresent = true
	if !empty {
		history.Mode = "warm"
	}
	return storeToolEnrollmentHistory(db, *history)
}

// The source fence does not revoke a directory descriptor opened earlier.
// After those writers drain, a Pending cold decision can become warm before
// any generation is published. This transition is durable and one-way. Ready
// history and missing selections never reach this initial-enrollment path.
func reclassifyPendingToolEnrollment(db *bbolt.DB, history *toolEnrollmentHistory) error {
	if history.Mode != "cold" {
		return nil
	}
	if _, err := os.Lstat(filepath.Join(history.Root, toolGenerationCurrent)); !os.IsNotExist(err) {
		return err
	}
	empty, err := coldToolEnrollmentSnapshotIsEmpty(*history)
	if err != nil || empty {
		return err
	}
	history.Mode = "warm"
	return storeToolEnrollmentHistory(db, *history)
}
