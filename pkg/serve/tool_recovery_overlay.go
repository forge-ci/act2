package serve

import (
	"context"
	"fmt"
	"path"

	"github.com/nektos/act/pkg/artifactcache"
)

const nativeToolSourceRoot = "/var/lib/docker"
const nativeToolUpperName = "bosn-tools-upper"
const nativeToolWorkName = "bosn-tools-work"

// ToolRecoveryOverlayRequest binds a stopped engine's disk to its frozen
// producer. Docker source ownership, writer absence and lifetime exclusion are
// the caller's responsibility; this API only recovers and publishes that view.
type ToolRecoveryOverlayRequest struct {
	Generation     ToolGeneration
	Pin            artifactcache.ToolRecoveryPin
	SourceRoot     string
	ToolCacheMount string
	MaxBytes       int64
}

func RecoverToolCache(ctx context.Context, request ToolRecoveryOverlayRequest) artifactcache.ToolRecoveryPublicationReport {
	refused := artifactcache.ToolRecoveryPublicationReport{SchemaVersion: 1, Root: request.Generation.Store, Pin: request.Pin, Partial: true}
	if err := validateToolRecoveryProducer(request); err != nil {
		refused.Error = err.Error()
		return refused
	}
	return artifactcache.PublishToolRecoveryView(ctx, request.Generation.Store, request.Pin, request.MaxBytes, func(ctx context.Context) (artifactcache.ToolRecoverySourceView, error) {
		return openToolRecoveryOverlay(ctx, request)
	})
}

func validateToolRecoveryProducer(request ToolRecoveryOverlayRequest) error {
	if request.Generation.ID != request.Pin.Generation {
		return fmt.Errorf("recovery lower differs from frozen intent")
	}
	if !path.IsAbs(request.ToolCacheMount) || path.Clean(request.ToolCacheMount) != request.ToolCacheMount {
		return fmt.Errorf("recovery producer target must be canonical and absolute")
	}
	server := &Server{cfg: Config{ToolCacheMount: request.ToolCacheMount}}
	_, err := server.toolcacheReady(&request.Generation)
	return err
}
