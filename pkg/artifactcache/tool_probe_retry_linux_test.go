//go:build linux

package artifactcache

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.etcd.io/bbolt"
)

func awaitProbe(ctx context.Context, t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-ctx.Done():
		t.Fatalf("probe was not reached: %v", ctx.Err())
	}
}

func awaitResult[T any](ctx context.Context, t *testing.T, result <-chan T) T {
	t.Helper()
	select {
	case report := <-result:
		return report
	case <-ctx.Done():
		t.Fatalf("retry did not finish: %v", ctx.Err())
		var zero T
		return zero
	}
}

func TestEnrollmentRetriesSelectionCatalogContention(t *testing.T) {
	source, root := toolBootstrapFixture(t)
	first := EnrollToolGeneration(context.Background(), source, root, 100)
	require.False(t, first.Partial, first.Error)
	catalog, err := prepareExistingToolSnapshotStore(root)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	done := make(chan ToolEnrollmentReport, 1)
	probe := make(chan struct{})
	resume := make(chan struct{})
	var resumeOnce sync.Once
	defer resumeOnce.Do(func() { close(resume) })
	defer catalog.Close()
	go func() {
		done <- retryToolEnrollmentReport(ctx, func(ctx context.Context) (ToolEnrollmentReport, bool) {
			report, retry := enrollToolGenerationAttempt(ctx, source, root, 100, toolEnrollmentAll, storeToolEnrollmentHistory)
			if retry {
				close(probe)
				<-resume
			}
			return report, retry
		})
	}()
	awaitProbe(ctx, t, probe)
	require.NoError(t, catalog.Close())
	resumeOnce.Do(func() { close(resume) })
	report := awaitResult(ctx, t, done)
	require.False(t, report.Partial, report.Error)
	require.Equal(t, first.Generation, report.Generation)
}

func TestRecoveryPinRetriesCatalogReacquisition(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	root, spec := toolGenerationFixture(t)
	first := InitializeToolGeneration(ctx, root, spec, 100)
	require.False(t, first.Partial, first.Error)
	now := time.Now().UTC()
	pin := ToolRecoveryPin{1, strings.Repeat("a", 64), first.Generation.ID, now, now.Add(time.Hour)}
	entered, verifyResume, probe, retryResume := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	done := make(chan ToolRecoveryPinReport, 1)
	var verifyOnce, retryOnce sync.Once
	defer verifyOnce.Do(func() { close(verifyResume) })
	defer retryOnce.Do(func() { close(retryResume) })
	go func() {
		calls := 0
		verify := func(ctx context.Context, generation string, data []byte, manifest toolManifest, maxBytes int64) error {
			calls++
			if calls == 1 {
				close(entered)
				<-verifyResume
			}
			return verifyToolGeneration(ctx, generation, data, manifest, maxBytes)
		}
		done <- retryToolRecoveryPinReport(ctx, func(ctx context.Context) (ToolRecoveryPinReport, bool) {
			report, retry := publishToolRecoveryPinAttempt(ctx, root, pin, 100, syncToolDirectory, verify, prepareToolGenerationUpdateCatalog)
			if retry {
				close(probe)
				<-retryResume
			}
			return report, retry
		})
	}()
	awaitProbe(ctx, t, entered)
	catalog, err := prepareExistingToolSnapshotStore(root)
	require.NoError(t, err)
	defer catalog.Close()
	verifyOnce.Do(func() { close(verifyResume) })
	awaitProbe(ctx, t, probe)
	require.NoError(t, catalog.Close())
	retryOnce.Do(func() { close(retryResume) })
	report := awaitResult(ctx, t, done)
	require.False(t, report.Partial, report.Error)
	require.True(t, report.Published)
}

func TestRetryDiagnosticsKeepCancellationAheadOfLongDetails(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	got := retryToolEnrollmentReport(ctx, func(context.Context) (ToolEnrollmentReport, bool) {
		cancel()
		return ToolEnrollmentReport{Partial: true, Error: strings.Repeat("x", 300)}, true
	})
	require.Contains(t, got.Error, "context canceled")

	ctx, cancel = context.WithCancel(context.Background())
	pin := retryToolRecoveryPinReport(ctx, func(context.Context) (ToolRecoveryPinReport, bool) {
		cancel()
		return ToolRecoveryPinReport{Partial: true, Error: strings.Repeat("x", 300)}, true
	})
	require.Contains(t, pin.Error, "context canceled")
}

func TestFreshEnrollmentHistorySurvivesCancellationAtOpen(t *testing.T) {
	source, root := toolBootstrapFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	report, _ := enrollToolGenerationAttemptWithOpen(ctx, source, root, 100, toolEnrollmentAll, storeToolEnrollmentHistory,
		func(ctx context.Context, root string, create bool) (*bbolt.DB, bool, error) {
			return openEnrollmentHistoryFileWithHook(ctx, toolEnrollmentHistoryPath(root), create, func(fresh bool) {
				if fresh {
					cancel()
				}
			})
		})
	require.True(t, report.Partial)
	report = EnrollToolGeneration(context.Background(), source, root, 100)
	require.False(t, report.Partial, report.Error)
	require.True(t, report.Enrolled)
}
