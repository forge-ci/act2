//go:build !linux

package artifactcache

import (
	"context"
	"fmt"
)

func enrollToolRecoverySourceStore(context.Context, string) error {
	return fmt.Errorf("native source stores require Linux")
}
func fenceToolRecoverySourceDisposal(context.Context, string, ToolRecoveryPin) error {
	return fmt.Errorf("native source disposal requires Linux")
}
