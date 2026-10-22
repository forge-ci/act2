package artifactcache

import (
	"os"
	"path/filepath"
)

// lstatInParent anchors metadata lookup to a directory and never follows the
// final component. Callers still verify directory type, ownership and mount.
func lstatInParent(path string) (os.FileInfo, error) {
	parent, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	return parent.Lstat(filepath.Base(path))
}
