//go:build linux

package artifactcache

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"go.etcd.io/bbolt"
)

func finalizeToolRecoverySource(ctx context.Context, root string, pin ToolRecoveryPin, maxBytes int64, openView ToolRecoverySourceFactory) (report ToolRecoveryFinalizationReport) {
	report = ToolRecoveryFinalizationReport{SchemaVersion: 1, Root: root, Pin: pin}
	if err := requireToolSourceFinalization(ctx, root, pin, maxBytes); err != nil {
		report.fail(err)
		return report
	}
	if err := retryToolRecoveryViewCleanup(); err != nil {
		report.fail(err)
		return report
	}
	if pin.ExpiresAt.After(time.Now().UTC()) {
		publication := PublishToolRecoveryView(ctx, root, pin, maxBytes, openView)
		if publication.Partial || !publication.Completed {
			report.fail(fmt.Errorf("source publication incomplete: %s", publication.Error))
			return report
		}
		report.Publication = &ToolRecoveryCompletion{Pin: pin, Generation: publication.Generation, CompletedAt: publication.CompletedAt}
		if err := finalizeToolSourceReference(ctx, root, pin); err != nil {
			report.fail(err)
			return report
		}
		report.ReferenceAbsent = true
	}
	if err := fenceToolRecoverySourceDisposal(ctx, root, pin); err != nil {
		report.fail(err)
		return report
	}
	report.Disposed = true
	if !report.ReferenceAbsent {
		if err := finalizeToolSourceReference(ctx, root, pin); err != nil {
			report.fail(err)
			return report
		}
		report.ReferenceAbsent = true
	}
	return report
}

func finalizeToolSourceReference(ctx context.Context, root string, pin ToolRecoveryPin) error {
	released := ReleaseToolRecoveryPin(ctx, root, pin)
	if released.Partial || !released.Absent {
		return fmt.Errorf("source reference release incomplete: %s", released.Error)
	}
	return nil
}

// This is read-only. In particular, a legacy/missing store must not be enrolled
// or a missing coordination inode recreated as a side effect of recovery.
func requireToolSourceFinalization(ctx context.Context, root string, pin ToolRecoveryPin, maxBytes int64) error {
	if err := pin.validate(); err != nil {
		return err
	}
	if maxBytes <= 0 {
		return fmt.Errorf("source finalization requires a positive payload ceiling")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	catalog, err := prepareExistingToolSnapshotStore(root)
	if err != nil {
		return err
	}
	defer catalog.Close()
	marker, err := os.ReadFile(filepath.Join(root, toolStoreMarker))
	if err != nil {
		return err
	}
	if string(marker) != toolStoreSourceRecoveryMagic {
		return fmt.Errorf("source finalization requires an enrolled epoch-3 store")
	}
	if err := requireToolRecoverySourceLease(root, pin.Owner); err != nil {
		return err
	}
	db, err := bbolt.Open(toolRecoverySourceLeasePath(root, pin.Owner), 0600, &bbolt.Options{
		ReadOnly: true, Timeout: 100 * time.Millisecond, OpenFile: openCoordinationFile,
	})
	if err != nil {
		return err
	}
	defer db.Close()
	return db.View(func(tx *bbolt.Tx) error {
		if err := verifyToolRecoverySourceIdentity(tx, pin); err != nil {
			return err
		}
		if bucket := tx.Bucket([]byte("source-disposal-v1")); bucket != nil {
			if bucket.Stats().KeyN != 1 || string(bucket.Get([]byte("decision"))) != "disposed" {
				return fmt.Errorf("invalid source disposal decision")
			}
		}
		return nil
	})
}

// A view whose unmount failed keeps its leases until cleanup succeeds, even
// after its pin expires. Retry cleanup before trying the exclusive fence.
func retryToolRecoveryViewCleanup() error {
	if !recoveryViewOwner.TryLock() {
		return fmt.Errorf("recovery view already in use by this process")
	}
	defer recoveryViewOwner.Unlock()
	return cleanupPendingToolRecoveryView()
}
