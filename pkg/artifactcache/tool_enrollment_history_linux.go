//go:build linux

package artifactcache

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"go.etcd.io/bbolt"
	boltErrors "go.etcd.io/bbolt/errors"
	"golang.org/x/sys/unix"
)

type toolEnrollmentHistory struct {
	SchemaVersion int    `json:"schema_version"`
	Source        string `json:"source"`
	Root          string `json:"root"`
	Mode          string `json:"mode"`
	Ready         bool   `json:"ready"`
	StoreIdentity string `json:"store_identity"`
	SourcePresent bool   `json:"source_present"`
}

func toolEnrollmentHistoryPath(root string) string {
	return filepath.Join(filepath.Dir(root), "."+filepath.Base(root)+".enrollment-v1.bolt")
}

// One fixed journal per store remains outside that store. Deleting a warm
// selection or the whole native tree cannot turn it into a first cold start.
// The Bolt writer lock excludes simultaneous enrollment and survives crashes.
func openToolEnrollmentHistory(ctx context.Context, root string) (*bbolt.DB, bool, error) {
	return openToolEnrollmentHistoryMode(ctx, root, true)
}

func openToolEnrollmentHistoryMode(ctx context.Context, root string, create bool) (*bbolt.DB, bool, error) {
	return openEnrollmentHistoryFile(ctx, toolEnrollmentHistoryPath(root), create)
}

func openEnrollmentHistoryFile(ctx context.Context, path string, create bool) (*bbolt.DB, bool, error) {
	return openEnrollmentHistoryFileWithHook(ctx, path, create, nil)
}

// The hook allows tests to cancel exactly after opening, before the ownership check.
func openEnrollmentHistoryFileWithHook(ctx context.Context, path string, create bool, opened func(bool)) (*bbolt.DB, bool, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		_, statErr := os.Lstat(path)
		fresh := os.IsNotExist(statErr)
		if fresh && !create {
			return nil, false, fmt.Errorf("prior enrollment fence history is missing")
		}
		if statErr != nil && !fresh {
			return nil, false, statErr
		}
		db, err := bbolt.Open(path, 0600, &bbolt.Options{
			Timeout: 100 * time.Millisecond,
			OpenFile: func(name string, flags int, mode os.FileMode) (*os.File, error) {
				if fresh {
					flags |= unix.O_EXCL
				} else {
					flags &^= unix.O_CREAT
				}
				return openToolEnrollmentFile(name, flags, mode)
			},
		})
		if errors.Is(err, boltErrors.ErrTimeout) {
			continue
		}
		if err != nil {
			return nil, false, err
		}
		if opened != nil {
			opened(fresh)
		}
		if err := ctx.Err(); err != nil && !fresh {
			return nil, false, errors.Join(err, db.Close())
		}
		// Include the initial file's namespace entry in the durable intent.
		if err := syncToolDirectory(filepath.Dir(path)); err != nil {
			return nil, false, errors.Join(err, db.Close())
		}
		return db, fresh, nil
	}
}

func openToolEnrollmentFile(name string, flags int, mode os.FileMode) (*os.File, error) {
	fd, err := unix.Open(name, flags|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, uint32(mode.Perm()))
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), name)
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return nil, errors.Join(err, file.Close())
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 || stat.Size > 1<<20 || (stat.Size == 0 && flags&unix.O_EXCL == 0) {
		return nil, errors.Join(fmt.Errorf("enrollment history must be a bounded unaliased regular file"), file.Close())
	}
	parentMount, err := toolMountID(filepath.Dir(name))
	if err != nil {
		return nil, errors.Join(err, file.Close())
	}
	mount, err := toolMountID(name)
	if err != nil || mount != parentMount {
		return nil, errors.Join(fmt.Errorf("enrollment history crosses mount boundary: %v", err), file.Close())
	}
	return file, nil
}

func loadToolEnrollmentHistory(db *bbolt.DB, source, root string) (*toolEnrollmentHistory, error) {
	var history *toolEnrollmentHistory
	err := db.View(func(tx *bbolt.Tx) error {
		bucket := tx.Bucket([]byte("enrollment-v1"))
		if bucket == nil {
			return nil
		}
		data := bucket.Get([]byte("history"))
		var row toolEnrollmentHistory
		if len(data) > 16384 || json.Unmarshal(data, &row) != nil {
			return fmt.Errorf("enrollment history is malformed or oversized")
		}
		canonical, err := json.Marshal(row)
		if err != nil || !bytes.Equal(canonical, data) || row.SchemaVersion != 1 || row.Source != source || row.Root != root || (row.Mode != "cold" && row.Mode != "warm") {
			return fmt.Errorf("enrollment history identity or schema differs")
		}
		if err := validateToolInstall(ToolGenerationInstall{Path: "store", ObjectID: row.StoreIdentity}); err != nil {
			return fmt.Errorf("enrollment history store identity is invalid")
		}
		if row.Mode == "warm" && !row.SourcePresent {
			return fmt.Errorf("warm enrollment history has no original source")
		}
		history = &row
		return nil
	})
	return history, err
}

func storeToolEnrollmentHistory(db *bbolt.DB, history toolEnrollmentHistory) error {
	data, err := json.Marshal(history)
	if err != nil {
		return err
	}
	if len(data) > 16384 {
		return fmt.Errorf("enrollment history encoding exceeds bound")
	}
	return db.Update(func(tx *bbolt.Tx) error {
		bucket, err := tx.CreateBucketIfNotExists([]byte("enrollment-v1"))
		if err != nil {
			return err
		}
		return bucket.Put([]byte("history"), data)
	})
}

func beginToolEnrollmentHistory(db *bbolt.DB, source, root string) (*toolEnrollmentHistory, error) {
	if _, err := os.Lstat(root); !os.IsNotExist(err) {
		return nil, fmt.Errorf("native store already exists without enrollment history; explicit reconciliation required")
	}
	cold, err := toolEnrollmentSourceIsCold(source)
	if err != nil {
		return nil, err
	}
	mode := "warm"
	if cold {
		mode = "cold"
	}
	_, sourceErr := os.Lstat(source)
	if sourceErr != nil && !os.IsNotExist(sourceErr) {
		return nil, sourceErr
	}
	if !cold && os.IsNotExist(sourceErr) {
		return nil, fmt.Errorf("legacy source disappeared during initial enrollment decision")
	}
	var identity [32]byte
	if _, err := rand.Read(identity[:]); err != nil {
		return nil, err
	}
	history := toolEnrollmentHistory{SchemaVersion: 1, Source: source, Root: root, Mode: mode, StoreIdentity: hex.EncodeToString(identity[:]), SourcePresent: sourceErr == nil}
	if err := storeToolEnrollmentHistory(db, history); err != nil {
		return nil, err
	}
	return &history, nil
}

func toolEnrollmentSourceIsCold(source string) (bool, error) {
	info, err := os.Lstat(source)
	if os.IsNotExist(err) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if !info.IsDir() {
		return false, fmt.Errorf("legacy source is not a non-symlink directory")
	}
	parentMount, err := toolMountID(filepath.Dir(source))
	if err != nil {
		return false, err
	}
	mount, err := toolMountID(source)
	if err != nil || mount != parentMount {
		return false, fmt.Errorf("legacy enrollment source crosses mount boundary: %v", err)
	}
	file, err := os.Open(source)
	if err != nil {
		return false, err
	}
	defer file.Close()
	entries, err := file.ReadDir(1)
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	return len(entries) == 0, nil
}
