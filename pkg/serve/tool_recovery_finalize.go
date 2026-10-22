package serve

import (
	"context"

	"github.com/nektos/act/pkg/artifactcache"
)

// FinalizeToolCache owns publication/release/fencing order. The caller still
// proves source ownership, writer absence and the helper namespace's teardown.
func FinalizeToolCache(ctx context.Context, request ToolRecoveryOverlayRequest) artifactcache.ToolRecoveryFinalizationReport {
	if request.Generation.ID != request.Pin.Generation {
		return artifactcache.ToolRecoveryFinalizationReport{SchemaVersion: 1, Root: request.Generation.Store,
			Pin: request.Pin, Partial: true, Error: "recovery lower differs from frozen intent"}
	}
	return artifactcache.FinalizeToolRecoverySource(ctx, request.Generation.Store, request.Pin, request.MaxBytes,
		func(ctx context.Context) (artifactcache.ToolRecoverySourceView, error) {
			if err := validateToolRecoveryProducer(request); err != nil {
				return artifactcache.ToolRecoverySourceView{}, err
			}
			return openToolRecoveryOverlay(ctx, request)
		})
}
