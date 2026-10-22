package artifactcache

import (
	"context"
	"time"
)

// InodeUsage separates metadata allocation, unique payload and references.
// Component totals overlap when hardlinks cross classes; only Total deduplicates
// the entire root. Unknown byte totals are null on an incomplete traversal.
type InodeUsage struct {
	FilesystemDevice    *uint64 `json:"filesystem_device"`
	PathEntries         int     `json:"path_entries"`
	UniqueInodes        int     `json:"unique_inodes"`
	ApparentBytes       *int64  `json:"apparent_bytes"`
	AllocatedBytes      *int64  `json:"allocated_bytes"`
	UniqueFileBytes     *int64  `json:"unique_file_bytes"`
	ReferencedFileBytes *int64  `json:"referenced_file_bytes"`
}

type CacheUsageComponent struct {
	Class string     `json:"class"`
	Usage InodeUsage `json:"usage"`
}

// CacheFootprint is a read-only non-atomic observation of one caller-selected
// cache root, including stages, journals and unknown files. It is neither a
// filesystem quota nor proof that any entry is eligible for removal. Traversal
// never follows symlinks or crosses mount boundaries; no file contents are read.
type CacheUsageNamespace struct {
	Namespace string     `json:"namespace"`
	StorePath string     `json:"store_path"`
	Usage     InodeUsage `json:"usage"`
}

type CacheFootprint struct {
	SchemaVersion     int                   `json:"schema_version"`
	Root              string                `json:"root"`
	ObservedAt        time.Time             `json:"observed_at"`
	Total             InodeUsage            `json:"total"`
	Components        []CacheUsageComponent `json:"components"`
	Namespaces        []CacheUsageNamespace `json:"namespaces"`
	OmittedNamespaces int                   `json:"omitted_namespaces"`
	Partial           bool                  `json:"partial"`
	Error             string                `json:"error,omitempty"`
}

func AuditCacheFootprint(ctx context.Context, root string, maxEntries int) CacheFootprint {
	return auditCacheFootprint(ctx, root, maxEntries)
}
