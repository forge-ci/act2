//go:build linux

package artifactcache

import (
	"context"
	"fmt"
	"io/fs"
	"strings"
	"syscall"
	"time"
)

func auditToolStoreUsage(ctx context.Context, root string, maxEntries int) (report ToolStoreUsage) {
	report = ToolStoreUsage{SchemaVersion: 1, Root: root, ObservedAt: time.Now().UTC()}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if maxEntries <= 0 || maxEntries > 1000000 {
		report.fail(fmt.Errorf("tool store inventory requires an entry bound of 1..1000000"))
		return report
	}
	guard := ToolGenerationSpec{SchemaVersion: 1, Installs: []ToolGenerationInstall{{Path: "inventory", ObjectID: strings.Repeat("0", 64)}}}
	if _, err := validateToolGenerationSpec(root, guard, 1); err != nil {
		report.fail(err)
		return report
	}
	catalog, err := prepareExistingToolSnapshotStore(root)
	if err != nil {
		report.fail(err)
		return report
	}
	defer catalog.Close()
	return auditToolStoreUsageLocked(ctx, root, maxEntries)
}

// Caller holds catalog exclusion. Accounting includes private stages and control
// files but reads only metadata; it does not establish eligibility for deletion.
func auditToolStoreUsageLocked(ctx context.Context, root string, maxEntries int) (report ToolStoreUsage) {
	report = ToolStoreUsage{SchemaVersion: 1, Root: root, ObservedAt: time.Now().UTC()}
	var counter inodeUsageCounter
	err := walkToolTreeWithDepth(ctx, root, 0, 72, func(_ string, entry fs.DirEntry) error {
		if counter.usage.PathEntries >= maxEntries {
			return fmt.Errorf("tool store inventory entry bound exceeded")
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return fmt.Errorf("tool store inode metadata unavailable")
		}
		apparent := info.Mode().IsRegular() || info.IsDir() || info.Mode()&fs.ModeSymlink != 0
		return counter.add(stat.Dev, stat.Ino, info.Size(), stat.Blocks, apparent, info.Mode().IsRegular())
	})
	report.InodeUsage = counter.report(err == nil)
	if err != nil {
		report.fail(err)
		return report
	}
	report.ObservedAt = time.Now().UTC()
	return report
}
