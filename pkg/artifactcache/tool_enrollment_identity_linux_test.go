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

func TestToolEnrollmentReadyNeverRepairsMissingOrChangedIdentity(t *testing.T) {
	for _, mutation := range []string{"missing", "empty", "different", "symlink", "hardlink"} {
		t.Run(mutation, func(t *testing.T) {
			source, root := toolBootstrapFixture(t)
			first := EnrollToolGeneration(context.Background(), source, root, 100)
			require.False(t, first.Partial, first.Error)
			path := filepath.Join(root, toolEnrollmentIdentityName)
			original := path + ".original"
			require.NoError(t, os.Rename(path, original))
			switch mutation {
			case "empty":
				require.NoError(t, os.WriteFile(path, nil, 0600))
			case "different":
				require.NoError(t, os.WriteFile(path, []byte("different identity"), 0600))
			case "symlink":
				require.NoError(t, os.Symlink(original, path))
			case "hardlink":
				require.NoError(t, os.Link(original, path))
			}
			again := EnrollToolGeneration(context.Background(), source, root, 100)
			require.True(t, again.Partial, "Ready state must not invent replacement store authority")
			require.False(t, again.Enrolled)
			if mutation == "missing" {
				_, err := os.Lstat(path)
				require.True(t, os.IsNotExist(err))
			}
		})
	}
}

func TestToolEnrollmentPendingRepairsOnlyItsFrozenPartialIdentity(t *testing.T) {
	for _, matching := range []bool{true, false} {
		t.Run(fmt.Sprint(matching), func(t *testing.T) {
			source, root := toolBootstrapFixture(t)
			db, _, err := openToolEnrollmentHistory(context.Background(), root)
			require.NoError(t, err)
			history, err := beginToolEnrollmentHistory(db, source, root)
			require.NoError(t, err)
			require.NoError(t, db.Close())
			require.NoError(t, enrollToolRecoverySourceStore(context.Background(), root))
			partial := toolEnrollmentIdentityBytes(history.StoreIdentity)[:30]
			if !matching {
				partial = []byte("foreign")
			}
			path := filepath.Join(root, toolEnrollmentIdentityName)
			require.NoError(t, os.WriteFile(path, partial, 0600))
			report := EnrollToolGeneration(context.Background(), source, root, 100)
			if matching {
				require.False(t, report.Partial, report.Error)
				require.NoError(t, requireToolEnrollmentIdentity(root, history.StoreIdentity))
			} else {
				require.True(t, report.Partial)
				data, err := os.ReadFile(path)
				require.NoError(t, err)
				require.Equal(t, partial, data, "foreign identity must remain untouched")
			}
		})
	}
}

func TestToolEnrollmentReconcilesAlreadyCommittedReadyAcknowledgementLoss(t *testing.T) {
	source, root := toolBootstrapFixture(t)
	first := enrollToolGenerationWithReady(context.Background(), source, root, 100, func(db *bbolt.DB, history toolEnrollmentHistory) error {
		if err := storeToolEnrollmentHistory(db, history); err != nil {
			return err
		}
		return fmt.Errorf("injected reply loss after Ready commit")
	})
	require.True(t, first.Partial)
	selection, err := CurrentToolGeneration(context.Background(), root, 100)
	require.NoError(t, err)
	moveEnrolledLegacyPayload(t, source, root)
	again := EnrollToolGeneration(context.Background(), source, root, 100)
	require.False(t, again.Partial, again.Error)
	require.Equal(t, selection.ID, again.Generation)
}

func TestToolEnrollmentRefusesUninitializedValidHistory(t *testing.T) {
	base := t.TempDir()
	source, root := filepath.Join(base, "legacy"), filepath.Join(base, "store")
	db, err := bbolt.Open(toolEnrollmentHistoryPath(root), 0600, nil)
	require.NoError(t, err)
	require.NoError(t, db.Close())
	report := EnrollToolGeneration(context.Background(), source, root, 100)
	require.True(t, report.Partial)
	require.Contains(t, report.Error, "no identity")
	_, err = os.Lstat(root)
	require.True(t, os.IsNotExist(err))
}
