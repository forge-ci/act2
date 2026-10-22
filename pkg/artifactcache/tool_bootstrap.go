package artifactcache

import "context"

// BootstrapToolGeneration deliberately migrates a quiescent legacy tool-cache
// tree into the first selected immutable generation. The caller must exclude
// all source writers, including older peers, throughout this operation. Missing,
// empty, oversized and invalid sources are errors, never empty warm generations.
// A valid existing selection is verified and reconciled without reading source;
// a corrupt selection or payload is refused rather than replaced. This is an
// explicit enrollment operation, not an ordinary engine-start fallback.
func BootstrapToolGeneration(ctx context.Context, source, root string, maxBytes int64) ToolGenerationUpdateReport {
	return bootstrapToolGeneration(ctx, source, root, maxBytes)
}
