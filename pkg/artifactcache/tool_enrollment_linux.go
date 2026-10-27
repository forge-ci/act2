//go:build linux

package artifactcache

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"
	"unicode/utf8"

	"go.etcd.io/bbolt"
)

func enrollToolGeneration(ctx context.Context, source, root string, maxBytes int64) (report ToolEnrollmentReport) {
	return enrollToolGenerationWithReady(ctx, source, root, maxBytes, storeToolEnrollmentHistory)
}

func enrollToolGenerationWithReady(ctx context.Context, source, root string, maxBytes int64, ready func(*bbolt.DB, toolEnrollmentHistory) error) (report ToolEnrollmentReport) {
	return enrollToolGenerationOperation(ctx, source, root, maxBytes, toolEnrollmentAll, ready)
}

func enrollToolGenerationPhase(ctx context.Context, source, root string, maxBytes int64, phase toolEnrollmentPhase) ToolEnrollmentReport {
	return enrollToolGenerationOperation(ctx, source, root, maxBytes, phase, storeToolEnrollmentHistory)
}

func enrollToolGenerationOperation(ctx context.Context, source, root string, maxBytes int64, phase toolEnrollmentPhase, ready func(*bbolt.DB, toolEnrollmentHistory) error) (report ToolEnrollmentReport) {
	report.SchemaVersion, report.Source, report.Root = 1, source, root
	ctx, cancel := toolOperationTimeout(ctx, 15*time.Minute)
	defer cancel()
	if err := validateToolEnrollmentPaths(source, root, maxBytes); err != nil {
		report.fail(err)
		return report
	}
	db, fresh, err := openToolEnrollmentHistoryMode(ctx, root, phase != toolEnrollmentRequireFenced)
	if err != nil {
		report.fail(err)
		return report
	}
	defer db.Close()
	history, err := loadToolEnrollmentHistory(db, source, root)
	if err != nil {
		report.fail(err)
		return report
	}
	if history == nil {
		if !fresh {
			report.fail(fmt.Errorf("existing enrollment history has no identity; explicit reconciliation required"))
			return report
		}
		history, err = beginToolEnrollmentHistory(db, source, root)
		if err != nil {
			report.fail(err)
			return report
		}
	}
	report.Mode = history.Mode
	if phase == toolEnrollmentRequireFenced {
		if err := requireToolEnrollmentSourceFence(*history); err != nil {
			report.fail(err)
			return report
		}
	}
	if !history.Ready {
		err := advanceToolEnrollment(ctx, db, history, phase, maxBytes, ready)
		report.Mode = history.Mode
		if err != nil {
			report.fail(err)
			return report
		}
	}
	if err := requireToolEnrollmentSourceFence(*history); err != nil {
		report.fail(err)
		return report
	}
	if phase == toolEnrollmentFenceOnly {
		report.SourceFenced = true
		return report
	}
	selection, err := toolEnrollmentSelection(ctx, root, maxBytes, *history)
	if err != nil {
		report.fail(err)
		return report
	}
	if err := reclaimToolEnrollmentSnapshot(ctx, *history); err != nil {
		report.fail(err)
		return report
	}
	report.Generation, report.Enrolled, report.SourceFenced = selection.ID, true, true
	return report
}

func advanceToolEnrollment(ctx context.Context, db *bbolt.DB, history *toolEnrollmentHistory, phase toolEnrollmentPhase, maxBytes int64, ready func(*bbolt.DB, toolEnrollmentHistory) error) error {
	if err := enrollToolRecoverySourceStore(ctx, history.Root); err != nil {
		return err
	}
	if err := prepareToolEnrollmentIdentity(history.Root, history.StoreIdentity); err != nil {
		return err
	}
	if phase != toolEnrollmentRequireFenced {
		if err := refreshPendingToolEnrollmentSource(db, history); err != nil {
			return err
		}
		if err := prepareToolEnrollmentSourceFence(*history); err != nil {
			return err
		}
	}
	if phase == toolEnrollmentFenceOnly {
		return nil
	}
	if err := reclassifyPendingToolEnrollment(db, history); err != nil {
		return err
	}
	if err := completeToolEnrollment(ctx, history.Root, maxBytes, *history); err != nil {
		return err
	}
	if err := syncToolDirectory(filepath.Dir(history.Root)); err != nil {
		return err
	}
	history.Ready = true
	return ready(db, *history)
}

