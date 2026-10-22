//go:build !linux

package artifactcache

import (
	"context"
	"time"
)

func auditCacheFootprint(_ context.Context, root string, _ int) CacheFootprint {
	return CacheFootprint{SchemaVersion: 1, Root: root, ObservedAt: time.Now().UTC(), Partial: true, Error: "cache inode allocation requires Linux"}
}
