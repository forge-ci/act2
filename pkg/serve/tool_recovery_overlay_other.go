//go:build !linux

package serve

import (
	"context"
	"fmt"

	"github.com/nektos/act/pkg/artifactcache"
)

func openToolRecoveryOverlay(context.Context, ToolRecoveryOverlayRequest) (artifactcache.ToolRecoverySourceView, error) {
	return artifactcache.ToolRecoverySourceView{}, fmt.Errorf("tool overlay recovery requires Linux")
}
