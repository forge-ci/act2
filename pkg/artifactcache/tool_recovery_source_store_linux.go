//go:build linux

package artifactcache

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

func enrollToolRecoverySourceStore(ctx context.Context, root string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateToolNativeStoreRoot(root); err != nil {
		return err
	}
	catalog, err := prepareToolSnapshotStoreWithInitialMarker(root, func(path string) (transferLease, error) {
		return openToolGenerationUpdateCatalog(ctx, path)
	}, toolStoreSourceRecoveryMagic)
	if err != nil {
		return err
	}
	defer catalog.Close()
	marker, err := os.ReadFile(filepath.Join(root, toolStoreMarker))
	if err != nil {
		return err
	}
	if string(marker) != toolStoreSourceRecoveryMagic {
		return fmt.Errorf("source store enrollment refuses older stores; create a new empty store before bootstrap")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return syncToolDirectory(root)
}
