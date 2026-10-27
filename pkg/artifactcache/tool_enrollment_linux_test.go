//go:build linux

package artifactcache

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.etcd.io/bbolt"
	"golang.org/x/sys/unix"
)

func TestToolEnrollmentNeverRebootstrapsLostWarmSelection(t *testing.T) {
	source, root := toolBootstrapFixture(t)
	first := EnrollToolGeneration(context.Background(), source, root, 100)
	require.False(t, first.Partial, first.Error)
	require.True(t, first.Enrolled)
	require.NoError(t, os.Remove(filepath.Join(root, toolGenerationCurrent)))
	again := EnrollToolGeneration(context.Background(), source, root, 100)
	require.True(t, again.Partial, "ordinary creation silently re-enrolled a lost warm selection")
	require.False(t, again.Enrolled)
	_, err := os.Lstat(filepath.Join(root, toolGenerationCurrent))
	require.True(t, os.IsNotExist(err), "refusal must preserve missing selection for reconciliation")
}

func TestToolEnrollmentColdHistorySurvivesStoreLoss(t *testing.T) {
	base := t.TempDir()
	source, root := filepath.Join(base, "legacy"), filepath.Join(base, "store")
	first := EnrollToolGeneration(context.Background(), source, root, 100)
	require.False(t, first.Partial, first.Error)
	require.True(t, first.Enrolled)
	require.Equal(t, "cold", first.Mode)
	lease, err := AcquireToolGenerationLease(context.Background(), root, first.Generation, 100)
	require.NoError(t, err)
	require.NoError(t, lease.Close())
	require.NoError(t, os.Rename(root, root+".lost"))
	again := EnrollToolGeneration(context.Background(), source, root, 100)
	require.True(t, again.Partial, "lost native tree must not become a new empty generation")
	require.False(t, again.Enrolled)
	_, err = os.Lstat(root)
	require.True(t, os.IsNotExist(err))
}

func TestToolEnrollmentReconcilesPendingReadyWithoutSource(t *testing.T) {
	source, root := toolBootstrapFixture(t)
	first := enrollToolGenerationWithReady(context.Background(), source, root, 100, func(*bbolt.DB, toolEnrollmentHistory) error {
		return fmt.Errorf("injected history acknowledgement loss")
	})
	require.True(t, first.Partial)
	require.False(t, first.Enrolled)
	selection, err := CurrentToolGeneration(context.Background(), root, 100)
	require.NoError(t, err)
	moveEnrolledLegacyPayload(t, source, root)
	again := EnrollToolGeneration(context.Background(), source, root, 100)
	require.False(t, again.Partial, again.Error)
	require.True(t, again.Enrolled)
	require.Equal(t, selection.ID, again.Generation)
	for range 20 {
		repeat := EnrollToolGeneration(context.Background(), source, root, 100)
		require.False(t, repeat.Partial, repeat.Error)
		require.Equal(t, selection.ID, repeat.Generation)
	}
	info, err := os.Stat(toolEnrollmentHistoryPath(root))
	require.NoError(t, err)
	require.LessOrEqual(t, info.Size(), int64(1<<20), "repeated engine planning must not append enrollment history")
}

func moveEnrolledLegacyPayload(t *testing.T, source, root string) {
	t.Helper()
	db, _, err := openToolEnrollmentHistory(context.Background(), root)
	require.NoError(t, err)
	history, err := loadToolEnrollmentHistory(db, source, root)
	require.NoError(t, err)
	require.NotNil(t, history)
	require.NoError(t, db.Close())
	require.NoError(t, requireToolEnrollmentSourceFence(*history))
	snapshot := toolEnrollmentSourceSnapshot(*history)
	require.NoError(t, os.Rename(snapshot, snapshot+".gone"))
}

func TestToolEnrollmentRefusesUnrecordedExistingNativeStore(t *testing.T) {
	source, root := toolBootstrapFixture(t)
	explicit := BootstrapToolGeneration(context.Background(), source, root, 100)
	require.False(t, explicit.Partial, explicit.Error)
	report := EnrollToolGeneration(context.Background(), source, root, 100)
	require.True(t, report.Partial)
	require.Contains(t, report.Error, "without enrollment history")
	selection, err := CurrentToolGeneration(context.Background(), root, 100)
	require.NoError(t, err)
	require.Equal(t, explicit.Generation.ID, selection.ID)
}

