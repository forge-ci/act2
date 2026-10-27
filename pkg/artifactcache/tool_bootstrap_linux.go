//go:build linux

package artifactcache

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

func bootstrapToolGeneration(ctx context.Context, source, root string, maxBytes int64) ToolGenerationUpdateReport {
	return bootstrapToolGenerationWithSelectionSync(ctx, source, root, maxBytes, syncToolDirectory)
}

func bootstrapToolGenerationWithSelectionSync(ctx context.Context, source, root string, maxBytes int64, syncSelection func(string) error) (report ToolGenerationUpdateReport) {
	report.SchemaVersion = 1
	// Explicit enrollment materializes every completed legacy install, unlike
	// an incremental publication. Keep the whole migration finite while allowing
	// the measured multi-GiB cache to complete all copy, verification and syncs.
	ctx, cancel := toolOperationTimeout(ctx, 15*time.Minute)
	defer cancel()
	if err := validateToolBootstrapStore(root, maxBytes); err != nil {
		report.fail(err)
		return report
	}
	if existing, err := existingBootstrapSelection(ctx, root, maxBytes); err != nil {
		report.fail(err)
		return report
	} else if existing != nil {
		return *existing
	}
	plans, err := inspectToolUpdates(ctx, root, source, nil, maxBytes, toolScanLegacyBootstrap)
	if err != nil {
		report.fail(err)
		return report
	}
	if len(plans) == 0 {
		report.fail(fmt.Errorf("bootstrap source has no completed warm installs"))
		return report
	}
	catalog, err := prepareToolSnapshotStore(ctx, root)
	if err != nil {
		report.fail(err)
		return report
	}
	defer catalog.Close()
	// Another explicit bootstrap can finish while the source is inspected.
	if selection, err := readToolGenerationSelection(root); err == nil {
		return reconcileBootstrapSelection(ctx, root, selection, maxBytes)
	} else if !os.IsNotExist(err) {
		report.fail(err)
		return report
	}
	installs, pending, err := publishPlannedToolObjects(ctx, catalog, root, plans, maxBytes)
	if err != nil {
		report.Generation.PendingStage = pending
		report.fail(err)
		return report
	}
	installs, err = validateToolGenerationSpec(root, ToolGenerationSpec{SchemaVersion: 1, Installs: installs}, maxBytes)
	if err != nil {
		report.fail(err)
		return report
	}
	report.Generation = publishToolGenerationLocked(ctx, catalog, root, installs, maxBytes)
	if report.Generation.Partial || !report.Generation.Published {
		report.fail(fmt.Errorf("bootstrap generation publication incomplete: %s", report.Generation.Error))
		return report
	}
	report.Selected, report.PendingSelection, err = writeToolGenerationSelection(ctx, catalog, root, ToolGenerationSelection{SchemaVersion: 1, ID: report.Generation.ID}, syncSelection)
	if err != nil {
		report.fail(err)
	}
	return report
}

func existingBootstrapSelection(ctx context.Context, root string, maxBytes int64) (*ToolGenerationUpdateReport, error) {
	if _, err := os.Lstat(root); os.IsNotExist(err) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	if _, err := os.Lstat(filepath.Join(root, toolStoreMarker)); os.IsNotExist(err) {
		return nil, verifyToolStoreInitialContents(root)
	} else if err != nil {
		return nil, err
	}
	catalog, err := prepareExistingToolSnapshotStore(root)
	if err != nil {
		return nil, err
	}
	defer catalog.Close()
	selection, err := readToolGenerationSelection(root)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	report := reconcileBootstrapSelection(ctx, root, selection, maxBytes)
	return &report, nil
}

func reconcileBootstrapSelection(ctx context.Context, root string, selection ToolGenerationSelection, maxBytes int64) (report ToolGenerationUpdateReport) {
	report.SchemaVersion = 1
	manifest, err := verifySelectedToolGeneration(ctx, root, selection, maxBytes)
	if err != nil {
		report.fail(err)
		return report
	}
	var total int64
	for _, entry := range manifest.Tree.Entries {
		if entry.Kind == "file" {
			total += entry.Bytes
		}
	}
	count := len(manifest.Tree.Entries)
	report.Generation = ToolSnapshotReport{SchemaVersion: 1, Source: root, Destination: filepath.Join(root, toolGenerationDirectory, selection.ID), ID: selection.ID, Completion: "generation-v1", Published: true, Reused: true, Bytes: &total, Entries: &count}
	report.Selected = true
	if err := syncToolDirectory(root); err != nil {
		report.fail(err)
	}
	return report
}

func validateToolBootstrapStore(root string, maxBytes int64) error {
	if maxBytes <= 0 {
		return fmt.Errorf("tool bootstrap requires a positive byte bound")
	}
	return validateToolNativeStoreRoot(root)
}

func validateToolNativeStoreRoot(root string) error {
	if len(root) > 4096 || !filepath.IsAbs(root) || filepath.Clean(root) != root || filepath.Dir(root) == root {
		return fmt.Errorf("tool store requires a canonical non-root path")
	}
	parent := filepath.Dir(root)
	resolved, err := filepath.EvalSymlinks(parent)
	if err != nil || resolved != parent {
		return fmt.Errorf("tool bootstrap store has missing or aliased ancestry")
	}
	if info, err := os.Lstat(root); err == nil && !info.IsDir() || err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("tool bootstrap store is not a directory")
	}
	return nil
}
