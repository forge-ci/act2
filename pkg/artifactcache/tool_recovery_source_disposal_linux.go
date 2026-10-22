//go:build linux

package artifactcache

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"go.etcd.io/bbolt"
)

// This epoch excludes runtimes which do not retain source lifetime locks.
// Enrollment must establish it before admitting writers; disposal never upgrades
// an existing store, since an older helper may already own a live source view.
const toolStoreSourceRecoveryMagic = "bosn-tool-snapshots-v3\n"

// fenceToolRecoverySourceDisposal records authority, not physical deletion.
// The caller must separately prove writer absence and exact source ownership.
// Source store enrollment establishes epoch 3 before bootstrap and admission.
func fenceToolRecoverySourceDisposal(ctx context.Context, root string, pin ToolRecoveryPin) error {
	return fenceToolRecoverySourceDisposalWithSync(ctx, root, pin, syncToolDirectory)
}

func fenceToolRecoverySourceDisposalWithSync(ctx context.Context, root string, pin ToolRecoveryPin, syncParent func(string) error) error {
	if err := pin.validate(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	catalog, err := prepareExistingToolSnapshotStore(root)
	if err != nil {
		return err
	}
	defer catalog.Close()
	marker, err := os.ReadFile(filepath.Join(root, toolStoreMarker))
	if err != nil {
		return err
	}
	if string(marker) != toolStoreSourceRecoveryMagic {
		return fmt.Errorf("source disposal requires an enrolled epoch-3 store")
	}
	if err := requireToolRecoverySourceLease(root, pin.Owner); err != nil {
		return err
	}
	db, err := bbolt.Open(toolRecoverySourceLeasePath(root, pin.Owner), 0600, &bbolt.Options{
		Timeout: 100 * time.Millisecond, OpenFile: openToolRecoverySourceWriter,
	})
	if err != nil {
		return err
	}
	defer db.Close()
	if err := db.View(func(tx *bbolt.Tx) error { return verifyToolRecoverySourceIdentity(tx, pin) }); err != nil {
		return err
	}
	if err := requireToolRecoveryDisposalEligible(ctx, root, pin); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := db.Update(func(tx *bbolt.Tx) error {
		bucket := tx.Bucket([]byte("source-disposal-v1"))
		if bucket != nil {
			if bucket.Stats().KeyN != 1 || string(bucket.Get([]byte("decision"))) != "disposed" {
				return fmt.Errorf("invalid source disposal decision")
			}
			return nil
		}
		bucket, err := tx.CreateBucket([]byte("source-disposal-v1"))
		if err != nil {
			return err
		}
		return bucket.Put([]byte("decision"), []byte("disposed"))
	}); err != nil {
		return err
	}
	return errors.Join(db.Sync(), syncParent(filepath.Join(root, toolRecoverySourceDirectory)))
}

func requireToolRecoveryDisposalEligible(ctx context.Context, root string, pin ToolRecoveryPin) error {
	now := time.Now().UTC()
	if pin.CreatedAt.After(now) {
		return fmt.Errorf("future-created recovery source")
	}
	records, err := readToolRecoveryPinRecords(ctx, root, now)
	if err != nil {
		return err
	}
	existing, present := records[pin.Owner]
	if present && !sameToolRecoveryPin(existing, pin) {
		return fmt.Errorf("source disposal differs from immutable pin")
	}
	if !pin.ExpiresAt.After(now) {
		return nil
	}
	if present {
		return fmt.Errorf("source recovery reference is still live")
	}
	selection, err := readToolGenerationSelection(root)
	if err != nil {
		return err
	}
	completion, err := completedToolRecovery(root, selection, pin)
	if err != nil {
		return err
	}
	if completion == nil {
		return fmt.Errorf("unexpired source lacks durable recovery completion")
	}
	return nil
}

// bbolt's ordinary coordination adapter deliberately opens read-only even for
// exclusive leases. A disposal transaction needs an existing writable inode,
// never O_CREATE, and verifies the opened identity before giving it to bbolt.
func openToolRecoverySourceWriter(name string, _ int, _ os.FileMode) (*os.File, error) {
	before, err := os.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() || before.Size() <= 0 || before.Size() > 1024*1024 {
		return nil, fmt.Errorf("invalid recovery source coordination")
	}
	file, err := os.OpenFile(name, os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	after, err := file.Stat()
	if err != nil || !os.SameFile(before, after) {
		return nil, errors.Join(fmt.Errorf("recovery source coordination identity changed"), err, file.Close())
	}
	return file, nil
}