func TestToolEnrollmentDoesNotHashPayloadDuringReadyPlanning(t *testing.T) {
	source, root := toolBootstrapFixture(t)
	first := EnrollToolGeneration(context.Background(), source, root, 100)
	require.False(t, first.Partial, first.Error)
	payload := filepath.Join(root, toolGenerationDirectory, first.Generation, "tree", "Go/1/x64/tool")
	require.NoError(t, os.WriteFile(payload, []byte("corrupt"), 0600))
	plan := EnrollToolGeneration(context.Background(), source, root, 100)
	require.False(t, plan.Partial, plan.Error)
	require.Equal(t, first.Generation, plan.Generation, "enrollment is an identity hint, not payload acceptance")
	lease, err := AcquireToolGenerationLease(context.Background(), root, plan.Generation, 100)
	require.Error(t, err, "the original reader admission must still reject corrupt payload")
	if lease != nil {
		require.NoError(t, lease.Close())
	}
}

func TestToolEnrollmentCreatesSourceRecoveryEpochThree(t *testing.T) {
	for _, mode := range []string{"cold", "warm"} {
		t.Run(mode, func(t *testing.T) {
			source, root := toolBootstrapFixture(t)
			if mode == "cold" {
				require.NoError(t, os.Rename(source, source+".gone"))
			}
			report := EnrollToolGeneration(context.Background(), source, root, 100)
			require.False(t, report.Partial, report.Error)
			marker, err := os.ReadFile(filepath.Join(root, toolStoreMarker))
			require.NoError(t, err)
			require.Equal(t, toolStoreSourceRecoveryMagic, string(marker), "ordinary enrollment must support producer source finalization")
		})
	}
}

func TestToolEnrollmentRefusesHistoryAliasesAndSpecialFiles(t *testing.T) {
	for _, kind := range []string{"symlink", "hardlink", "fifo", "oversized", "corrupt", "empty"} {
		t.Run(kind, func(t *testing.T) {
			source, root := toolBootstrapFixture(t)
			path := toolEnrollmentHistoryPath(root)
			switch kind {
			case "symlink":
				require.NoError(t, os.Symlink(source, path))
			case "hardlink":
				other := filepath.Join(filepath.Dir(root), "other")
				require.NoError(t, os.WriteFile(other, []byte("preserved"), 0600))
				require.NoError(t, os.Link(other, path))
			case "fifo":
				require.NoError(t, unix.Mkfifo(path, 0600))
			case "oversized":
				require.NoError(t, os.WriteFile(path, make([]byte, (1<<20)+1), 0600))
			case "corrupt":
				require.NoError(t, os.WriteFile(path, []byte("corrupt"), 0600))
			case "empty":
				require.NoError(t, os.WriteFile(path, nil, 0600))
				require.NoError(t, os.Rename(source, source+".gone"))
			}
			report := EnrollToolGeneration(context.Background(), source, root, 100)
			require.True(t, report.Partial)
			_, err := os.Lstat(root)
			require.True(t, os.IsNotExist(err), "rejected history must not initialize native state")
		})
	}
}

func TestToolEnrollmentRejectsHistorySourceCollisionBeforeMutation(t *testing.T) {
	root := filepath.Join(t.TempDir(), "store")
	source := toolEnrollmentHistoryPath(root)
	report := EnrollToolGeneration(context.Background(), source, root, 100)
	require.True(t, report.Partial)
	entries, err := os.ReadDir(filepath.Dir(root))
	require.NoError(t, err)
	require.Empty(t, entries, "invalid source must not be created as enrollment history")
}

