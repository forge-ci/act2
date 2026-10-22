//go:build linux

package artifactcache

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"go.etcd.io/bbolt"
)

func TestToolEnrollmentReclaimsLegacySnapshotAfterReady(t *testing.T) {
	source, root := toolBootstrapFixture(t)
	private := filepath.Join(source, ".incomplete")
	require.NoError(t, os.Mkdir(private, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(private, "discard"), []byte("private unfinished data"), 0600))
	external := filepath.Join(filepath.Dir(source), "unrelated")
	require.NoError(t, os.WriteFile(external, []byte("preserve"), 0600))
	require.NoError(t, os.Symlink(external, filepath.Join(private, "alias")))
	report := EnrollToolGeneration(context.Background(), source, root, 100)
	require.False(t, report.Partial, report.Error)
	db, _, err := openToolEnrollmentHistory(context.Background(), root)
	require.NoError(t, err)
	history, err := loadToolEnrollmentHistory(db, source, root)
	require.NoError(t, err)
	require.NoError(t, db.Close())
	require.True(t, history.Ready)
	entries, err := os.ReadDir(toolEnrollmentSourceSnapshot(*history))
	require.NoError(t, err)
	require.Len(t, entries, 1, "migration retained a second payload copy")
	require.Equal(t, toolEnrollmentSourceIdentityName, entries[0].Name())
	data, err := os.ReadFile(external)
	require.NoError(t, err)
	require.Equal(t, "preserve", string(data))
	lease, err := AcquireToolGenerationLease(context.Background(), root, report.Generation, 100)
	require.NoError(t, err)
	require.NoError(t, lease.Close())
}

func TestToolEnrollmentReclaimsDeepPrivateSnapshot(t *testing.T) {
	source, root := toolBootstrapFixture(t)
	deep := filepath.Join(source, ".unfinished")
	for range 80 {
		deep = filepath.Join(deep, "d")
	}
	require.NoError(t, os.MkdirAll(deep, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(deep, "data"), []byte("discard"), 0600))
	report := EnrollToolGeneration(context.Background(), source, root, 100)
	require.False(t, report.Partial, report.Error)
	require.True(t, report.Enrolled)
	db, _, err := openToolEnrollmentHistory(context.Background(), root)
	require.NoError(t, err)
	history, err := loadToolEnrollmentHistory(db, source, root)
	require.NoError(t, err)
	require.NoError(t, db.Close())
	entries, err := os.ReadDir(toolEnrollmentSourceSnapshot(*history))
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, toolEnrollmentSourceIdentityName, entries[0].Name())
}

func TestToolEnrollmentReclamationWaitsForDurableReady(t *testing.T) {
	source, root := toolBootstrapFixture(t)
	report := enrollToolGenerationWithReady(context.Background(), source, root, 100, func(*bbolt.DB, toolEnrollmentHistory) error {
		return fmt.Errorf("Ready not committed")
	})
	require.True(t, report.Partial)
	db, _, err := openToolEnrollmentHistory(context.Background(), root)
	require.NoError(t, err)
	history, err := loadToolEnrollmentHistory(db, source, root)
	require.NoError(t, err)
	require.NoError(t, db.Close())
	require.False(t, history.Ready)
	err = reclaimToolEnrollmentSnapshot(context.Background(), *history)
	require.ErrorContains(t, err, "pending")
	_, err = os.Stat(filepath.Join(toolEnrollmentSourceSnapshot(*history), "Go/1/x64/tool"))
	require.NoError(t, err, "failed Ready must retain its original payload")
}

func TestToolEnrollmentReclamationIsBoundedAndResumes(t *testing.T) {
	directory := t.TempDir()
	for i := range 20 {
		require.NoError(t, os.WriteFile(filepath.Join(directory, fmt.Sprint(i)), []byte("discard"), 0600))
	}
	file, err := os.Open(directory)
	require.NoError(t, err)
	defer file.Close()
	mount, err := enrollmentReclaimMount(int(file.Fd()))
	require.NoError(t, err)
	budget := 3
	done, err := reclaimToolEnrollmentDirectory(context.Background(), file, int(file.Fd()), mount, 0, &budget)
	require.NoError(t, err)
	require.False(t, done)
	require.Zero(t, budget)
	entries, err := os.ReadDir(directory)
	require.NoError(t, err)
	require.Len(t, entries, 17)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	budget = 100
	_, err = reclaimToolEnrollmentDirectory(ctx, file, int(file.Fd()), mount, 0, &budget)
	require.ErrorIs(t, err, context.Canceled)
	entries, err = os.ReadDir(directory)
	require.NoError(t, err)
	require.Len(t, entries, 17)
	require.NoError(t, file.Close())
	file, err = os.Open(directory)
	require.NoError(t, err)
	defer file.Close()
	budget = 100
	done, err = reclaimToolEnrollmentDirectory(context.Background(), file, int(file.Fd()), mount, 0, &budget)
	require.NoError(t, err)
	require.True(t, done)
	entries, err = os.ReadDir(directory)
	require.NoError(t, err)
	require.Empty(t, entries)
}
