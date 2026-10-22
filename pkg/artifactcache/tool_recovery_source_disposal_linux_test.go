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

func TestToolRecoverySourceDisposalRequiresEpochAndCompletion(t *testing.T) {
	for _, scenario := range []string{"old-epoch", "live-pin", "missing-completion", "completed"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			root, source, pin := toolRecoveryPublicationFixture(t)
			if scenario != "old-epoch" {
				require.NoError(t, os.WriteFile(filepath.Join(root, toolStoreMarker), []byte(toolStoreSourceRecoveryMagic), 0600))
			}
			if scenario == "completed" {
				published := PublishToolRecoveryView(ctx, root, pin, 100, func(context.Context) (ToolRecoverySourceView, error) {
					return ToolRecoverySourceView{Path: source, Close: func() error { return nil }}, nil
				})
				require.False(t, published.Partial, published.Error)
			}
			if scenario == "completed" || scenario == "missing-completion" {
				released := ReleaseToolRecoveryPin(ctx, root, pin)
				require.True(t, released.Absent, released.Error)
			}
			err := fenceToolRecoverySourceDisposal(ctx, root, pin)
			if scenario != "completed" {
				require.Error(t, err)
				reader, readErr := openToolRecoverySourceReader(root, pin)
				require.NoError(t, readErr, "refusal must preserve a recoverable source")
				require.NoError(t, reader.Close())
				return
			}
			require.NoError(t, err)
			require.NoError(t, fenceToolRecoverySourceDisposal(ctx, root, pin), "retry must reconcile durable decision")
			reader, readErr := openToolRecoverySourceReader(root, pin)
			require.Nil(t, reader)
			require.Error(t, readErr)
			reserved := PublishToolRecoveryPin(ctx, root, pin, 100)
			require.True(t, reserved.Partial)
		})
	}
}

func TestToolRecoverySourceDisposalExcludesActiveReaders(t *testing.T) {
	root, _, pin := toolRecoveryPublicationFixture(t)
	require.NoError(t, os.WriteFile(filepath.Join(root, toolStoreMarker), []byte(toolStoreSourceRecoveryMagic), 0600))
	reader, err := openToolRecoverySourceReader(root, pin)
	require.NoError(t, err)
	started := time.Now()
	err = fenceToolRecoverySourceDisposal(context.Background(), root, pin)
	require.Error(t, err)
	require.Less(t, time.Since(started), time.Second, "catalog must not wait behind a long-lived recovery view")
	require.NoError(t, reader.Close())
}

func TestToolRecoverySourceDisposalLostSyncRetainsDecision(t *testing.T) {
	ctx := context.Background()
	root, source, pin := toolRecoveryPublicationFixture(t)
	require.NoError(t, os.WriteFile(filepath.Join(root, toolStoreMarker), []byte(toolStoreSourceRecoveryMagic), 0600))
	published := PublishToolRecoveryView(ctx, root, pin, 100, func(context.Context) (ToolRecoverySourceView, error) {
		return ToolRecoverySourceView{Path: source, Close: func() error { return nil }}, nil
	})
	require.True(t, published.Completed, published.Error)
	released := ReleaseToolRecoveryPin(ctx, root, pin)
	require.True(t, released.Absent, released.Error)
	injected := errors.New("injected lost disposal acknowledgement")
	require.ErrorIs(t, fenceToolRecoverySourceDisposalWithSync(ctx, root, pin, func(string) error { return injected }), injected)
	reader, err := openToolRecoverySourceReader(root, pin)
	require.Nil(t, reader)
	require.Error(t, err, "a lost acknowledgement must never reopen the source")
	synced := false
	require.NoError(t, fenceToolRecoverySourceDisposalWithSync(ctx, root, pin, func(directory string) error {
		synced = true
		return syncToolDirectory(directory)
	}))
	require.True(t, synced)
}

func TestToolRecoveryExpiredSourceStillExcludesActiveReader(t *testing.T) {
	ctx := context.Background()
	root, _, original := toolRecoveryPublicationFixture(t)
	pin := original
	pin.Owner = strings.Repeat("b", 64)
	pin.CreatedAt = time.Now().UTC()
	pin.ExpiresAt = pin.CreatedAt.Add(2 * time.Second)
	reserved := PublishToolRecoveryPin(ctx, root, pin, 100)
	require.True(t, reserved.Published, reserved.Error)
	require.NoError(t, os.WriteFile(filepath.Join(root, toolStoreMarker), []byte(toolStoreSourceRecoveryMagic), 0600))
	reader, err := openToolRecoverySourceReader(root, pin)
	require.NoError(t, err)
	time.Sleep(time.Until(pin.ExpiresAt) + 20*time.Millisecond)
	require.Error(t, fenceToolRecoverySourceDisposal(ctx, root, pin), "expiry must not revoke an active source view")
	require.NoError(t, reader.Close())
	require.NoError(t, fenceToolRecoverySourceDisposal(ctx, root, pin), "actual expiry permits incomplete-source disposal only after readers leave")
	require.NoError(t, fenceToolRecoverySourceDisposal(ctx, root, pin))
	changed := pin
	changed.Generation = strings.Repeat("c", 64)
	require.Error(t, fenceToolRecoverySourceDisposal(ctx, root, changed), "expired state must still match immutable source identity")
}
