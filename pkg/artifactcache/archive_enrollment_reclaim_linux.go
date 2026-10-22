//go:build linux

package artifactcache

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

var errArchiveSourceBudgetPending = errors.New("archive source reclamation entry budget exhausted; retry to continue")

func archiveSourceReclamationPending(err error) bool {
	return errors.Is(err, errArchiveSourceBudgetPending)
}

func reclaimArchiveSource(ctx context.Context, h *archiveEnrollmentHistory) error {
	return reclaimArchiveSourceWithBudget(ctx, h, 100000)
}

func reclaimArchiveSourceWithBudget(ctx context.Context, h *archiveEnrollmentHistory, budget int) error {
	if !h.Ready {
		return fmt.Errorf("cannot reclaim a pending archive source")
	}
	if err := requireArchiveFence(h); err != nil {
		return err
	}
	if err := requireArchiveSnapshot(h); err != nil {
		return err
	}
	if err := validateArchiveDestination(ctx, h); err != nil {
		return err
	}
	return (enrollmentReclaimRoot{path: h.snapshot(), parent: filepath.Dir(h.Source), marker: archiveSourceIdentity, identity: archiveFenceBytes(h), pending: errArchiveSourceBudgetPending}).reclaim(ctx, budget)
}

// Called under the exclusive cohort lease, before opening namespace databases.
// Ready is the durable evidence that original writers were drained and the
// publication was verified. Pending and unrelated source trees remain untouched.
func reclaimCohortArchiveSources(ctx context.Context, root string) error {
	entries, err := readDirectoryPage(root, maxCohortNamespaces+2)
	if err != nil {
		return err
	}
	if len(entries) > maxCohortNamespaces+1 {
		return fmt.Errorf("cohort namespace limit exceeded")
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if err := reclaimCohortArchiveSource(ctx, root, entry.Name()); err != nil {
			return err
		}
	}
	return nil
}

func reclaimCohortArchiveSource(ctx context.Context, root, namespace string) error {
	if err := validateImportName(namespace); err != nil {
		return err
	}
	source := filepath.Join(filepath.Dir(root), namespace)
	if _, err := os.Lstat(archiveHistoryPath(source)); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	db, _, err := openArchiveHistory(ctx, source, false)
	if err != nil {
		return err
	}
	defer db.Close()
	h, err := readArchiveHistory(db)
	if err != nil {
		return err
	}
	if h == nil || h.Source != source || h.Root != root || h.Namespace != namespace {
		return fmt.Errorf("archive source history identity differs")
	}
	// Use caller-selected paths after checking the record, rather than granting
	// a persisted JSON document authority to choose reclamation paths.
	h.Source, h.Root, h.Namespace = source, root, namespace
	if !h.Ready {
		return fmt.Errorf("archive publication remains pending; preserve source and destination for reconciliation")
	}
	request := ArchiveEnrollmentReport{Source: h.Source, Root: h.Root, Namespace: h.Namespace, MaxBytes: h.MaxBytes}
	if err := validateArchiveEnrollment(&request, ArchiveEnrollmentComplete); err != nil {
		return err
	}
	return reclaimArchiveSource(ctx, h)
}
