//go:build linux

package artifactcache

import (
	"context"
	"path/filepath"

	"golang.org/x/sync/errgroup"
)

// A small fixed pool lets file syncs share journal commits without retaining
// unbounded descriptors. Every original writer is still synced before success.
// The group always drains before a caller can remove or publish the stage.
func copyToolSnapshotEntries(ctx context.Context, source, payload string, entries []toolEntry, copyEntry func(context.Context, string, string, toolEntry) error) error {
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.Path == "." || entry.Kind == "file" {
			continue
		}
		if err := copyEntry(ctx, filepath.Join(source, entry.Path), filepath.Join(payload, entry.Path), entry); err != nil {
			return err
		}
	}
	group, copyCtx := errgroup.WithContext(ctx)
	group.SetLimit(4)
	for _, entry := range entries {
		if entry.Kind != "file" {
			continue
		}
		if copyCtx.Err() != nil {
			break
		}
		group.Go(func() error {
			return copyEntry(copyCtx, filepath.Join(source, entry.Path), filepath.Join(payload, entry.Path), entry)
		})
	}
	if err := group.Wait(); err != nil {
		return err
	}
	return ctx.Err()
}
