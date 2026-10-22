//go:build linux

package artifactcache

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestToolRecoveryFinalizationPublishesBeforeReleasing(t *testing.T) {
	ctx := context.Background()
	root, source, pin := toolRecoveryPublicationFixture(t)
	require.NoError(t, os.WriteFile(filepath.Join(root, toolStoreMarker), []byte(toolStoreSourceRecoveryMagic), 0600))
	opened, closed := 0, 0
	report := FinalizeToolRecoverySource(ctx, root, pin, 100, func(context.Context) (ToolRecoverySourceView, error) {
		opened++
		return ToolRecoverySourceView{Path: source, Close: func() error {
			closed++
			require.NoError(t, requireLiveToolRecovery(ctx, root, pin), "reference survives until view cleanup succeeds")
			return nil
		}}, nil
	})
	require.False(t, report.Partial, report.Error)
	require.True(t, report.Disposed)
	require.True(t, report.ReferenceAbsent)
	require.NotNil(t, report.Publication)
	require.Equal(t, 1, opened)
	require.Equal(t, 1, closed)
	retried := FinalizeToolRecoverySource(ctx, root, pin, 100, nil)
	require.False(t, retried.Partial, retried.Error)
	require.True(t, retried.Disposed)
	require.True(t, retried.ReferenceAbsent)
	require.Equal(t, report.Publication, retried.Publication)
}

func TestToolRecoveryFinalizationRetriesUnmountAfterExpiry(t *testing.T) {
	ctx := context.Background()
	root, source, pin := toolRecoveryPublicationFixture(t)
	pin.Owner = strings.Repeat("b", 64)
	pin.CreatedAt = time.Now().UTC()
	pin.ExpiresAt = pin.CreatedAt.Add(2 * time.Second)
	reserved := PublishToolRecoveryPin(ctx, root, pin, 100)
	require.True(t, reserved.Published, reserved.Error)
	require.NoError(t, os.WriteFile(filepath.Join(root, toolStoreMarker), []byte(toolStoreSourceRecoveryMagic), 0600))
	cleanupFails := true
	closes := 0
	defer func() {
		cleanupFails = false
		require.NoError(t, retryToolRecoveryViewCleanup())
	}()
	report := FinalizeToolRecoverySource(ctx, root, pin, 100, func(context.Context) (ToolRecoverySourceView, error) {
		return ToolRecoverySourceView{Path: source, Close: func() error {
			closes++
			if cleanupFails {
				return errors.New("injected unmount failure")
			}
			return nil
		}}, nil
	})
	require.True(t, report.Partial)
	require.False(t, report.ReferenceAbsent)
	require.NoError(t, requireLiveToolRecovery(ctx, root, pin))
	time.Sleep(time.Until(pin.ExpiresAt) + 20*time.Millisecond)
	cleanupFails = false
	retried := FinalizeToolRecoverySource(ctx, root, pin, 100, func(context.Context) (ToolRecoverySourceView, error) {
		t.Fatal("expired finalization must never reopen the source")
		return ToolRecoverySourceView{}, nil
	})
	require.False(t, retried.Partial, retried.Error)
	require.True(t, retried.ReferenceAbsent)
	require.True(t, retried.Disposed)
	require.Equal(t, 2, closes)
}

func TestToolRecoveryFinalizationRefusesLegacyStore(t *testing.T) {
	root, _, pin := toolRecoveryPublicationFixture(t)
	report := FinalizeToolRecoverySource(context.Background(), root, pin, 100, func(context.Context) (ToolRecoverySourceView, error) {
		t.Fatal("legacy store must refuse before source access")
		return ToolRecoverySourceView{}, nil
	})
	require.True(t, report.Partial)
	require.Contains(t, report.Error, "epoch-3")
	require.NoError(t, requireLiveToolRecovery(context.Background(), root, pin))
	marker, err := os.ReadFile(filepath.Join(root, toolStoreMarker))
	require.NoError(t, err)
	require.NotEqual(t, toolStoreSourceRecoveryMagic, string(marker))
}

func TestToolRecoveryFinalizationExpiryPreservesActiveReader(t *testing.T) {
	ctx := context.Background()
	root, _, pin := toolRecoveryPublicationFixture(t)
	pin.Owner = strings.Repeat("b", 64)
	pin.CreatedAt = time.Now().UTC()
	pin.ExpiresAt = pin.CreatedAt.Add(2 * time.Second)
	reserved := PublishToolRecoveryPin(ctx, root, pin, 100)
	require.True(t, reserved.Published, reserved.Error)
	require.NoError(t, os.WriteFile(filepath.Join(root, toolStoreMarker), []byte(toolStoreSourceRecoveryMagic), 0600))
	reader, err := openToolRecoverySourceReader(root, pin)
	require.NoError(t, err)
	defer reader.Close()
	time.Sleep(time.Until(pin.ExpiresAt) + 20*time.Millisecond)
	refused := FinalizeToolRecoverySource(ctx, root, pin, 100, nil)
	require.True(t, refused.Partial)
	require.False(t, refused.ReferenceAbsent)
	require.False(t, refused.Disposed)
	_, err = os.Stat(filepath.Join(root, toolRecoveryPinDirectory, pin.Owner+".json"))
	require.NoError(t, err, "active view retains the exact reference even after expiry")
	require.NoError(t, reader.Close())
	retried := FinalizeToolRecoverySource(ctx, root, pin, 100, nil)
	require.False(t, retried.Partial, retried.Error)
	require.True(t, retried.ReferenceAbsent)
	require.True(t, retried.Disposed)
}

func TestToolRecoveryFinalizationRefusesInvalidAuthority(t *testing.T) {
	for _, scenario := range []string{"missing-source", "changed-pin", "cancelled", "zero-budget"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			root, _, original := toolRecoveryPublicationFixture(t)
			require.NoError(t, os.WriteFile(filepath.Join(root, toolStoreMarker), []byte(toolStoreSourceRecoveryMagic), 0600))
			pin, maxBytes := original, int64(100)
			switch scenario {
			case "missing-source":
				path := toolRecoverySourceLeasePath(root, pin.Owner)
				require.NoError(t, os.Rename(path, path+".held"))
			case "changed-pin":
				pin.ExpiresAt = pin.ExpiresAt.Add(time.Second)
			case "cancelled":
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelled
			case "zero-budget":
				maxBytes = 0
			}
			report := FinalizeToolRecoverySource(ctx, root, pin, maxBytes, func(context.Context) (ToolRecoverySourceView, error) {
				t.Fatal("invalid authority must refuse before opening the source")
				return ToolRecoverySourceView{}, nil
			})
			require.True(t, report.Partial)
			require.False(t, report.Disposed)
			require.False(t, report.ReferenceAbsent)
			require.NoError(t, requireLiveToolRecovery(context.Background(), root, original))
			if scenario == "missing-source" {
				_, err := os.Stat(toolRecoverySourceLeasePath(root, pin.Owner))
				require.True(t, os.IsNotExist(err), "finalization must never recreate missing authority")
			}
		})
	}
}
