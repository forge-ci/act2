//go:build linux

package artifactcache

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestToolRecoveryViewHoldsOriginalReaderThroughCleanup(t *testing.T) {
	root, source, pin := toolRecoveryPublicationFixture(t)
	original, _, err := readToolGenerationManifest(filepath.Join(root, toolGenerationDirectory, pin.Generation), pin.Generation)
	require.NoError(t, err)
	opened, closed := false, false
	report := PublishToolRecoveryView(context.Background(), root, pin, 100, func(ctx context.Context) (ToolRecoverySourceView, error) {
		opened = true
		successor := UpdateToolGeneration(ctx, root, ToolGenerationSpec{SchemaVersion: 1, Installs: []ToolGenerationInstall{{Path: "Node/2/x64", ObjectID: original.Installs[0].ObjectID}}}, 100)
		require.False(t, successor.Partial, successor.Error)
		release := ReleaseToolRecoveryPin(ctx, root, pin)
		require.False(t, release.Partial, release.Error)
		require.Error(t, RetireToolGeneration(ctx, root, pin.Generation, 100), "factory runs under the original reader even after pin release")
		return ToolRecoverySourceView{Path: source, Close: func() error {
			closed = true
			require.Error(t, RetireToolGeneration(ctx, root, pin.Generation, 100), "reader survives through source unmount/cleanup")
			return nil
		}}, nil
	})
	require.True(t, opened)
	require.True(t, closed)
	require.True(t, report.Partial, "withdrawn protection must refuse selection")
	require.False(t, report.Completed)
	require.NoError(t, RetireToolGeneration(context.Background(), root, pin.Generation, 100), "reader closes after cleanup")
}

func TestToolRecoveryViewReconcilesWithoutReopeningAndReportsCleanupFailure(t *testing.T) {
	root, source, pin := toolRecoveryPublicationFixture(t)
	closes := 0
	first := PublishToolRecoveryView(context.Background(), root, pin, 100, func(context.Context) (ToolRecoverySourceView, error) {
		return ToolRecoverySourceView{Path: source, Close: func() error { closes++; return errors.New("injected view cleanup failure") }}, nil
	})
	require.Equal(t, 1, closes)
	require.True(t, first.Completed, "selection and receipt committed before cleanup")
	require.True(t, first.Partial, "cleanup failure is never release authority")
	require.Contains(t, first.Error, "view cleanup")
	retried := PublishToolRecoveryView(context.Background(), root, pin, 100, func(context.Context) (ToolRecoverySourceView, error) {
		t.Fatal("committed publication must not reopen a view")
		return ToolRecoverySourceView{}, errors.New("unexpected source access")
	})
	require.False(t, retried.Partial, retried.Error)
	require.True(t, retried.Completed)
	require.Equal(t, first.Generation, retried.Generation)
	require.True(t, first.CompletedAt.Equal(retried.CompletedAt))
}
