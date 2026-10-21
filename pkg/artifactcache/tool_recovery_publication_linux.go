//go:build linux

package artifactcache

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"time"
)

func syncToolRecoverySelection(root string) error { return syncToolDirectory(root) }

func recoveryPublicationReceipt(root string, pin ToolRecoveryPin, c ToolRecoveryCompletion) ToolRecoveryPublicationReport {
	return ToolRecoveryPublicationReport{SchemaVersion: 1, Root: root, Pin: pin, Generation: c.Generation, CompletedAt: c.CompletedAt, Completed: true}
}

func requireLiveToolRecovery(ctx context.Context, root string, pin ToolRecoveryPin) error {
	now := time.Now().UTC()
	if pin.CreatedAt.After(now) || !pin.ExpiresAt.After(now) {
		return fmt.Errorf("recovery intent is future-created or expired")
	}
	records, err := readToolRecoveryPinRecords(ctx, root, now)
	if err != nil {
		return err
	}
	existing, ok := records[pin.Owner]
	if !ok || !sameToolRecoveryPin(existing, pin) {
		return fmt.Errorf("recovery reference is absent or differs from frozen intent")
	}
	return nil
}

// Receipt lookup intentionally precedes source access and live-pin admission:
// an acknowledged publication is reconciled, never repeated after release.
func reconcileToolRecovery(root string, selection ToolGenerationSelection, pin ToolRecoveryPin) (*ToolRecoveryPublicationReport, error) {
	completion, err := completedToolRecovery(root, selection, pin)
	if err != nil || completion == nil {
		return nil, err
	}
	if err := syncToolDirectory(root); err != nil {
		return nil, err
	}
	if selection.Recovery == nil || selection.Recovery.Pin.Owner != pin.Owner {
		if err := syncToolDirectory(filepath.Join(root, toolRecoveryCompletionDirectory)); err != nil {
			return nil, err
		}
	}
	report := recoveryPublicationReceipt(root, pin, *completion)
	return &report, nil
}

// A dedicated recovery helper owns at most one view. Keep failed cleanup
// strongly reachable (including its reader) until cleanup succeeds or the
// process exits. Serial admission bounds retained mounts and descriptors to one.
var recoveryViewOwner struct {
	sync.Mutex
	pending *toolRecoveryPendingView
}

type toolRecoveryPendingView struct {
	reader ToolGenerationLease
	close  func() error
}

func cleanupPendingToolRecoveryView() error {
	pending := recoveryViewOwner.pending
	if pending == nil {
		return nil
	}
	if err := pending.close(); err != nil {
		return fmt.Errorf("recovery view cleanup: %w", err)
	}
	if err := pending.reader.Close(); err != nil {
		return err
	}
	recoveryViewOwner.pending = nil
	return nil
}

func publishToolRecoveryView(ctx context.Context, root string, pin ToolRecoveryPin, maxBytes int64, openView ToolRecoverySourceFactory, syncSelection func(string) error) (report ToolRecoveryPublicationReport) {
	report = ToolRecoveryPublicationReport{SchemaVersion: 1, Root: root, Pin: pin}
	if !recoveryViewOwner.TryLock() {
		report.fail(fmt.Errorf("recovery view already in use by this process"))
		return report
	}
	defer recoveryViewOwner.Unlock()
	if err := cleanupPendingToolRecoveryView(); err != nil {
		report.fail(err)
		return report
	}
	ctx, cancel := context.WithTimeout(ctx, 180*time.Second)
	defer cancel()
	receipt, err := admitToolRecoveryPublication(ctx, root, pin, maxBytes)
	if err != nil {
		report.fail(err)
		return report
	}
	if receipt != nil {
		return *receipt
	}
	reader, err := AcquireToolGenerationLease(ctx, root, pin.Generation, maxBytes)
	if err != nil {
		report.fail(err)
		return report
	}
	readerTransferred := false
	defer func() {
		if !readerTransferred {
			_ = reader.Close()
		}
	}()
	original, _, err := readToolGenerationManifest(filepath.Join(root, toolGenerationDirectory, pin.Generation), pin.Generation)
	if err != nil {
		report.fail(err)
		return report
	}
	if openView == nil {
		report.fail(fmt.Errorf("recovery source factory missing"))
		return report
	}
	view, err := openView(ctx)
	// A factory that failed after mounting must still return its cleanup handle.
	if view.Close != nil {
		readerTransferred = true
		recoveryViewOwner.pending = &toolRecoveryPendingView{reader: reader, close: view.Close}
		defer func() {
			if err := cleanupPendingToolRecoveryView(); err != nil {
				report.fail(err)
			}
		}()
	}
	if err != nil {
		report.fail(err)
		return report
	}
	if view.Close == nil {
		report.fail(fmt.Errorf("recovery source closer missing"))
		return report
	}
	plans, err := inspectToolRecoveryUpdates(ctx, root, view.Path, original.Installs, maxBytes)
	if err != nil {
		report.fail(err)
		return report
	}
	catalog, err := prepareToolGenerationUpdateCatalog(ctx, root)
	if err != nil {
		report.fail(err)
		return report
	}
	defer catalog.Close()
	return publishToolRecoveryLocked(ctx, catalog, root, pin, maxBytes, plans, syncSelection)
}

