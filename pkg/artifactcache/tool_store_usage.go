package artifactcache

import (
	"context"
	"time"
)

// ToolStoreUsage describes one catalog-coordinated tool-store inventory, not
// machine-wide filesystem usage. Byte totals are unknown (null) on incomplete
// scans. Allocated bytes use inode blocks, not filesystem journal/backing-store
// allocation or a quota. Cross-store hardlinks require broader deduplication.
// Apparent bytes count st_size once per unique regular file, directory and
// symlink, including directory metadata regardless of the installed du version.
// GNU du >=9.2 excludes directory st_size; tests independently supplement that
// oracle rather than changing this contract. Allocated bytes include blocks of
// every inode type. Referenced file bytes count all regular-file paths and
// deliberately do not deduplicate hardlinks.
type ToolStoreUsage struct {
	SchemaVersion int       `json:"schema_version"`
	Root          string    `json:"root"`
	ObservedAt    time.Time `json:"observed_at"`
	InodeUsage
	Partial bool   `json:"partial"`
	Error   string `json:"error,omitempty"`
}

func (r *ToolStoreUsage) fail(err error) {
	r.Partial, r.Error = true, toolReportError(err)
}

// AuditToolStoreUsage bounds metadata traversal under the original catalog
// writer lock, counts (device,inode) once, and never follows symlinks. All store
// paths, including control files and stages, contribute to this scoped report.
func AuditToolStoreUsage(ctx context.Context, root string, maxEntries int) ToolStoreUsage {
	return auditToolStoreUsage(ctx, root, maxEntries)
}