func TestToolEnrollmentReadyRefusesDowngradedOrReplacedStore(t *testing.T) {
	for _, mutation := range []string{"epoch1", "epoch2", "replacement"} {
		t.Run(mutation, func(t *testing.T) {
			source, root := toolBootstrapFixture(t)
			first := EnrollToolGeneration(context.Background(), source, root, 100)
			require.False(t, first.Partial, first.Error)
			if mutation == "replacement" {
				require.NoError(t, os.Rename(root, root+".original"))
				other := EnrollToolRecoverySourceStore(context.Background(), root)
				require.False(t, other.Partial, other.Error)
				empty := ToolGenerationSpec{SchemaVersion: 1, Installs: []ToolGenerationInstall{}}
				published := PublishToolGeneration(context.Background(), root, empty, 100)
				require.False(t, published.Partial, published.Error)
				selected := InitializeToolGeneration(context.Background(), root, empty, 100)
				require.False(t, selected.Partial, selected.Error)
			} else {
				marker := toolStoreMagic
				if mutation == "epoch2" {
					marker = "bosn-tool-snapshots-v2\n"
				}
				require.NoError(t, os.WriteFile(filepath.Join(root, toolStoreMarker), []byte(marker), 0600))
			}
			again := EnrollToolGeneration(context.Background(), source, root, 100)
			require.True(t, again.Partial, "Ready admission accepted a changed producer store")
			require.False(t, again.Enrolled)
		})
	}
}

func TestToolEnrollmentContentionHonorsCancellation(t *testing.T) {
	source, root := toolBootstrapFixture(t)
	db, _, err := openToolEnrollmentHistory(context.Background(), root)
	require.NoError(t, err)
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	start := time.Now()
	report := EnrollToolGeneration(ctx, source, root, 100)
	require.True(t, report.Partial)
	require.Contains(t, report.Error, "deadline exceeded")
	require.Less(t, time.Since(start), time.Second)
}

func TestToolEnrollmentReportsSelectionCatalogContention(t *testing.T) {
	source, root := toolBootstrapFixture(t)
	first := EnrollToolGeneration(context.Background(), source, root, 100)
	require.False(t, first.Partial, first.Error)
	catalog, err := prepareExistingToolSnapshotStore(root)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	again := EnrollToolGeneration(ctx, source, root, 100)
	require.True(t, again.Partial)
	require.Contains(t, again.Error, "deadline exceeded")
	require.NoError(t, catalog.Close())
	retried := EnrollToolGeneration(context.Background(), source, root, 100)
	require.False(t, retried.Partial, retried.Error)
	require.True(t, retried.Enrolled)
	require.Equal(t, first.Generation, retried.Generation)
}

func TestToolEnrollmentBudgetWaitsForHistoryAndKeepsFenceOnRetry(t *testing.T) {
	base := t.TempDir()
	source, root := filepath.Join(base, "legacy"), filepath.Join(base, "store")
	fenced := FenceToolEnrollmentSource(context.Background(), source, root, 100)
	require.False(t, fenced.Partial, fenced.Error)
	require.True(t, fenced.SourceFenced)
	fence, err := os.ReadFile(source)
	require.NoError(t, err)
	lock, err := bbolt.Open(toolEnrollmentHistoryPath(root), 0600, nil)
	require.NoError(t, err)
	short, err := WithToolOperationBudget(context.Background(), time.Second)
	require.NoError(t, err)
	failed := CompleteToolEnrollment(short, source, root, 100)
	require.True(t, failed.Partial)
	require.Contains(t, failed.Error, "deadline exceeded")
	require.NoError(t, lock.Close())
	after, err := os.ReadFile(source)
	require.NoError(t, err)
	require.Equal(t, fence, after)

	long, err := WithToolOperationBudget(context.Background(), 30*time.Second)
	require.NoError(t, err)
	entered := false
	done := enrollToolGenerationOperation(long, source, root, 100, toolEnrollmentRequireFenced,
		func(db *bbolt.DB, history toolEnrollmentHistory) error {
			entered = true
			time.Sleep(1500 * time.Millisecond)
			return storeToolEnrollmentHistory(db, history)
		})
	require.True(t, entered, "the real enrollment reached its slow readiness step")
	require.False(t, done.Partial, done.Error)
	require.True(t, done.Enrolled)
	repeated := CompleteToolEnrollment(context.Background(), source, root, 100)
	require.False(t, repeated.Partial, repeated.Error)
	require.Equal(t, done.Generation, repeated.Generation)
}
