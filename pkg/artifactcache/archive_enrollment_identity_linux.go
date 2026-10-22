//go:build linux

package artifactcache

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"go.etcd.io/bbolt"
	"golang.org/x/sys/unix"
)

const archiveDestinationMarker = ".archive-route-identity-v1"

var archiveIdentityBucket = []byte("act2-archive-routing-v1")

type archiveFileIdentity struct {
	device uint64
	inode  uint64
}
type archiveDestinationIdentity struct {
	directory archiveFileIdentity
	metadata  archiveFileIdentity
}

func archivePathIdentity(path string, directory bool) (archiveFileIdentity, error) {
	var stat unix.Stat_t
	if err := unix.Lstat(path, &stat); err != nil {
		return archiveFileIdentity{}, err
	}
	expected := uint32(unix.S_IFREG)
	if directory {
		expected = unix.S_IFDIR
	}
	if stat.Mode&unix.S_IFMT != expected || !directory && stat.Nlink != 1 {
		return archiveFileIdentity{}, fmt.Errorf("archive destination is aliased or has changed type")
	}
	return archiveFileIdentity{device: stat.Dev, inode: stat.Ino}, nil
}
func currentArchiveDestinationIdentity(h *archiveEnrollmentHistory) (archiveDestinationIdentity, error) {
	destination := filepath.Join(h.Root, h.Namespace)
	if err := requireToolEnrollmentDirectory(destination, h.Root); err != nil {
		return archiveDestinationIdentity{}, err
	}
	directory, err := archivePathIdentity(destination, true)
	if err != nil {
		return archiveDestinationIdentity{}, err
	}
	metadata, err := archivePathIdentity(filepath.Join(destination, "bolt.db"), false)
	if err != nil {
		return archiveDestinationIdentity{}, err
	}
	return archiveDestinationIdentity{directory: directory, metadata: metadata}, nil
}
func openArchiveIdentityDB(h *archiveEnrollmentHistory, readOnly bool) (*bbolt.DB, error) {
	destination := filepath.Join(h.Root, h.Namespace)
	return bbolt.Open(filepath.Join(destination, "bolt.db"), 0600, &bbolt.Options{
		ReadOnly: readOnly, Timeout: 25 * time.Millisecond,
		OpenFile: func(name string, flags int, mode os.FileMode) (*os.File, error) {
			fd, err := unix.Open(name, (flags&^unix.O_CREAT)|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, uint32(mode.Perm()))
			if err != nil {
				return nil, err
			}
			file := os.NewFile(uintptr(fd), name)
			var stat unix.Stat_t
			if err = unix.Fstat(fd, &stat); err != nil {
				return nil, errors.Join(err, file.Close())
			}
			if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 || stat.Size > auditMaxMetadataBytes {
				return nil, errors.Join(fmt.Errorf("archive metadata is not a bounded unaliased regular file"), file.Close())
			}
			parentMount, mountErr := toolMountID(destination)
			if mountErr != nil {
				return nil, errors.Join(mountErr, file.Close())
			}
			fileMount, mountErr := toolMountID(name)
			if mountErr != nil || parentMount != fileMount {
				return nil, errors.Join(fmt.Errorf("archive metadata crosses mount boundary: %v", mountErr), file.Close())
			}
			return file, nil
		},
	})
}

// Persist an incarnation in both directory and metadata. Device numbers belong
// to mounts and are only compared within one operation, never across restarts.
func freezeArchiveDestinationIdentity(h *archiveEnrollmentHistory) error {
	before, err := currentArchiveDestinationIdentity(h)
	if err != nil {
		return err
	}
	destination := filepath.Join(h.Root, h.Namespace)
	if err = prepareToolEnrollmentMarker(destination, archiveDestinationMarker, archiveFenceBytes(h)); err != nil {
		return err
	}
	db, err := openArchiveIdentityDB(h, false)
	if err != nil {
		return err
	}
	writeErr := db.Update(func(tx *bbolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists(archiveIdentityBucket)
		if err != nil {
			return err
		}
		value := b.Get([]byte("incarnation"))
		if value != nil && !bytes.Equal(value, []byte(h.Identity)) {
			return fmt.Errorf("archive metadata incarnation differs")
		}
		return b.Put([]byte("incarnation"), []byte(h.Identity))
	})
	if err = errors.Join(writeErr, db.Close()); err != nil {
		return err
	}
	after, err := currentArchiveDestinationIdentity(h)
	if err != nil {
		return err
	}
	if before != after {
		return fmt.Errorf("archive destination changed during enrollment")
	}
	return nil
}
func requireArchiveDestinationIdentity(h *archiveEnrollmentHistory) error {
	before, err := currentArchiveDestinationIdentity(h)
	if err != nil {
		return err
	}
	destination := filepath.Join(h.Root, h.Namespace)
	if err = requireToolEnrollmentMarker(destination, archiveDestinationMarker, archiveFenceBytes(h)); err != nil {
		return err
	}
	db, err := openArchiveIdentityDB(h, true)
	if err != nil {
		return err
	}
	readErr := db.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket(archiveIdentityBucket)
		if b == nil || !bytes.Equal(b.Get([]byte("incarnation")), []byte(h.Identity)) {
			return fmt.Errorf("archive metadata incarnation differs or is missing")
		}
		return nil
	})
	if err = errors.Join(readErr, db.Close()); err != nil {
		return err
	}
	after, err := currentArchiveDestinationIdentity(h)
	if err != nil {
		return err
	}
	if before != after {
		return fmt.Errorf("archive destination changed during admission")
	}
	return nil
}
