//go:build linux

package artifactcache

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	boltErrors "go.etcd.io/bbolt/errors"
)

func TestToolRecoveryPinPublicationIsImmutableAndRetryable(t *testing.T) {
	ctx := context.Background()
	root, spec := toolGenerationFixture(t)
	first := InitializeToolGeneration(ctx, root, spec, 100)
	require.False(t, first.Partial, first.Error)
	now := time.Now().UTC()
	pin := ToolRecoveryPin{1, strings.Repeat("a", 64), first.Generation.ID, now, now.Add(time.Hour)}
	report := PublishToolRecoveryPin(ctx, root, pin, 100)
	require.False(t, report.Partial, report.Error)
	require.True(t, report.Published)
	report = PublishToolRecoveryPin(ctx, root, pin, 100)
	require.False(t, report.Partial, report.Error)
	require.True(t, report.Reused)
	pin.ExpiresAt = pin.ExpiresAt.Add(time.Minute)
	report = PublishToolRecoveryPin(ctx, root, pin, 100)
	require.True(t, report.Partial)
	require.Contains(t, report.Error, "differs")
}

func TestToolRecoveryPinPublicationReconcilesAfterParentSyncFailure(t *testing.T) {
	ctx := context.Background()
	root, spec := toolGenerationFixture(t)
	first := InitializeToolGeneration(ctx, root, spec, 100)
	require.False(t, first.Partial, first.Error)
	now := time.Now().UTC()
	pin := ToolRecoveryPin{1, strings.Repeat("a", 64), first.Generation.ID, now, now.Add(time.Hour)}
	report := publishToolRecoveryPinWithSync(ctx, root, pin, 100, func(directory string) error {
		if directory == filepath.Join(root, toolRecoveryPinDirectory) {
			return errors.New("injected parent sync failure")
		}
		return syncToolDirectory(directory)
	})
	require.True(t, report.Published)
	require.True(t, report.Partial)
	report = PublishToolRecoveryPin(ctx, root, pin, 100)
	require.False(t, report.Partial, report.Error)
	require.True(t, report.Published)
	require.True(t, report.Reused)
}

func TestToolRecoveryPinValidationReleasesCatalogUnderOriginalReader(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	root, spec := toolGenerationFixture(t)
	first := InitializeToolGeneration(ctx, root, spec, 100)
	require.False(t, first.Partial, first.Error)
	now := time.Now().UTC()
	pin := ToolRecoveryPin{1, strings.Repeat("a", 64), first.Generation.ID, now, now.Add(time.Hour)}
	entered, release := make(chan struct{}), make(chan struct{})
	result := make(chan ToolRecoveryPinReport, 1)
	go func() {
		result <- publishToolRecoveryPinWithValidation(ctx, root, pin, 100, syncToolDirectory,
			func(ctx context.Context, generation string, data []byte, manifest toolManifest, maxBytes int64) error {
				close(entered)
				select {
				case <-release:
				case <-ctx.Done():
					return ctx.Err()
				}
				return verifyToolGeneration(ctx, generation, data, manifest, maxBytes)
			})
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("validation did not start")
	}
	catalog, err := prepareExistingToolSnapshotStore(root)
	require.NoError(t, err, "validation must not serialize unrelated catalog operations")
	require.NoError(t, catalog.Close())
	writer, err := openTransferLease(filepath.Join(first.Generation.Destination, toolGenerationReaderLock), false, 100*time.Millisecond)
	if writer != nil {
		_ = writer.Close()
	}
	require.Error(t, err, "the original generation reader must protect payload validation")
	close(release)
	select {
	case report := <-result:
		require.False(t, report.Partial, report.Error)
		require.True(t, report.Published)
	case <-ctx.Done():
		t.Fatal("publication did not finish")
	}
}

func TestToolRecoveryPinReportsCatalogContention(t *testing.T) {
	ctx := context.Background()
	root, spec := toolGenerationFixture(t)
	first := InitializeToolGeneration(ctx, root, spec, 100)
	require.False(t, first.Partial, first.Error)
	now := time.Now().UTC()
	pin := ToolRecoveryPin{1, strings.Repeat("a", 64), first.Generation.ID, now, now.Add(time.Hour)}
	catalog, err := prepareExistingToolSnapshotStore(root)
	require.NoError(t, err)
	defer catalog.Close()
	deadlineCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	report := PublishToolRecoveryPin(deadlineCtx, root, pin, 100)
	require.True(t, report.Partial)
	require.Contains(t, report.Error, "recovery reservation catalog")
	require.Contains(t, report.Error, "deadline exceeded")
}

func TestToolRecoveryPinWaitsForInitialCatalogContention(t *testing.T) {
	ctx := context.Background()
	root, spec := toolGenerationFixture(t)
	first := InitializeToolGeneration(ctx, root, spec, 100)
	require.False(t, first.Partial, first.Error)
	now := time.Now().UTC()
	pin := ToolRecoveryPin{1, strings.Repeat("a", 64), first.Generation.ID, now, now.Add(time.Hour)}
	catalog, err := prepareExistingToolSnapshotStore(root)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result := make(chan ToolRecoveryPinReport, 1)
	entered, resume := make(chan struct{}), make(chan struct{})
	go func() {
		result <- publishToolRecoveryPinWithCatalog(ctx, root, pin, 100, syncToolDirectory, verifyToolGeneration, func(ctx context.Context, root string) (transferLease, error) {
			return prepareToolGenerationUpdateCatalogWithOpen(ctx, root, func(path string, readOnly bool, timeout time.Duration) (transferLease, error) {
				lease, err := openTransferLease(path, readOnly, timeout)
				if errors.Is(err, boltErrors.ErrTimeout) {
					close(entered)
					<-resume
				}
				return lease, err
			})
		})
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("reservation did not attempt the catalog lock")
	}
	require.NoError(t, catalog.Close())
	close(resume)
	report := <-result
	require.False(t, report.Partial, report.Error)
	require.True(t, report.Published)
}

func TestToolRecoveryPinHonorsOperationBudgetAndPreservesRetry(t *testing.T) {
	root, spec := toolGenerationFixture(t)
	first := InitializeToolGeneration(context.Background(), root, spec, 100)
	require.False(t, first.Partial, first.Error)
	now := time.Now().UTC()
	pin := ToolRecoveryPin{1, strings.Repeat("e", 64), first.Generation.ID, now, now.Add(time.Hour)}
	verify := func(ctx context.Context, generation string, data []byte, manifest toolManifest, maxBytes int64) error {
		select {
		case <-time.After(1500 * time.Millisecond):
			return verifyToolGeneration(ctx, generation, data, manifest, maxBytes)
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	short, err := WithToolOperationBudget(context.Background(), time.Second)
	require.NoError(t, err)
	failed := publishToolRecoveryPinWithValidation(short, root, pin, 100, syncToolDirectory, verify)
	require.True(t, failed.Partial)
	require.Contains(t, failed.Error, "deadline exceeded")
	require.False(t, failed.Published)
	long, err := WithToolOperationBudget(context.Background(), 5*time.Second)
	require.NoError(t, err)
	retried := publishToolRecoveryPinWithValidation(long, root, pin, 100, syncToolDirectory, verify)
	require.False(t, retried.Partial, retried.Error)
	require.True(t, retried.Published)
}
