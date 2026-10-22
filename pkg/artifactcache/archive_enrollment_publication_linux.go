//go:build linux

package artifactcache

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
)

// Only first admission compares the still-quiescent source with the actual
// destination. Caller holds destination transfer exclusion through Ready commit.
// After Ready, retention and new uploads legitimately change it.
func verifyInitialArchivePublication(ctx context.Context, h *archiveEnrollmentHistory) error {
	destination := filepath.Join(h.Root, h.Namespace)
	receipt, err := ReadImportPublicationReceipt(destination)
	if err != nil {
		return err
	}
	if receipt.Source != h.snapshot() || receipt.MaxBytes != h.MaxBytes {
		return fmt.Errorf("initial archive publication identity differs")
	}
	sourceDB, err := openAuditDB(h.snapshot())
	if err != nil {
		return err
	}
	defer sourceDB.Close()
	source := &Handler{dir: h.snapshot(), storage: &Storage{rootDir: filepath.Join(h.snapshot(), "cache")}}
	inventory := auditLocked(ctx, source, sourceDB, 0)
	if inventory.Partial || inventory.Fingerprint != receipt.SourceFingerprint {
		return fmt.Errorf("retained archive source differs from initial publication")
	}
	report := ImportReport{}
	selected, err := selectImportCaches(ctx, source, sourceDB, h.MaxBytes, &report)
	if err != nil {
		return err
	}
	if uint64(len(selected)) != receipt.ImportedCount {
		return fmt.Errorf("initial archive publication count differs from retained source")
	}
	target := &Handler{dir: destination, storage: &Storage{rootDir: filepath.Join(destination, "cache")}}
	targetDB, err := openAuditDB(destination)
	if err != nil {
		return err
	}
	defer targetDB.Close()
	current := auditLocked(ctx, target, targetDB, 0)
	if current.Partial || current.EntryCount == nil || *current.EntryCount != receipt.ImportedCount || current.ArchiveBytes == nil || *current.ArchiveBytes != receipt.ImportedBytes {
		return fmt.Errorf("initial archive publication inventory differs from receipt")
	}
	var caches []*Cache
	if err = targetDB.Find(&caches, nil); err != nil {
		return err
	}
	sort.Slice(caches, func(i, j int) bool { return caches[i].ID < caches[j].ID })
	if len(caches) != len(selected) {
		return fmt.Errorf("initial archive publication lost metadata")
	}
	if err = verifyInitialArchiveRows(ctx, selected, caches, source, target, receipt); err != nil {
		return err
	}
	final := auditLocked(ctx, source, sourceDB, 0)
	if final.Partial || final.Fingerprint != inventory.Fingerprint {
		return fmt.Errorf("retained archive source changed during reconciliation")
	}
	return nil
}

func verifyInitialArchiveRows(ctx context.Context, selected, caches []*Cache, source, target *Handler, receipt ImportPublicationReceipt) error {
	var total int64
	for index, original := range selected {
		if err := ctx.Err(); err != nil {
			return err
		}
		actual := caches[index]
		expected := *original
		expected.ID = actual.ID
		if expected != *actual {
			return fmt.Errorf("initial archive publication metadata differs")
		}
		sourceSum, err := archiveChecksum(ctx, source.storage.filename(original.ID), original.Size)
		if err != nil {
			return err
		}
		targetSum, err := archiveChecksum(ctx, target.storage.filename(actual.ID), actual.Size)
		if err != nil {
			return err
		}
		if sourceSum != targetSum {
			return fmt.Errorf("initial archive publication bytes differ")
		}
		if index < len(receipt.Receipts) {
			row := receipt.Receipts[index]
			if row.SourceID != original.ID || row.DestinationID != actual.ID || row.Bytes != actual.Size || row.SHA256 != targetSum {
				return fmt.Errorf("initial archive publication checksum receipt differs")
			}
		}
		total += actual.Size
	}
	if total != receipt.ImportedBytes {
		return fmt.Errorf("initial archive publication byte total differs")
	}
	return nil
}
