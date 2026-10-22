//go:build linux

package artifactcache

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestToolRecoverySourceEnrollmentRefusesOlderStores(t *testing.T) {
	for _, warm := range []bool{false, true} {
		t.Run(map[bool]string{false: "empty-v1", true: "warm-v2"}[warm], func(t *testing.T) {
			var root string
			if warm {
				root, _, _ = toolRecoveryPublicationFixture(t)
			} else {
				root = filepath.Join(t.TempDir(), "store")
				catalog, err := prepareToolSnapshotStore(root)
				require.NoError(t, err)
				require.NoError(t, catalog.Close())
			}
			path := filepath.Join(root, toolStoreMarker)
			before, err := os.ReadFile(path)
			require.NoError(t, err)
			report := EnrollToolRecoverySourceStore(context.Background(), root)
			require.True(t, report.Partial)
			require.False(t, report.Enrolled)
			after, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Equal(t, before, after, "enrollment must not upgrade an existing store")
		})
	}
}

func TestToolRecoverySourceEnrollmentRetryAndCancellation(t *testing.T) {
	root := filepath.Join(t.TempDir(), "store")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.True(t, EnrollToolRecoverySourceStore(ctx, root).Partial)
	_, err := os.Lstat(root)
	require.ErrorIs(t, err, os.ErrNotExist)
	first := EnrollToolRecoverySourceStore(context.Background(), root)
	require.True(t, first.Enrolled, first.Error)
	before, err := os.Stat(filepath.Join(root, toolStoreLock))
	require.NoError(t, err)
	retried := EnrollToolRecoverySourceStore(context.Background(), root)
	require.True(t, retried.Enrolled, retried.Error)
	after, err := os.Stat(filepath.Join(root, toolStoreLock))
	require.NoError(t, err)
	require.True(t, os.SameFile(before, after))
	alias := root + "-alias"
	require.NoError(t, os.Symlink(root, alias))
	require.True(t, EnrollToolRecoverySourceStore(context.Background(), alias).Partial)
}

func TestToolRecoverySourceEnrollmentRefusesAliasedAncestorBeforeMutation(t *testing.T) {
	base := t.TempDir()
	physical := filepath.Join(base, "real")
	require.NoError(t, os.Mkdir(physical, 0700))
	alias := filepath.Join(base, "alias")
	require.NoError(t, os.Symlink(physical, alias))
	report := EnrollToolRecoverySourceStore(context.Background(), filepath.Join(alias, "store"))
	require.True(t, report.Partial, "enrollment must refuse aliased ancestry like bootstrap")
	_, err := os.Lstat(filepath.Join(physical, "store"))
	require.ErrorIs(t, err, os.ErrNotExist, "refusal must precede store creation")
}
