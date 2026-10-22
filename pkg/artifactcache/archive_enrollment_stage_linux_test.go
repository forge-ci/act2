//go:build linux

package artifactcache

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func pendingArchiveStage(t *testing.T) (*archiveEnrollmentHistory, transferLease) {
	t.Helper()
	source, root := archiveEnrollmentFixture(t, 1)
	enrollArchiveTest(t, source, root, ArchiveEnrollmentFence)
	report := ArchiveEnrollmentReport{Source: source, Root: root, Namespace: "repo", MaxBytes: 160}
	db, h, err := loadArchiveHistory(context.Background(), &report, false)
	require.NoError(t, err)
	h.ImportStarted = true
	require.NoError(t, storeArchiveHistory(db, h))
	require.NoError(t, db.Close())
	stage, lease, err := archiveImportStages(context.Background(), h).create(root, filepath.Join(root, "repo"))
	require.NoError(t, err)
	require.Equal(t, h.stage(), stage)
	return h, lease
}
func TestArchiveEnrollmentResumesPartialStageWithoutFollowingLinks(t *testing.T) {
	h, lease := pendingArchiveStage(t)
	outside := filepath.Join(t.TempDir(), "unrelated")
	require.NoError(t, os.WriteFile(outside, []byte("preserve"), 0600))
	require.NoError(t, os.Symlink(outside, filepath.Join(h.stage(), "foreign-link")))
	require.NoError(t, os.Mkdir(filepath.Join(h.stage(), "cache"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(h.stage(), "cache", "unfinished"), []byte("partial"), 0600))
	require.NoError(t, lease.Close())
	require.True(t, enrollArchiveTest(t, h.Source, h.Root, ArchiveEnrollmentComplete).Ready)
	contents, err := os.ReadFile(outside)
	require.NoError(t, err)
	require.Equal(t, "preserve", string(contents))
	_, err = os.Lstat(h.stage())
	require.True(t, os.IsNotExist(err), "publication must consume the single controlled stage")
	receipt, err := ReadImportPublicationReceipt(filepath.Join(h.Root, h.Namespace))
	require.NoError(t, err)
	require.EqualValues(t, 1, receipt.ImportedCount)
}
func TestArchiveEnrollmentBoundedStageReclamationResumes(t *testing.T) {
	h, lease := pendingArchiveStage(t)
	for index := 0; index < 10; index++ {
		require.NoError(t, os.WriteFile(filepath.Join(h.stage(), fmt.Sprint("partial-", index)), []byte("partial"), 0600))
	}
	err := reclaimArchiveStageWithBudget(context.Background(), h, 3)
	require.ErrorContains(t, err, "entry budget exhausted")
	require.NoError(t, requireToolEnrollmentMarker(h.stage(), archiveStageMarker, archiveFenceBytes(h)))
	names, err := filepath.Glob(filepath.Join(h.stage(), "partial-*"))
	require.NoError(t, err)
	require.NotEmpty(t, names)
	require.Less(t, len(names), 10)
	require.NoError(t, lease.Close())
	require.True(t, enrollArchiveTest(t, h.Source, h.Root, ArchiveEnrollmentComplete).Ready)
}
func TestArchiveEnrollmentRefusesUnownedNonemptyStage(t *testing.T) {
	source, root := archiveEnrollmentFixture(t, 1)
	enrollArchiveTest(t, source, root, ArchiveEnrollmentFence)
	report := ArchiveEnrollmentReport{Source: source, Root: root, Namespace: "repo", MaxBytes: 160}
	db, h, err := loadArchiveHistory(context.Background(), &report, false)
	require.NoError(t, err)
	require.NoError(t, db.Close())
	require.NoError(t, os.Mkdir(root, 0700))
	require.NoError(t, os.Mkdir(h.stage(), 0700))
	unknown := filepath.Join(h.stage(), "unrelated")
	require.NoError(t, os.WriteFile(unknown, []byte("preserve"), 0600))
	refused := EnrollArchive(context.Background(), source, root, "repo", 160, ArchiveEnrollmentComplete)
	require.True(t, refused.Partial)
	require.Contains(t, refused.Error, "no ownership witness")
	contents, err := os.ReadFile(unknown)
	require.NoError(t, err)
	require.Equal(t, "preserve", string(contents))
}

func TestArchiveEnrollmentDistinguishesRetryableContentionFromInvalidPolicy(t *testing.T) {
	h, lease := pendingArchiveStage(t)
	busy := EnrollArchive(context.Background(), h.Source, h.Root, h.Namespace, h.MaxBytes, ArchiveEnrollmentComplete)
	require.True(t, busy.Partial)
	require.False(t, busy.Ready)
	encoded, err := json.Marshal(busy)
	require.NoError(t, err)
	require.Contains(t, string(encoded), `"retryable":true`, "lock contention must be a typed scheduling result")
	require.NoError(t, lease.Close())
	invalid := EnrollArchive(context.Background(), h.Source, h.Root, h.Namespace, h.MaxBytes+1, ArchiveEnrollmentComplete)
	require.True(t, invalid.Partial)
	encoded, err = json.Marshal(invalid)
	require.NoError(t, err)
	require.Contains(t, string(encoded), `"retryable":false`, "policy conflicts must not become wait loops")
}

func TestArchiveEnrollmentJournalContentionHasBoundedRetryableReply(t *testing.T) {
	source, root := archiveEnrollmentFixture(t, 1)
	enrollArchiveTest(t, source, root, ArchiveEnrollmentFence)
	report := ArchiveEnrollmentReport{Source: source, Root: root, Namespace: "repo", MaxBytes: 160}
	db, _, err := loadArchiveHistory(context.Background(), &report, false)
	require.NoError(t, err)
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	started := time.Now()
	busy := EnrollArchive(ctx, source, root, "repo", 160, ArchiveEnrollmentComplete)
	require.Less(t, time.Since(started), 800*time.Millisecond, "journal contention must not consume the whole operation deadline")
	require.True(t, busy.Partial)
	require.True(t, busy.Retryable)
	require.False(t, busy.Ready)
}
