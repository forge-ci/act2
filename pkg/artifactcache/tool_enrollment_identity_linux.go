//go:build linux

package artifactcache

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

const toolEnrollmentIdentityName = ".tool-enrollment-identity-v1"

func toolEnrollmentIdentityBytes(identity string) []byte {
	return []byte("bosn-tool-enrollment-v1\n" + identity + "\n")
}

// Pending history freezes these exact bytes before the native root exists.
// Under both enrollment and catalog exclusion, a partial initial write can be
// completed without changing identity. Ready admission never creates/repairs it.
func prepareToolEnrollmentIdentity(root, identity string) error {
	catalog, err := prepareExistingToolSnapshotStore(root)
	if err != nil {
		return err
	}
	defer catalog.Close()
	return prepareToolEnrollmentMarker(root, toolEnrollmentIdentityName, toolEnrollmentIdentityBytes(identity))
}

func prepareToolEnrollmentMarker(directory, name string, expected []byte) error {
	file, err := openToolEnrollmentMarker(directory, name, unix.O_RDWR|unix.O_CREAT, len(expected))
	if err != nil {
		return err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, int64(len(expected)+1)))
	if err != nil {
		return err
	}
	if !bytes.HasPrefix(expected, data) {
		return fmt.Errorf("pending enrollment store identity differs")
	}
	if !bytes.Equal(expected, data) {
		if _, err := file.WriteAt(expected, 0); err != nil {
			return err
		}
	}
	if err := file.Sync(); err != nil {
		return err
	}
	return syncToolDirectory(directory)
}

func requireToolEnrollmentIdentity(root, identity string) error {
	return requireToolEnrollmentMarker(root, toolEnrollmentIdentityName, toolEnrollmentIdentityBytes(identity))
}

func requireToolEnrollmentMarker(directory, name string, expected []byte) error {
	file, err := openToolEnrollmentMarker(directory, name, unix.O_RDONLY, len(expected))
	if err != nil {
		return err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, int64(len(expected)+1)))
	if err != nil {
		return err
	}
	if !bytes.Equal(data, expected) {
		return fmt.Errorf("enrolled native store identity differs or is incomplete")
	}
	return nil
}

func openToolEnrollmentMarker(root, name string, flags, maxBytes int) (*os.File, error) {
	path := filepath.Join(root, name)
	fd, err := unix.Open(path, flags|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0600)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return nil, errors.Join(err, file.Close())
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 || stat.Size > int64(maxBytes) {
		return nil, errors.Join(fmt.Errorf("enrollment identity must be a bounded unaliased regular file"), file.Close())
	}
	rootMount, err := toolMountID(root)
	if err != nil {
		return nil, errors.Join(err, file.Close())
	}
	mount, err := toolMountID(path)
	if err != nil || mount != rootMount {
		return nil, errors.Join(fmt.Errorf("enrollment identity crosses mount boundary: %v", err), file.Close())
	}
	return file, nil
}