func validateToolEnrollmentPaths(source, root string, maxBytes int64) error {
	if err := validateToolBootstrapStore(root, maxBytes); err != nil {
		return err
	}
	if !filepath.IsAbs(source) || filepath.Clean(source) != source || source == root || source == toolEnrollmentHistoryPath(root) || filepath.Dir(source) != filepath.Dir(root) || len(filepath.Base(source)) > 128 || len(filepath.Base(root)) > 200 || !utf8.ValidString(source) || !utf8.ValidString(root) {
		return fmt.Errorf("enrollment requires distinct canonical sibling source and store paths")
	}
	return nil
}

func completeToolEnrollment(ctx context.Context, root string, maxBytes int64, history toolEnrollmentHistory) error {
	// Reconcile a selection published before the first history acknowledgement.
	// Ready admission never invokes bootstrap or initialize.
	if _, err := os.Lstat(filepath.Join(root, toolGenerationCurrent)); err == nil {
		_, err := CurrentToolGeneration(ctx, root, maxBytes)
		if err == nil {
			err = syncToolDirectory(root)
		}
		return err
	} else if !os.IsNotExist(err) {
		return err
	}
	if history.Mode == "warm" {
		if err := requirePendingToolEnrollmentSnapshot(history); err != nil {
			return err
		}
		report := BootstrapToolGeneration(ctx, toolEnrollmentSourceSnapshot(history), root, maxBytes)
		if report.Partial || !report.Selected {
			return fmt.Errorf("warm enrollment incomplete: %s", report.Error)
		}
		return nil
	}
	return initializeColdToolEnrollment(ctx, root, maxBytes, history)
}

func initializeColdToolEnrollment(ctx context.Context, root string, maxBytes int64, history toolEnrollmentHistory) error {
	if err := requireColdToolEnrollmentSnapshot(history); err != nil {
		return err
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return err
	}
	spec := ToolGenerationSpec{SchemaVersion: 1, Installs: []ToolGenerationInstall{}}
	published := PublishToolGeneration(ctx, root, spec, maxBytes)
	if published.Partial || !published.Published {
		return fmt.Errorf("cold generation publication incomplete: %s", published.Error)
	}
	selected := InitializeToolGeneration(ctx, root, spec, maxBytes)
	if selected.Partial || !selected.Selected {
		return fmt.Errorf("cold generation selection incomplete: %s", selected.Error)
	}
	return nil
}

func toolEnrollmentSelection(ctx context.Context, root string, maxBytes int64, history toolEnrollmentHistory) (ToolGenerationSelection, error) {
	if err := ctx.Err(); err != nil {
		return ToolGenerationSelection{}, err
	}
	catalog, err := prepareExistingToolSnapshotStore(root)
	if err != nil {
		return ToolGenerationSelection{}, fmt.Errorf("enrollment selection catalog acquisition (100ms lock probe): %w", err)
	}
	defer catalog.Close()
	if err := requireToolEnrollmentIdentity(root, history.StoreIdentity); err != nil {
		return ToolGenerationSelection{}, err
	}
	marker, err := os.ReadFile(filepath.Join(root, toolStoreMarker))
	if err != nil || string(marker) != toolStoreSourceRecoveryMagic {
		return ToolGenerationSelection{}, fmt.Errorf("enrolled source store epoch differs: %v", err)
	}
	selection, err := readToolGenerationSelection(root)
	if err != nil {
		return selection, err
	}
	manifest, _, err := readToolGenerationManifest(filepath.Join(root, toolGenerationDirectory, selection.ID), selection.ID)
	if err != nil {
		return selection, err
	}
	_, err = validateToolGenerationSpec(root, ToolGenerationSpec{SchemaVersion: manifest.SchemaVersion, Installs: manifest.Installs}, maxBytes)
	return selection, err
}
