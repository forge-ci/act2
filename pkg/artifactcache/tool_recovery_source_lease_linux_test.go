//go:build linux

package artifactcache

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.etcd.io/bbolt"
)

func TestToolRecoveryViewExcludesSourceDisposal(t *testing.T) {
	root, source, pin := toolRecoveryPublicationFixture(t)
	directory := filepath.Join(root, ".tool-recovery-source-v1")
	require.NoError(t, os.MkdirAll(directory, 0700))
	path := filepath.Join(directory, pin.Owner+".bolt")
	initial, err := bbolt.Open(path, 0600, &bbolt.Options{Timeout: 100 * time.Millisecond})
	require.NoError(t, err)
	require.NoError(t, initial.Close())
	opened := false
	report := PublishToolRecoveryView(context.Background(), root, pin, 100, func(context.Context) (ToolRecoverySourceView, error) {
		opened = true
		disposal, acquireErr := openTransferLease(path, false, 100*time.Millisecond)
		if disposal != nil {
			require.NoError(t, disposal.Close())
		}
		require.Error(t, acquireErr, "source disposal lock was acquired while recovery factory was live")
		return ToolRecoverySourceView{Path: source, Close: func() error { return nil }}, nil
	})
	require.True(t, opened)
	require.False(t, report.Partial, report.Error)
	require.True(t, report.Completed)
	disposal, err := openTransferLease(path, false, 100*time.Millisecond)
	require.NoError(t, err, "finished namespace cleanup must release the source lease")
	require.NoError(t, disposal.Close())
}

func TestToolRecoverySourceLeaseSurvivesFailedUnmountAndGC(t *testing.T) {
	for _, openingFails := range []bool{false, true} {
		t.Run(fmt.Sprint(openingFails), func(t *testing.T) {
			root, source, pin := toolRecoveryPublicationFixture(t)
			cleanupFails := true
			defer func() {
				cleanupFails = false
				_ = PublishToolRecoveryView(context.Background(), root, pin, 100, nil)
			}()
			first := PublishToolRecoveryView(context.Background(), root, pin, 100, func(context.Context) (ToolRecoverySourceView, error) {
				view := ToolRecoverySourceView{Path: source, Close: func() error {
					if cleanupFails {
						return errors.New("injected unmount failure")
					}
					return nil
				}}
				if openingFails {
					return view, errors.New("injected source verification failure")
				}
				return view, nil
			})
			require.True(t, first.Partial)
			released := ReleaseToolRecoveryPin(context.Background(), root, pin)
			require.True(t, released.Absent, released.Error)
			runtime.GC()
			path := toolRecoverySourceLeasePath(root, pin.Owner)
			disposal, err := openTransferLease(path, false, 100*time.Millisecond)
			if disposal != nil {
				require.NoError(t, disposal.Close())
			}
			require.Error(t, err, "failed unmount must retain source protection after return, release and GC")
			cleanupFails = false
			_ = PublishToolRecoveryView(context.Background(), root, pin, 100, nil)
			disposal, err = openTransferLease(path, false, 100*time.Millisecond)
			require.NoError(t, err, "successful pending cleanup releases the source")
			require.NoError(t, disposal.Close())
		})
	}
}

func TestToolRecoverySourceCoordinationCorruptionRefusesBeforeOpening(t *testing.T) {
	for _, mode := range []string{"directory-symlink", "file-symlink", "hardlink", "missing", "empty", "oversized"} {
		t.Run(mode, func(t *testing.T) {
			root, _, pin := toolRecoveryPublicationFixture(t)
			directory := filepath.Join(root, toolRecoverySourceDirectory)
			path := toolRecoverySourceLeasePath(root, pin.Owner)
			switch mode {
			case "directory-symlink":
				require.NoError(t, os.Rename(directory, directory+".original"))
				require.NoError(t, os.Symlink(directory+".original", directory))
			case "file-symlink":
				require.NoError(t, os.Rename(path, path+".original"))
				require.NoError(t, os.Symlink(path+".original", path))
			case "hardlink":
				require.NoError(t, os.Link(path, path+".alias"))
			case "missing":
				require.NoError(t, os.Rename(path, path+".original"))
			case "empty":
				require.NoError(t, os.WriteFile(path, nil, 0600))
			case "oversized":
				require.NoError(t, os.WriteFile(path, make([]byte, 1024*1024+1), 0600))
			}
			report := PublishToolRecoveryView(context.Background(), root, pin, 100, func(context.Context) (ToolRecoverySourceView, error) {
				t.Fatal("invalid coordination must refuse before source opening")
				return ToolRecoverySourceView{}, nil
			})
			require.True(t, report.Partial)
			if mode == "missing" {
				_, err := os.Lstat(path)
				require.ErrorIs(t, err, os.ErrNotExist, "admission must not recreate coordination")
			}
		})
	}
}

func TestToolRecoverySourceLeaseRetrySyncsBeforeAcknowledgement(t *testing.T) {
	root, _, pin := toolRecoveryPublicationFixture(t)
	path := toolRecoverySourceLeasePath(root, pin.Owner)
	before, err := os.Stat(path)
	require.NoError(t, err)
	failed := false
	first := publishToolRecoveryPinWithSync(context.Background(), root, pin, 100, func(directory string) error {
		if filepath.Base(directory) == toolRecoverySourceDirectory {
			failed = true
			return errors.New("injected source coordination sync failure")
		}
		return syncToolDirectory(directory)
	})
	require.True(t, failed)
	require.True(t, first.Partial)
	synced := false
	retried := publishToolRecoveryPinWithSync(context.Background(), root, pin, 100, func(directory string) error {
		if filepath.Base(directory) == toolRecoverySourceDirectory {
			synced = true
		}
		return syncToolDirectory(directory)
	})
	require.True(t, synced, "visible coordination still needs durable parent-sync reconciliation")
	require.False(t, retried.Partial, retried.Error)
	require.True(t, retried.Published)
	require.True(t, retried.Reused)
	after, err := os.Stat(path)
	require.NoError(t, err)
	require.True(t, os.SameFile(before, after), "retries must not replace a coordination inode")
}

func TestToolRecoveryDisposedSourceCannotReopen(t *testing.T) {
	root, _, pin := toolRecoveryPublicationFixture(t)
	path := toolRecoverySourceLeasePath(root, pin.Owner)
	db, err := bbolt.Open(path, 0600, &bbolt.Options{Timeout: 100 * time.Millisecond})
	require.NoError(t, err)
	require.NoError(t, db.Update(func(tx *bbolt.Tx) error {
		bucket, e := tx.CreateBucketIfNotExists([]byte("source-disposal-v1"))
		if e != nil {
			return e
		}
		return bucket.Put([]byte("pin"), []byte("disposed"))
	}))
	require.NoError(t, db.Close())
	lease, err := acquireToolRecoverySourceLease(context.Background(), root, pin)
	if lease != nil {
		require.NoError(t, lease.Close())
	}
	require.Error(t, err, "durably disposed or corrupt source state must refuse reopening")
	reserved := PublishToolRecoveryPin(context.Background(), root, pin, 100)
	require.True(t, reserved.Partial, "reservation retry must not reopen a disposed source")
}
