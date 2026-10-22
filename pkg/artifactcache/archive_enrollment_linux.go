//go:build linux

package artifactcache

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"go.etcd.io/bbolt"
	boltErrors "go.etcd.io/bbolt/errors"
)

func enrollArchive(ctx context.Context, report *ArchiveEnrollmentReport, phase ArchiveEnrollmentPhase) error {
	if err := validateArchiveEnrollment(report, phase); err != nil {
		return err
	}
	db, h, err := loadArchiveHistory(ctx, report, phase == ArchiveEnrollmentFence)
	if err != nil {
		return err
	}
	defer db.Close()
	report.Snapshot = h.snapshot()
	if phase == ArchiveEnrollmentFence && !h.Ready {
		if err = fenceArchiveSource(h); err != nil {
			return err
		}
	} else if err = requireArchiveFence(h); err != nil {
		return err
	}
	report.SourceFenced = true
	if err = requireArchiveSnapshot(h); err != nil {
		return err
	}
	if h.Ready || phase == ArchiveEnrollmentRequire {
		if !h.Ready {
			return fmt.Errorf("archive routing is not ready")
		}
		if err = validateArchiveDestination(ctx, h); err != nil {
			return err
		}
		report.Ready = true
		return nil
	}
	if phase == ArchiveEnrollmentFence {
		return nil
	}
	if err = publishArchiveDestination(ctx, db, h); err != nil {
		return err
	}
	if err = finishInitialArchiveEnrollment(ctx, db, h); err != nil {
		return err
	}
	report.Ready = true
	return nil
}

func validateArchiveEnrollment(r *ArchiveEnrollmentReport, phase ArchiveEnrollmentPhase) error {
	if phase > ArchiveEnrollmentRequire || r.MaxBytes <= 0 {
		return fmt.Errorf("invalid archive enrollment phase or bound")
	}
	if err := validateImportName(r.Namespace); err != nil {
		return err
	}
	if !filepath.IsAbs(r.Source) || filepath.Clean(r.Source) != r.Source || !filepath.IsAbs(r.Root) || filepath.Clean(r.Root) != r.Root || filepath.Base(r.Source) != r.Namespace || filepath.Dir(r.Root) != filepath.Dir(r.Source) || r.Source == r.Root {
		return fmt.Errorf("archive enrollment requires distinct sibling source and cohort root")
	}
	parent := filepath.Dir(r.Source)
	canonical, err := filepath.EvalSymlinks(parent)
	if err != nil || canonical != parent {
		return fmt.Errorf("archive enrollment parent must be an existing canonical directory")
	}
	if info, err := os.Lstat(r.Root); err == nil {
		if !info.IsDir() {
			return fmt.Errorf("archive cohort root is not a directory")
		}
		return requireToolEnrollmentDirectory(r.Root, parent)
	} else if !os.IsNotExist(err) {
		return err
	}
	return nil
}

func publishArchiveDestination(ctx context.Context, db *bbolt.DB, h *archiveEnrollmentHistory) error {
	destination := filepath.Join(h.Root, h.Namespace)
	if _, err := os.Lstat(destination); err == nil {
		// A lost import reply is recovered from the receipt and current inventory,
		// never by treating destination existence alone as successful publication.
		return syncToolDirectory(h.Root)
	} else if !os.IsNotExist(err) {
		return err
	}
	if h.Cold {
		if err := prepareColdArchiveSource(ctx, h); err != nil {
			return err
		}
	}
	h.ImportStarted = true
	if err := storeArchiveHistory(db, h); err != nil {
		return err
	}
	result := importCompletedWithStages(ctx, h.snapshot(), h.Root, h.Namespace, h.MaxBytes, archiveImportStages(ctx, h))
	if !result.Published && result.PendingStage == "" {
		h.ImportStarted = false
		if err := storeArchiveHistory(db, h); err != nil {
			return err
		}
	}
	if result.Partial || !result.Published {
		if result.cause != nil {
			return fmt.Errorf("archive import remains incomplete: %w", result.cause)
		}
		return fmt.Errorf("archive import remains incomplete: %s", result.Error)
	}
	return nil
}
func prepareColdArchiveSource(ctx context.Context, h *archiveEnrollmentHistory) error {
	if err := os.Mkdir(h.snapshot(), 0700); err != nil && !os.IsExist(err) {
		return err
	}
	if err := prepareToolEnrollmentMarker(h.snapshot(), archiveSourceIdentity, archiveFenceBytes(h)); err != nil {
		return err
	}
	// An absent or originally empty source may only gain our own coordination
	// files before its first import. Never silently skip unexpected warm data.
	entries, err := readDirectoryPage(h.snapshot(), 4)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Name() != archiveSourceIdentity && entry.Name() != "bolt.db" && entry.Name() != "cache" {
			return fmt.Errorf("cold archive source gained unexpected state")
		}
	}
	if _, err := NewStorage(filepath.Join(h.snapshot(), "cache")); err != nil {
		return err
	}
	handler := &Handler{dir: h.snapshot(), storage: &Storage{rootDir: filepath.Join(h.snapshot(), "cache")}}
	metadata, err := handler.openDB()
	if err != nil {
		return err
	}
	if err = metadata.Close(); err != nil {
		return err
	}
	sourceDB, err := openAuditDB(h.snapshot())
	if err != nil {
		return err
	}
	defer sourceDB.Close()
	audit := auditLocked(ctx, handler, sourceDB, 0)
	if audit.Partial || audit.EntryCount == nil || *audit.EntryCount != 0 || audit.ArchiveBytes == nil || *audit.ArchiveBytes != 0 {
		return fmt.Errorf("cold archive source gained data")
	}
	return nil
}
func validateArchiveDestination(ctx context.Context, h *archiveEnrollmentHistory) error {
	if h.Ready {
		if err := requireArchiveDestinationIdentity(h); err != nil {
			return err
		}
	}
	receipt, err := ReadImportPublicationReceipt(filepath.Join(h.Root, h.Namespace))
	if err != nil {
		return err
	}
	if receipt.Source != h.snapshot() || receipt.MaxBytes != h.MaxBytes {
		return fmt.Errorf("archive publication receipt differs from frozen intent")
	}
	audit := AuditStore(ctx, filepath.Join(h.Root, h.Namespace), 0)
	if audit.Status == StoreBusy {
		return fmt.Errorf("current archive destination busy: %w", boltErrors.ErrTimeout)
	}
	if audit.Partial || audit.Status != StoreReady || audit.ArchiveBytes == nil || audit.EntryCount == nil {
		return fmt.Errorf("current archive destination inventory is unavailable")
	}
	if h.Ready {
		return requireArchiveDestinationIdentity(h)
	}
	return nil
}

// Hold destination transfer exclusion through durable Ready publication. Idle
// cohort maintenance must not remove an archive between validation and commit.
func finishInitialArchiveEnrollment(ctx context.Context, db *bbolt.DB, h *archiveEnrollmentHistory) error {
	target := &Handler{dir: filepath.Join(h.Root, h.Namespace)}
	lease, err := target.transferLock(false)
	if err != nil {
		return err
	}
	defer lease.Close()
	if err = verifyInitialArchivePublication(ctx, h); err != nil {
		return err
	}
	if err = freezeArchiveDestinationIdentity(h); err != nil {
		return err
	}
	h.Ready = true
	if err = requireArchiveDestinationIdentity(h); err != nil {
		return err
	}
	return storeArchiveHistory(db, h)
}