func publishToolRecoveryLocked(ctx context.Context, catalog transferLease, root string, pin ToolRecoveryPin, maxBytes int64, plans []toolRecoveryObjectPlan, syncSelection func(string) error) (report ToolRecoveryPublicationReport) {
	report = ToolRecoveryPublicationReport{SchemaVersion: 1, Root: root, Pin: pin}
	selection, err := readToolGenerationSelection(root)
	if err != nil {
		report.fail(err)
		return report
	}
	if receipt, err := reconcileToolRecovery(root, selection, pin); err != nil {
		report.fail(err)
		return report
	} else if receipt != nil {
		return *receipt
	}
	if err := requireLiveToolRecovery(ctx, root, pin); err != nil {
		report.fail(err)
		return report
	}
	latest, err := verifySelectedToolGeneration(ctx, root, selection, maxBytes)
	if err != nil {
		report.fail(err)
		return report
	}
	if selection.Recovery != nil {
		if err := archiveToolRecoveryCompletion(ctx, catalog, root, *selection.Recovery); err != nil {
			report.fail(err)
			return report
		}
	}
	updates, pending, err := publishToolRecoveryObjects(ctx, catalog, root, plans, maxBytes)
	if err != nil {
		report.PendingStage = pending
		report.fail(err)
		return report
	}
	merged := mergeToolGenerationInstalls(latest.Installs, updates)
	installs, err := validateToolGenerationSpec(root, merged, maxBytes)
	if err != nil {
		report.fail(err)
		return report
	}
	generation := publishToolGenerationLocked(ctx, catalog, root, installs, maxBytes)
	if generation.Partial || !generation.Published {
		report.PendingStage = generation.PendingStage
		report.fail(fmt.Errorf("recovery generation publication incomplete: %s", generation.Error))
		return report
	}
	// Publication is not allowed to acquire fresh authority after finite expiry.
	if err := requireLiveToolRecovery(ctx, root, pin); err != nil {
		report.fail(err)
		return report
	}
	completion := ToolRecoveryCompletion{Pin: pin, Generation: generation.ID, CompletedAt: time.Now().UTC()}
	if err := completion.validate(); err != nil {
		report.fail(err)
		return report
	}
	report = recoveryPublicationReceipt(root, pin, completion)
	selected, pending, err := writeToolGenerationSelection(ctx, catalog, root, ToolGenerationSelection{SchemaVersion: 1, ID: generation.ID, Recovery: &completion}, syncSelection)
	report.PendingStage = pending
	report.Completed = selected && err == nil
	if err != nil {
		report.fail(err)
	}
	return report
}

func admitToolRecoveryPublication(ctx context.Context, root string, pin ToolRecoveryPin, maxBytes int64) (*ToolRecoveryPublicationReport, error) {
	if err := pin.validate(); err != nil {
		return nil, err
	}
	guard := ToolGenerationSpec{SchemaVersion: 1, Installs: []ToolGenerationInstall{{Path: "recovery", ObjectID: pin.Generation}}}
	if _, err := validateToolGenerationSpec(root, guard, maxBytes); err != nil {
		return nil, err
	}
	catalog, err := prepareExistingToolSnapshotStore(root)
	if err != nil {
		return nil, err
	}
	defer catalog.Close()
	selection, err := readToolGenerationSelection(root)
	if err != nil {
		return nil, err
	}
	receipt, err := reconcileToolRecovery(root, selection, pin)
	if err != nil || receipt != nil {
		return receipt, err
	}
	return nil, requireLiveToolRecovery(ctx, root, pin)
}

func publishToolRecoveryObjects(ctx context.Context, catalog transferLease, root string, plans []toolRecoveryObjectPlan, maxBytes int64) ([]ToolGenerationInstall, string, error) {
	updates := make([]ToolGenerationInstall, 0, len(plans))
	for _, plan := range plans {
		object := publishInspectedToolSnapshot(ctx, catalog, plan.report.Source, root, maxBytes, plan.report, plan.manifest, plan.data, syncToolDirectory)
		if object.Partial || !object.Published {
			return nil, object.PendingStage, fmt.Errorf("recovery object publication incomplete: %s", object.Error)
		}
		updates = append(updates, ToolGenerationInstall{Path: plan.path, ObjectID: object.ID})
	}
	return updates, "", nil
}
