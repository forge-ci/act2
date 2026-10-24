//go:build linux

package artifactcache

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// act2#91: a second writer waits for a busy catalog instead of failing with a
// bare bbolt timeout, and its operation context still bounds the wait.
func TestToolStoreWriterWaitsForBusyCatalog(t *testing.T) {
	root := filepath.Join(t.TempDir(), "store")
	held, err := prepareToolSnapshotStore(context.Background(), root)
	require.NoError(t, err)

	done := make(chan error, 1)
	go func() {
		lease, err := prepareToolSnapshotStore(context.Background(), root)
		if err == nil {
			err = lease.Close()
		}
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("writer returned while the catalog was held: %v", err)
	case <-time.After(500 * time.Millisecond):
	}
	require.NoError(t, held.Close())
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("writer did not proceed after the catalog was released")
	}

	held, err = prepareToolSnapshotStore(context.Background(), root)
	require.NoError(t, err)
	defer held.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_, err = prepareToolSnapshotStore(ctx, root)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	_, err = probeToolSnapshotStore(root)
	require.Error(t, err, "a probe keeps its short timeout")
}
