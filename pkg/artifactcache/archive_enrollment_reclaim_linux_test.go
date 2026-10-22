//go:build linux

package artifactcache

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func requireArchiveSourceReclaimed(t *testing.T, snapshot string) {
	t.Helper()
	entries, err := os.ReadDir(snapshot)
	require.NoError(t, err)
	require.Len(t, entries, 1, "durable Ready must reclaim the source payload, keeping its ownership witness")
	require.Equal(t, archiveSourceIdentity, entries[0].Name())
}

func TestArchiveCompletionReclaimsOnlyTheVerifiedSource(t *testing.T) {
	source, root := archiveEnrollmentFixture(t, 2)
	fence := enrollArchiveTest(t, source, root, ArchiveEnrollmentFence)
	enrollArchiveTest(t, source, root, ArchiveEnrollmentComplete)
	requireArchiveSourceReclaimed(t, fence.Snapshot)
	require.True(t, enrollArchiveTest(t, source, root, ArchiveEnrollmentRequire).Ready)
	policy := DefaultPolicy()
	policy.CohortRoot = root
	h, err := StartHandlerWithPolicy(filepath.Join(root, "repo"), "", "127.0.0.1", 0, nil, policy)
	require.NoError(t, err)
	defer h.Close()
	assertArchiveHit(t, h, "warm-1")
}

func TestIdleCohortMaintenanceResumesArchiveSourceReclamation(t *testing.T) {
	h := readyArchiveBeforeReclaim(t)
	pass := MaintainCohort(context.Background(), h.Root, 160, DefaultPolicy())
	require.False(t, pass.Partial, pass.Errors)
	requireArchiveSourceReclaimed(t, h.snapshot())
	require.True(t, enrollArchiveTest(t, h.Source, h.Root, ArchiveEnrollmentRequire).Ready)
}

func readyArchiveBeforeReclaim(t *testing.T) *archiveEnrollmentHistory {
	t.Helper()
	source, root := archiveEnrollmentFixture(t, 1)
	fence := enrollArchiveTest(t, source, root, ArchiveEnrollmentFence)
	imported := ImportCompleted(context.Background(), fence.Snapshot, root, "repo", 160)
	require.False(t, imported.Partial, imported.Error)
	request := ArchiveEnrollmentReport{Source: source, Root: root, Namespace: "repo", MaxBytes: 160}
	db, history, err := loadArchiveHistory(context.Background(), &request, false)
	require.NoError(t, err)
	// Simulate interruption after durable Ready and before source deletion.
	require.NoError(t, finishInitialArchiveEnrollment(context.Background(), db, history))
	require.NoError(t, db.Close())
	return history
}

func TestArchiveSourceReclamationIsBoundedAndResumable(t *testing.T) {
	h := readyArchiveBeforeReclaim(t)
	for i := range 40 {
		require.NoError(t, os.WriteFile(filepath.Join(h.snapshot(), fmt.Sprintf("extra-%d", i)), []byte("retained"), 0600))
	}
	err := reclaimArchiveSourceWithBudget(context.Background(), h, 5)
	require.ErrorIs(t, err, errArchiveSourceBudgetPending)
	require.True(t, archiveSourceReclamationPending(err))
	require.NoError(t, requireArchiveSnapshot(h), "partial removal must keep ownership evidence")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.Error(t, reclaimArchiveSource(ctx, h))
	pass := MaintainCohort(context.Background(), h.Root, 160, DefaultPolicy())
	require.False(t, pass.Partial, pass.Errors)
	requireArchiveSourceReclaimed(t, h.snapshot())
}

func TestArchiveSourceReclamationPreservesEveryUnprovenBoundary(t *testing.T) {
	for _, fault := range []string{"pending", "source witness", "source fence", "source symlink", "destination"} {
		t.Run(fault, func(t *testing.T) {
			h := readyArchiveBeforeReclaim(t)
			switch fault {
			case "pending":
				h.Ready = false
			case "source witness":
				require.NoError(t, os.WriteFile(filepath.Join(h.snapshot(), archiveSourceIdentity), []byte("foreign"), 0600))
			case "source fence":
				require.NoError(t, os.WriteFile(h.Source, []byte("foreign"), 0600))
			case "source symlink":
				require.NoError(t, os.Rename(h.snapshot(), h.snapshot()+".held"))
				require.NoError(t, os.Symlink(h.snapshot()+".held", h.snapshot()))
			case "destination":
				require.NoError(t, os.Rename(filepath.Join(h.Root, h.Namespace), filepath.Join(h.Root, "held")))
			}
			err := reclaimArchiveSource(context.Background(), h)
			require.Error(t, err)
			_, err = os.Stat(filepath.Join(h.snapshot(), "bolt.db"))
			require.NoError(t, err, "refusal must not reclaim source data")
		})
	}
}

func TestIdleCohortMaintenanceProtectsPendingArchivePublication(t *testing.T) {
	source, root := archiveEnrollmentFixture(t, 1)
	fence := enrollArchiveTest(t, source, root, ArchiveEnrollmentFence)
	imported := ImportCompleted(context.Background(), fence.Snapshot, root, "repo", 160)
	require.False(t, imported.Partial, imported.Error)
	pass := MaintainCohort(context.Background(), root, 1, DefaultPolicy())
	require.True(t, pass.Partial, "a pending first publication cannot become ordinary eviction authority")
	audit := AuditStore(context.Background(), filepath.Join(root, "repo"), 0)
	require.False(t, audit.Partial, audit.Errors)
	require.EqualValues(t, 1, *audit.EntryCount)
	require.EqualValues(t, 80, *audit.ArchiveBytes)
	_, err := os.Stat(filepath.Join(fence.Snapshot, "bolt.db"))
	require.NoError(t, err)
}

func TestAPendingPublicationNoLongerBlocksTheRestOfTheCohort(t *testing.T) {
	source, root := archiveEnrollmentFixture(t, 1)
	fence := enrollArchiveTest(t, source, root, ArchiveEnrollmentFence)
	imported := ImportCompleted(context.Background(), fence.Snapshot, root, "repo", 160)
	require.False(t, imported.Partial, imported.Error)
	other := filepath.Join(filepath.Dir(source), "other")
	require.NoError(t, os.Mkdir(other, 0700))
	storage, err := NewStorage(filepath.Join(other, "cache"))
	require.NoError(t, err)
	seedAuditEntries(t, &Handler{dir: other, storage: storage}, 2)
	for _, phase := range []ArchiveEnrollmentPhase{ArchiveEnrollmentFence, ArchiveEnrollmentComplete} {
		r := EnrollArchive(context.Background(), other, root, "other", 160, phase)
		require.False(t, r.Partial, r.Error)
	}
	pass := MaintainCohort(context.Background(), root, 1, DefaultPolicy())
	require.True(t, pass.Partial, "the pending namespace stays outside the budget")
	require.Equal(t, []string{"repo"}, pass.PendingNamespaces)
	require.Len(t, pass.Namespaces, 1, pass.Errors)
	require.Equal(t, "other", filepath.Base(pass.Namespaces[0].Namespace))
	audit := AuditStore(context.Background(), filepath.Join(root, "repo"), 0)
	require.False(t, audit.Partial, audit.Errors)
	require.EqualValues(t, 80, *audit.ArchiveBytes, "the pending publication is untouched")
	_, err = os.Stat(filepath.Join(fence.Snapshot, "bolt.db"))
	require.NoError(t, err)
}
