//go:build !linux

package artifactcache

import (
	"context"
	"fmt"
)

func finalizeToolRecoverySource(_ context.Context, root string, pin ToolRecoveryPin, _ int64, _ ToolRecoverySourceFactory) ToolRecoveryFinalizationReport {
	report := ToolRecoveryFinalizationReport{SchemaVersion: 1, Root: root, Pin: pin}
	report.fail(fmt.Errorf("source finalization requires Linux"))
	return report
}
