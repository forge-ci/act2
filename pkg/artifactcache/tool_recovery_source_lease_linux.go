//go:build linux

package artifactcache

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"go.etcd.io/bbolt"
	"golang.org/x/sys/unix"
)

const toolRecoverySourceDirectory = ".tool-recovery-source-v1"

func toolRecoverySourceLeasePath(root, owner string) string {
	return filepath.Join(root, toolRecoverySourceDirectory, owner+".bolt")
}

func requireToolRecoverySourceDirectory(root string) error {
	directory := filepath.Join(root, toolRecoverySourceDirectory)
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("recovery source coordination directory is missing or invalid")
	}
	rootMount, err := toolMountID(root)
	if err != nil {
		return err
	}
	mount, err := toolMountID(directory)
	if err != nil || mount != rootMount {
		return fmt.Errorf("recovery source coordination crosses mount boundary")
	}
	return nil
}

func requireToolRecoverySourceLease(root, owner string) error {
	if err := requireToolRecoverySourceDirectory(root); err != nil {
		return err
	}
	var info unix.Stat_t
	path := toolRecoverySourceLeasePath(root, owner)
	if err := unix.Lstat(path, &info); err != nil {
		return err
	}
	if info.Mode&unix.S_IFMT != unix.S_IFREG || info.Nlink != 1 || info.Size <= 0 || info.Size > 1024*1024 {
		return fmt.Errorf("recovery source coordination must be a bounded unaliased regular file")
	}
	rootMount, err := toolMountID(root)
	if err != nil {
		return err
	}
	mount, err := toolMountID(path)
	if err != nil || mount != rootMount {
		return fmt.Errorf("recovery source lease crosses mount boundary")
	}
	return nil
}

// The catalog excludes namespace mutation. Only a private tracked stage may
// initialize the Bolt inode; helper admission never recreates a missing lease.
func prepareToolRecoverySourceLease(ctx context.Context, catalog transferLease, root string, pin ToolRecoveryPin, syncParent func(string) error) (pending string, err error) {
	directory := filepath.Join(root, toolRecoverySourceDirectory)
	if _, err := os.Lstat(directory); os.IsNotExist(err) {
		if err := os.Mkdir(directory, 0700); err != nil {
			return "", err
		}
		if err := syncParent(root); err != nil {
			return "", err
		}
	} else if err != nil {
		return "", err
	}
	if err := requireToolRecoverySourceDirectory(root); err != nil {
		return "", err
	}
	path := toolRecoverySourceLeasePath(root, pin.Owner)
	if _, err := os.Lstat(path); err == nil {
		if err := requireToolRecoverySourceLease(root, pin.Owner); err != nil {
			return "", err
		}
		lease, err := openToolRecoverySourceReader(root, pin)
		if err != nil {
			return "", err
		}
		if err := lease.Close(); err != nil {
			return "", err
		}
		return "", syncParent(directory)
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if err := requireToolRecoverySourceCapacity(directory); err != nil {
		return "", err
	}
	stage, err := createOwnedToolStage(catalog, root, root, ".tool-stage-")
	if err != nil {
		return stage, err
	}
	pending = stage
	defer func() {
		cleanup := cleanupOwnedToolStage(ctx, catalog, root, stage)
		if cleanup == nil {
			pending = ""
		}
		err = errors.Join(err, cleanup)
	}()
	staged := filepath.Join(stage, "source.bolt")
	db, err := bbolt.Open(staged, 0600, &bbolt.Options{Timeout: 100 * time.Millisecond})
	if err != nil {
		return pending, err
	}
	encoded, encodeErr := json.Marshal(pin)
	writeErr := encodeErr
	if writeErr == nil {
		writeErr = db.Update(func(tx *bbolt.Tx) error {
			bucket, err := tx.CreateBucket([]byte("source-identity-v1"))
			if err != nil {
				return err
			}
			return bucket.Put([]byte("pin"), encoded)
		})
	}
	if err := errors.Join(writeErr, db.Sync(), db.Close()); err != nil {
		return pending, err
	}
	if err := ctx.Err(); err != nil {
		return pending, err
	}
	if err := os.Link(staged, path); err != nil {
		return pending, err
	}
	return pending, syncParent(directory)
}

func requireToolRecoverySourceCapacity(directory string) error {
	file, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer file.Close()
	entries, err := file.ReadDir(toolRecoveryPinLimit + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if len(entries) >= toolRecoveryPinLimit {
		return fmt.Errorf("recovery source coordination at capacity")
	}
	return nil
}

func acquireToolRecoverySourceLease(ctx context.Context, root string, pin ToolRecoveryPin) (transferLease, error) {
	catalog, err := prepareExistingToolSnapshotStore(root)
	if err != nil {
		return nil, err
	}
	defer catalog.Close()
	if err := requireLiveToolRecovery(ctx, root, pin); err != nil {
		return nil, err
	}
	if err := requireToolRecoverySourceLease(root, pin.Owner); err != nil {
		return nil, err
	}
	return openToolRecoverySourceReader(root, pin)
}

// A source decision is immutable. Even malformed disposal state refuses access;
// neither admission nor reservation may repair it by reopening the source.
func openToolRecoverySourceReader(root string, pin ToolRecoveryPin) (transferLease, error) {
	db, err := bbolt.Open(toolRecoverySourceLeasePath(root, pin.Owner), 0600, &bbolt.Options{
		ReadOnly: true, Timeout: 100 * time.Millisecond, OpenFile: openCoordinationFile,
	})
	if err != nil {
		return nil, err
	}
	err = db.View(func(tx *bbolt.Tx) error {
		if err := verifyToolRecoverySourceIdentity(tx, pin); err != nil {
			return err
		}
		if tx.Bucket([]byte("source-disposal-v1")) != nil {
			return fmt.Errorf("recovery source is disposed or disposal state is invalid")
		}
		return nil
	})
	if err != nil {
		return nil, errors.Join(err, db.Close())
	}
	return db, nil
}

func verifyToolRecoverySourceIdentity(tx *bbolt.Tx, pin ToolRecoveryPin) error {
	encoded, err := json.Marshal(pin)
	if err != nil {
		return err
	}
	bucket := tx.Bucket([]byte("source-identity-v1"))
	if bucket == nil || !bytes.Equal(bucket.Get([]byte("pin")), encoded) {
		return fmt.Errorf("recovery source identity differs from frozen intent")
	}
	return nil
}
